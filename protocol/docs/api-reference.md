# API reference

Everything is under `/v1` except `/healthz` and `/join`. The protocol behind these endpoints is in the [E2EE Protocol](./e2ee-protocol.md); the WebSocket is in the [Sync Protocol](./sync-protocol.md).

## Conventions

- **Success:** `{"data": …}`
- **Error:** `{"error": {"code": "…", "message": "…"}}`. `message` is optional, and internal error details are never included.
- **Binary values:** base64url without padding.
- **Auth:** the `session` cookie from `/auth/login`. 🔒 in the tables below means a session is required.
- **Origin:** every `POST`/`PUT`/`PATCH`/`DELETE` and the WebSocket upgrade must send `Origin: <ORIGIN>`.
- **Body size:** 1 MiB maximum for HTTP bodies. WebSocket frames have their own limits.

## Endpoints

### Health

| | Path | |
|---|---|---|
| `GET` | `/healthz` | `ok`, or `503` if the database is unreachable |
| `GET` | `/join` | The invite page (HTML). An invite link is `<server>/join#<s>`; the page hands it to the installed app as `runer://join?host=<server>#<s>`. Static: the fragment never reaches the server |

### Auth and sessions

| | Path | | Body → data |
|---|---|---|---|
| `POST` | `/v1/auth/challenge` | | `{auth_pub}` → `{nonce, pow_bits}` |
| `POST` | `/v1/auth/login` | | `{auth_pub, nonce, sig, [identity_sign_pub, identity_agree_pub, identity_bundle, identity_sig, pow_nonce, inbox_entry, inbox_sig]}` → `{account_id, bundle_gen, session_id, inbox_id}` |
| `POST` | `/v1/auth/logout` | 🔒 | → `{revoked}` |
| `GET` | `/v1/sessions` | 🔒 | → `[{id, user_agent, created_at, last_seen_at, current}]` (`current`: the asking session) |
| `DELETE` | `/v1/sessions/{id}` | 🔒 | → `{revoked}` |

**Sign-up Inbox.** A sign-up also sends `inbox_entry` and `inbox_sig` (base64url): the signed `create` of the account's Inbox, checked as `POST /v1/groups` checks one, with `author` = `identity_sign_pub` and `author_agree` = `identity_agree_pub`. The account and its Inbox are created together; missing or invalid → `400 bad_request` and no account. `inbox_id` is in every login response (`null` only for accounts older than the Inbox).

**No personal account info.** A sign-up sends keys and nothing else about the person: the server stores no name and no email ([E2EE §3.3](./e2ee-protocol.md#33-what-the-server-stores-per-account)). Unknown fields in the body are ignored.

**Sign-up proof-of-work.** A first login (sign-up) needs `pow_nonce` such that `SHA-256(auth_pub ‖ nonce ‖ pow_nonce)` (raw bytes) has at least `pow_bits` leading zero bits. Use the `pow_bits` from the challenge: it's the same for every caller on a server (so it reveals nothing about whether an account exists), but operators can change it (`SIGNUP_POW_BITS`, [Self-hosting](../../docs/self-hosting.md#configuration)). Never hardcode it.

### Account

| | Path | | Body → data |
|---|---|---|---|
| `GET` | `/v1/account` | 🔒 | → `{id, identity_sign_pub, identity_agree_pub, bundle_gen, created_at, last_seen_at, inbox_id, settings_sealed, settings_gen}`. `settings_sealed` is `null` until first set |
| `PUT` | `/v1/account/settings` | 🔒 | `{settings_sealed, settings_gen}` → `{settings_gen}`. Replaces the sealed settings ([E2EE §3.3](./e2ee-protocol.md#33-what-the-server-stores-per-account)); 29..16384 bytes decoded (`400 bad_request`). `settings_gen` must be the current one (0 before the first write), else `409 conflict`: refetch, reapply, retry |
| `GET` | `/v1/account/bundle` | 🔒 | → `{identity_bundle, bundle_gen}` |
| `POST` | `/v1/account/rekey/challenge` | 🔒 | → `{nonce}` |
| `POST` | `/v1/account/rekey` | 🔒 | `{nonce, sig, new_auth_pub, new_bundle, bundle_gen}` → `{bundle_gen}` |
| `DELETE` | `/v1/account` | 🔒 | → `{scheduled}`. Deletes the account and every list it owns, and ends every session. `409 account_has_lists` while the account is a member of a list it doesn't own: see below |

**Deleting an account.** Every list the account owns is deleted with it, at once and for all members: its Inbox and the lists it shares, as `DELETE /v1/groups/{id}` would (invites revoked, members sent a `group` frame). There is no ownership transfer. A list the account is only a member of is different: only a signed `leave` takes a member out of a log ([E2EE §8.4](./e2ee-protocol.md#84-deleting-an-account)), and the server can't sign one. So while the account is a member of a list it doesn't own, the server refuses with `409 account_has_lists` and changes nothing; the client appends a `leave` to each such list, then calls `DELETE /v1/account`. On success every session of the account ends at once (open sockets get `bye` with `session-revoked`), the key can no longer log in, and the identity bundle and sealed settings are cleared. What remains, and for how long, is under [Data retention](../../docs/self-hosting.md#data-retention).

### Lists

A list is called a group in the API: it's a membership log plus one encrypted document.

| | Path | | Body → data |
|---|---|---|---|
| `GET` | `/v1/groups` | 🔒 | → `[{id, head_seq, head_hash, epoch}]`, the lists you're a member of |
| `POST` | `/v1/groups` | 🔒 | `{entry, sig}` (a `create`) → list (`201`) |
| `GET` | `/v1/groups/{id}/log?from=N` | 🔒 | → `[{seq, entry, sig, hash}]` |
| `POST` | `/v1/groups/{id}/log` | 🔒 | `{entry, sig}` → list. `409` if the head moved; `403` for an `add` or `invite` on an account's Inbox |
| `GET` | `/v1/groups/{id}/members` | 🔒 | → `[{account_id, identity_sign_pub, role}]`, role `owner` or `member`. A convenience: clients trust the log |
| `DELETE` | `/v1/groups/{id}` | 🔒 owner | → `{deleted}`. Also revokes its invites. `403` on an account's Inbox |

### Invites

| | Path | | Body → data |
|---|---|---|---|
| `POST` | `/v1/groups/{id}/invites` | 🔒 owner | `{invite_id, package, expires_at, [invite_pub]}` → `{id, expires_at}` (`201`). `invite_pub` makes it a link invite: an open `invite` entry for that key must be in the log and expire no earlier than `expires_at`, else `409` |
| `GET` | `/v1/groups/{id}/join-requests` | 🔒 owner | → `[{id, invite_id, account_id, identity_sign_pub, identity_agree_pub, enc_display_name, mac}]` |
| `GET` | `/v1/invites/{invite_id}` | none | → `{package}` |
| `POST` | `/v1/invites/{invite_id}/join` | 🔒 | `{enc_display_name, mac}` → `{id}` (`201`) |
| `DELETE` | `/v1/invites/{invite_id}` | 🔒 owner | → `{revoked}`. For a link invite this only stops serving the package; append `uninvite` to close it |
| `GET` | `/v1/invites/{invite_id}/log?from=N` | 🔒 | → `[{seq, entry, sig, hash}]`, the list's log, for an open link invite. The invitee isn't a member yet |
| `POST` | `/v1/invites/{invite_id}/accept` | 🔒 | `{entry, sig}` (the joiner's own `join`) → list. `409` if the head moved, `410` if the invite closed |

Two kinds of invite ([E2EE §6](./e2ee-protocol.md)). A **link invite** is consumed by the invitee's own `join` entry, sent to `/accept`; its package stops being served once the log closes the invite (join, `uninvite`, or a new epoch). An **approve-first invite** goes through join requests and is consumed when the owner's `add` entry citing it (`invite_proof`) is appended.

### Sync

| | Path | | |
|---|---|---|---|
| `GET` | `/v1/sync` | 🔒 | WebSocket; frames in the [Sync Protocol](./sync-protocol.md) |

## HTTP error codes

| Status | `code` | Typical cause |
|---|---|---|
| 400 | `bad_request` | Malformed body, invalid log entry, expiry too far out |
| 401 | `invalid_credentials` | Login failed (for any reason; deliberately vague) |
| 401 | `unauthenticated` | No session, or it expired: sign in again with the key |
| 401 | `session_revoked` | The session was ended (logout, revoked from another device, account key changed). Don't sign back in silently: stop and ask the user |
| 403 | `origin_mismatch` | Missing or wrong `Origin` |
| 403 | `forbidden` | Not a member or not the owner; deleting or sharing an Inbox. Also returned for ids that don't exist. |
| 404 | `not_found` | Unknown session, invite and similar |
| 409 | `account_has_lists` | `DELETE /v1/account` while the account is a member of a list it doesn't own: leave those lists first |
| 409 | `conflict` | Log head moved (refetch and retry), stale `bundle_gen` or `settings_gen`, already exists |
| 410 | `invite_not_usable` | Invite expired, revoked, consumed, or voided by a new epoch |
| 413 | `too_large` | Size limit |
| 429 | `rate_limited` | Slow down |
| 500 | `internal` | Server bug; the details are only in the server log |
