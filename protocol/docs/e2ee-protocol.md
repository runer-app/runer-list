# E2EE Protocol — Accounts, Keys, Lists & Sharing

**Related:** [Sync Protocol](./sync-protocol.md) · [API Reference](./api-reference.md)

> The reference for identity, key management and list membership. Code comments cite it as **E2EE §n**. The client must match it.

---

# 1. What this protocol must guarantee

> **A malicious or compromised server can withhold data and observe metadata, but cannot read content, cannot insert itself into a list, cannot substitute keys during an invite, and cannot forge edits.**

| Attacker | Can | Cannot |
|---|---|---|
| Database thief / backup leak | See metadata (which accounts share which list with whom, when, how much) and ciphertext. Accounts are keys and ids: the server holds no names or emails, so the sharing graph is between pseudonymous accounts | Read content or titles, log in as a user |
| Malicious server operator | Withhold/delay data, see metadata, show stale state or hide a removal (detectable, §4.4) | Read content, add members, swap keys at invite, forge edits or membership |
| Member of a shared list | Edit or delete anything in that list; rotate its key | Forge another member's authorship; add or remove people; read other lists |
| Someone who intercepts a link invite (§6.1) | Join in the invitee's place and read the list until the owner removes them | Join after the invite is used, revoked, expired or voided by a new epoch; hide that they joined |
| Someone who intercepts an approve-first invite (§6.2) | Ask to join in the invitee's place | Join without the owner approving; pass a safety-code check |
| Removed member | Keep what they already decrypted; with a **colluding** server, read what others still encrypt under the old key until they switch (§8.3) | Read anything encrypted under a key created after their removal |
| Thief of an **unlocked, logged-in** device | Act as that user until they respond (§9) | Continue after account key change + session revocation |
| Phished account key | Take over that user until key change | Affect other users' keys |

**Client distribution is part of the trusted base.** The client is a **signed Tauri app** (desktop, and mobile via Tauri 2). The server never delivers executable code, so a compromised server cannot push a key-stealing build. A web client, if ever offered, must be served from a separate static origin and carries the classic caveat: whoever serves the JavaScript can steal keys.

---

# 2. Decisions at a glance

| Question | Decision |
|---|---|
| Login | A random **Account Key**: 12 words, shown once. No username, no password, no email. |
| Account info | None on the server: no name, no email. An account is its keys and an id. An optional account name is client data, kept in the sealed account settings (§3.3). |
| Who holds keys | Each **user** has one identity key pair, cached on their devices. |
| Unit of sharing | A **list**. Each list has its own membership log and key. A personal list is a list with one member; the Inbox is one too. |
| Inbox | Every account has an Inbox: a private list created with the account, which can't be deleted or shared. The server records which list it is; clients check in the verified log that they own it and are its only member. |
| Who decides membership | The list's **owner** (its creator), through a **signed, hash-chained log**. Clients verify it; the server only mirrors it. |
| Invites | Link with a secret in the URL fragment. By default the link itself admits one person, even while the owner is offline (§6.1); an approve-first variant keeps the owner in the loop (§6.2). Optional safety code. |
| Removal | Immediate **key rotation** (new epoch). |
| Recovery | The Account Key. Lose it and every device, and personal lists are gone; shared lists are recovered by being re-invited. |
| MLS | Not used (§12). |

---

# 3. Accounts

## 3.1 The Account Key

Signup generates an Account Key and shows it once; the user re-enters three random words to confirm. Signing in anywhere means typing the 12 words.

Format: 128 random bits as 12 BIP-39 English words. The checksum catches typos before any request. The entropy is used directly as HKDF input; BIP-39's PBKDF2 seed step is not used.

## 3.2 What is derived from it

All derivation is HKDF-SHA-256 with distinct labels (exact bytes in §10.1):

```text
Account Key (128-bit)
   ├─ HKDF "runer/v1/auth" → Ed25519 seed → Auth key pair   (server stores the public half; login signs a challenge)
   └─ HKDF "runer/v1/wrap" → AES-256-GCM key                (decrypts the Identity Bundle)
```

The HKDF input is the 16 raw entropy bytes.

The **Identity key pair** (Ed25519 signing + X25519 agreement) is generated randomly at signup, not derived. Its private halves are stored on the server encrypted under the wrap key: the **Identity Bundle**. Keeping identity separate from the Account Key means the Account Key can change without collaborators noticing (§9.2).

## 3.3 What the server stores per account

| Field | Why |
|---|---|
| `auth_pub` | Verify login challenges. Unique. |
| `identity_sign_pub`, `identity_agree_pub` | Others look up keys, but always verify them via a log or invite (§6). Unique, so one identity can't be registered twice. |
| `identity_bundle`, `bundle_gen` | Ciphertext for new devices: `nonce(12) ‖ AES-256-GCM(wrap, 0x01 ‖ sign_seed(32) ‖ agree_private(32), AD = auth_pub ‖ uint64be(bundle_gen))`. The leading `0x01` is the bundle format version. `bundle_gen` increments on every rekey; clients warn if the server serves a lower one. |
| `settings_sealed` (nullable), `settings_gen` | The account's settings, the same on every device. Opaque to the server: `nonce(12) ‖ AES-256-GCM(HKDF(identity_agree_private, "runer/v1/account-settings"), UTF-8 JSON, AD = "runer/v1/account-settings" ‖ 0x00 ‖ identity_sign_pub)`. The JSON is a client concern: an object with a format version `v`, and unknown fields from a newer client are kept, never dropped. The optional account name lives in this JSON, so the server never sees it; the name collaborators see is the one the client copies into each shared list's encrypted document (and `owner_name` and `enc_display_name` when inviting and joining, §6), any of which may be empty. Empty until first set, replaced with `PUT /account/settings`. `settings_gen` increments on every write, and a write must name the current one: the server can't merge what it can't read, so a device that lost the race refetches, reapplies its change and retries. |
| `created_at`, `last_seen_at` | Activity. |

The Account Key has 128 bits of entropy, so a stolen database offers no offline brute-force path, and no password hashing is needed. Signup requires a small proof-of-work, with a loose per-IP flood cap (many people share one IP).

## 3.4 Login

1. Derive the auth key from the Account Key.
2. `POST /auth/challenge {auth_pub}` → a 32-byte nonce, bound to `auth_pub`, single-use, valid 60 s. Same answer whether or not the account exists.
3. Sign `"runer/v1/login" || 0x00 || uint16be(len(server_url)) || server_url || nonce`. `server_url` is the API base URL **the client connected to**, never one the server supplies; the server compares it with its `PUBLIC_URL`, so a signature can't be relayed to another server. Rekey uses the same construction with `"runer/v1/rekey"`.
4. The server issues a session cookie (`HttpOnly`, `Secure`, `SameSite=Strict`) and records a session row.
5. The client fetches and decrypts the Identity Bundle.

Sessions expire after 30 days idle and 180 days absolute; the client silently re-authenticates from the stored Account Key. A session that ends is deleted; a client that can no longer log in (the key changed, the account is gone) stops and asks the user. The token is 256 random bits; the server stores its SHA-256. The client makes all HTTP and WebSocket calls from Tauri's Rust side, which holds the cookie. The server checks `Origin` on every mutation and on the WebSocket upgrade.

## 3.5 What the device keeps

- In the **OS keychain**: the Account Key and a random 256-bit device cache key.
- In the app's local database, encrypted under the device cache key: identity keys, list keys, and the **encrypted** server messages (with their `seq`), never plaintext.

Plaintext document state and the search index exist only in memory and are rebuilt at startup.

---

# 4. Lists and the membership log

## 4.1 The log

Each list has an append-only log. Every entry is signed by its author's identity key and chained to the previous one by hash:

```text
entry = {
  v: 1,
  group: <list id>,                               // omitted in genesis
  seq: n,
  prev: SHA-256(entry n-1 bytes),                 // omitted in genesis
  type: create | add | remove | leave | rotate | invite | uninvite | join,
  author: <identity_sign_pub>,
  author_agree: <identity_agree_pub>,             // create only
  subject: <identity_sign_pub of affected member>,// add/remove/leave/join
  subject_agree: <identity_agree_pub of subject>, // add and join only
  epoch: e,                                       // key epoch after this entry
  envelopes: [ {recipient, hpke_ciphertext} ],    // §5
  key_chain: AES-GCM(K_chain(e), K_{e-1}),        // only when epoch changes, §5
  invite: <link invite's Ed25519 public key>,     // invite, uninvite, join (§6.1)
  expires: <Unix milliseconds>,                   // invite only
  invite_proof: <MAC (add, §6.2) | invite key's signature (join, §6.1)>,
  ts: <Unix milliseconds>
}
signature = Ed25519(author, entry_bytes)
```

`list id = SHA-256(genesis entry bytes)`, so the id commits to the owner. Signatures are over the exact stored bytes, which are never re-serialized.

**Everything a client relies on is signed.** The encryption keys the list key is wrapped to (`author_agree`, `subject_agree`) come from the verified log, never from the server.

**Canonical encoding.** An entry is valid only if its bytes are exactly the canonical encoding of what they decode to; validators re-encode and compare:

- a JSON object with keys in the order above, lowercase, each at most once, no other keys;
- no whitespace; optional fields omitted rather than empty (no `""`, `[]` or `null`);
- integers as plain decimal in `[0, 2^53-1]`;
- binary as base64url without padding; strings raw UTF-8, no escaping beyond what JSON requires (`<`, `>`, `&` unescaped);
- envelopes as `{recipient, hpke_ciphertext}`.

This removes every way two parsers could read different values from one signature. `protocol/log-fixtures` has a signed example of each non-canonical form; all must be rejected.

## 4.2 Validity rules (identical in the client and the server)

1. `seq` and `prev` extend the current head, and `group` is this list's id.
2. `create` is only valid as seq 0. Its author becomes the **owner**, at epoch 0.
3. `add`, `remove`, `invite` and `uninvite`: the author must be the owner. `rotate`: the author must be a current member.
4. `remove` and `rotate` increase `epoch` by exactly one and carry `key_chain` and an envelope for **every remaining member** (removal and rotation are atomic).
5. Every other type keeps the epoch. `add` carries an envelope of the current key for the new member.
6. `leave`: the author must be the subject. The owner can neither leave nor be removed; they delete the list instead.
7. `ts` is within 24 hours of the previous entry's `ts`, or later. It is informational, never used for authorization, except as the clock the invite rules (9, 11) compare `expires` with.
8. **Shape.** `create` carries `author_agree` and no `group`; no other type carries `author_agree`. `add` and `join` carry `subject_agree`; no other type does. `invite`, `uninvite` and `join` carry `invite` and no envelopes or `key_chain`; no other type carries `invite`. Only `invite` carries `expires`. `join` carries `subject` equal to `author`, and `invite_proof`; `invite` and `uninvite` carry no `subject`. `invite_proof` appears on `add` and `join` only. `create` may carry one envelope, `K_0` for its author, and no other; a genesis without one stays valid. The entry is canonically encoded (§4.1).
9. `invite` opens a link invite: its key has never been used in this list before, and `ts < expires ≤ ts + 7 days`.
10. `uninvite` closes an open invite.
11. `join` admits its author: they are not a member, the cited invite is open, `ts ≤ expires`, and `invite_proof` verifies under the invite key (§6.1). The invite closes.
12. An entry that changes the epoch (`remove`, `rotate`) closes every open invite.

The server also checks what only it can: the author is the session's own identity, `author_agree`/`subject_agree` are the keys registered for those accounts, `ts` is at most 5 minutes ahead of its clock, an `invite`'s `expires` is in the future, and a `join` arrives before its invite's `expires`.

## 4.3 Server role

- The server **mirrors** the log; its `memberships` and `group_invites` tables are derived from it, never the other way round. Each membership is bound to the identity key the log admitted.
- It verifies signatures and rules 1–12 before accepting an entry.
- The server's validator and a client's are kept identical by the fixtures in `protocol/log-fixtures`, which both test suites replay.
- Appending is compare-and-swap on `prev`: two of the owner's devices acting at once get one `409`, which refetches and retries.

## 4.4 Fork and rollback detection

- Each client persists the highest `(seq, hash)` it has verified per list and warns hard if the server later shows a lower seq or a different hash at a known seq.
- Every content update carries the author's log head (§7). An update citing a head the client can't place relative to its own triggers a warning. A head it can't place is one that is not the hash of an entry in its verified log. The client's log may just be behind, so on such a head (or on content under an epoch or by an author its log doesn't know yet) it first catches up the log and tries again; only a head still unknown after that raises the warning, shown as a fork warning once per list. The content is still applied if its author passes the receiver's check (§7) in the log the client has.
- **Withheld removal.** A live update whose `epoch` is lower than the receiver's current epoch triggers a warning ("a member hasn't received the latest membership change") and a log refetch. Clients also refetch every list's head on reconnect and at least hourly.

This doesn't prevent a fork; it makes one visible to the members it splits.

---

# 5. Keys

```text
Identity X25519 (per user)
   │  HPKE envelope, inside a signed log entry
   ▼
List key K_e (per list, per epoch)
   └── HKDF(K_e, info = "runer/v1/list") → document key
                                            encrypts the list document: name, blocks, snapshots
```

- **Envelopes:** HPKE (RFC 9180) base mode, `DHKEM(X25519, HKDF-SHA256) / HKDF-SHA256 / AES-256-GCM`, `info = "runer/v1/env" ‖ 0x00 ‖ list_id(32 raw bytes) ‖ uint64be(epoch) ‖ recipient(32)`, where `recipient` is the member's `identity_sign_pub` (the envelope's `recipient` field). AAD is empty and the plaintext is `K_e`. `hpke_ciphertext = enc(32) ‖ ciphertext ‖ tag` (80 bytes). They live inside signed entries, so the author's signature authenticates them. The key the envelope is encrypted to is the member's `*_agree` key **as recorded in the verified log**. In a `create` entry, `list_id` in the info is 32 zero bytes: the list id is the hash of that entry, and the envelope is bound to the list by being part of it.
- **Epochs:** a new random 32-byte `K_{e+1}` on every removal or leave (via the next `rotate`). `key_chain` lets anyone holding `K_{e+1}` decrypt `K_e`, back to `K_0`, so a new member needs one envelope to read the whole history.
- **Key separation:** `K_e` is only ever HKDF input. `K_chain(e) = HKDF(K_e, info = "runer/v1/chain")`, used once with an all-zero nonce and empty AD: `key_chain = ciphertext ‖ tag` (48 bytes). The document key is `HKDF(K_e, info = "runer/v1/list")`; there is one document per list and `K_e` is already per list, so no salt is needed.
- **List names are inside the encrypted document.** The server sees only opaque ids.

## 5.1 The list document

Each list has exactly one encrypted Yjs document: its name and a flat, ordered sequence of tasks and notes. The server stores and relays it without knowing its shape, which is the client's ([app.md §3.1](../../docs/app.md#31-the-list-document)). Due dates, reminders and assignees exist only in this document.

---

# 6. Inviting someone

The attack to stop: Alice asks the server for Bob's key and gets the server's own. The fix: Alice and Bob share a secret over a channel the server doesn't control, the invite link, and use it to authenticate each other's keys.

There are three ways onto a list:

| | Owner online when the invitee joins? | Owner confirms? | Use |
|---|---|---|---|
| **Link invite** (§6.1) | No | No; notified afterwards | Default |
| **Approve-first invite** (§6.2) | Yes | Yes, optionally with the safety code | Higher assurance; no UI in the app |
| **Direct add** (§6.3) | — | — | Someone the owner already shares a list with |

**Common to both invite kinds.** The owner picks `s`, 32 random bytes, and derives (HKDF, §10.1) `id` (16 bytes, the server sees its base64url), `enc` and `mac` (32 bytes each) with labels `runer/v1/invite-id`, `runer/v1/invite-enc` and `runer/v1/invite-mac`. The link is `https://<server>/join#<base64url(s)>`: an ordinary `https://` URL on the list's server, so it also tells the app which server to use, and the fragment never reaches the server. The server's `/join` page hands the fragment to the app's `runer://` deep link. The server stores `{id, package, expiry ≤ 7 days}` and serves the package to anyone who asks with `id`.

`package = nonce(12) ‖ AES-256-GCM(enc, P, AD = "runer/v1/invite-package" ‖ 0x00 ‖ id)`, where `P` is canonical JSON (§4.1 rules), keys in this order:

```text
{"v":1,"list":<hex list id>,"log_head":<b64url>,"owner":<b64url sign pub>,"owner_agree":<b64url agree pub>,
 "name":<string>,"owner_name":<string>,"epoch":<int>,"list_key":<b64url K_e>}
```

`owner_name`, `epoch` and `list_key` are present in a link invite and omitted in an approve-first one. `owner_name` is the owner's account name when the link was made, so the invitee sees who invited them before they can read the list; it is left out when empty. Like the rest of the package it is sealed under `s`, so the server never sees it, and it is display text only: nothing is verified against it. The invitee verifies the list's log from genesis (the list id is its hash), pinning the owner's keys from the package, before trusting anything else in it.

## 6.1 Link invite (default)

The link carries everything needed to join, so the owner's device need not be online.

```text
Alice (owner)                              Server                              Bob
s, id, enc, mac as above
invite key pair = Ed25519 from seed HKDF(s, "runer/v1/invite-sign")
append signed `invite` {invite: invite_pub, expires}  ──►  mirror: invite open
package P with owner_name, epoch e, list_key K_e,
  log_head = hash of the `invite` entry               ──►  store {id, package, invite_pub}
Link ── sent over Signal / mail / in person ──────────────────────────────────────►
                                                             derive id, enc, invite key
                                             ◄── fetch package(id), decrypt
                                             ◄── GET /invites/{id}/log: verify from genesis,
                                                 find the open `invite` entry for invite_pub
                                             ◄── POST /invites/{id}/accept: signed `join`
                                                 {subject = Bob, subject_agree, invite,
                                                  invite_proof = Ed25519(invite key, join message)}
                     check rules 1–12, append,
                     close the invite, stop serving the package
Alice's app, when next online: `group` frame → "Bob joined via link"
```

Exact bytes:

- **Invite key:** `seed = HKDF(s, info = "runer/v1/invite-sign")`, 32 bytes, used as an Ed25519 private key seed; `invite_pub` is its public key. Every holder of `s` derives the same pair.
- **`invite` entry:** `invite = base64url(invite_pub)`, `expires` in Unix milliseconds with `ts < expires ≤ ts + 7 days` (rule 9). The owner appends it before storing the package; the package's `expires_at` must not be later than `expires`, or the server refuses it.
- **Join message:** `"runer/v1/join" ‖ 0x00 ‖ list_id(32 raw bytes) ‖ identity_sign_pub(32) ‖ identity_agree_pub(32)` of the joiner. `invite_proof` is the 64-byte Ed25519 signature over it by the invite key. Every member can check it, because `invite_pub` is in the owner-signed `invite` entry.
- **`join` entry:** `author = subject =` the joiner's `identity_sign_pub`, `subject_agree`, `invite`, `invite_proof`, epoch unchanged, no envelope: the joiner already has `K_e` from the package, and `key_chain` gives them the history.
- On `409` (the head moved) the joiner refetches the invite log and retries; if the invite closed meanwhile, the server answers `410 invite_not_usable`.

**The joiner's checks.** Before building a `join`, the joiner verifies the log from genesis and requires all of:

1. the package's `owner` and `owner_agree` equal the log's owner keys;
2. the package's `log_head` is the hash of an entry in the verified log;
3. the invite for `invite_pub` is open and unexpired in that log;
4. the package's `epoch` is the log's current epoch;
5. for an epoch above 0, the package's `list_key` opens that epoch's `key_chain`.

Nothing in the log commits to `K_0`, so at epoch 0 check 5 has nothing to open: the package's AEAD under `s` is the only check on the key.

| Server tries to… | Fails because |
|---|---|
| Join the list itself | The proof needs the invite key, derived from `s` |
| Swap the joiner's keys | The proof signs them |
| Replay a join into another list or with another invite | The proof binds the list id; the entry names the invite key, and the owner signed that |
| Admit a second person with the same invite | Rule 11 closes the invite on first use, and every member checks it |
| Hand the joiner a fake list | The package is AEAD under `s`; the log is verified from genesis |

**Residual risk: the link is a key.** Whoever opens it first joins and can read the list's history under `K_e`. The mitigations: a short expiry (the app defaults to 48 hours), single use, the owner is notified of every join and can remove the joiner with rotation (§8.1), and the optional **safety code** (below) lets both sides check afterwards.

**Revoking.** The owner appends `uninvite`; the server then stops serving the package. `DELETE /invites/{id}` alone only stops serving the package; the log invite stays open for anyone who already fetched it. Because an unused link still carries `K_e`, the owner's app appends a `rotate` the next time it is online after an `uninvite`, or after an unused invite expires. Any epoch change voids every open invite (rule 12); the app offers to send a new link.

## 6.2 Approve-first invite

The owner approves each join, so their device must be online after the invitee opens the link.

```text
Alice (owner)                     Server                         Bob
package P without epoch/list_key
          ── store {id, package, expiry ≤ 7d, single use} ──►
keep s in the list document, so Alice's other devices can approve
Link ── sent by Alice over Signal / mail / in person ──────────────────►
                                                          derive id, enc, mac
                                               ◄── fetch package(id), decrypt,
                                                   verify the list log from genesis
                                               ◄── join request {id, AEAD(enc, display name),
                                                                 HMAC(mac, id || Bob pubs)}
verify HMAC ⇒ Bob's keys are exactly what the link holder sent
"Bob wants to join · Safety code 48213 90127 … [Approve] [Reject]"
append signed `add Bob` + envelope(K_e), invite_proof = the MAC
          ── log entry ───────────────────────────────────►  verify (Alice's key pinned
                                                              from the package), open envelope
```

| Server tries to… | Fails because |
|---|---|
| Swap Bob's key in the join request | HMAC needs `s` |
| Swap the package (fake list, fake Alice) | AEAD needs `s` |
| Add its own member | Entries need the owner's signature |
| Hand Bob a fake log | Bob verifies it from the genesis hash in the package |
| Replay the join request | The invite is consumed by the `add` that cites it, in the same transaction |

Exact bytes (vectors in `protocol/vectors/invite.json`):

- The display name in the join request is `nonce(12) ‖ AES-256-GCM(enc, UTF-8 name, AD = "runer/v1/invite-join" ‖ 0x00 ‖ id)`. Its AD differs from the package's so one ciphertext can't pass for the other.
- The MAC (later the `add` entry's `invite_proof`) is `HMAC-SHA-256(mac, id ‖ joiner identity_sign_pub ‖ joiner identity_agree_pub)`, all raw bytes.

Residual risk: **whoever opens the link first can ask to join**, but the owner decides, and "invite already used" is a visible alarm.

## 6.3 Adding someone you already share a list with

No link is needed. The owner already holds the other person's keys from another list's verified log, so the owner's app appends `add` with an envelope of `K_e` to the `*_agree` key pinned there, and no `invite_proof`. The server only checks that the keys are that account's registered ones. The new member sees the list at once. This is the common case for couples: only the first shared list needs a link.

## 6.4 Safety code

A 30-digit fingerprint of one identity: `h = SHA-256("runer/v1/safety" ‖ 0x00 ‖ identity_sign_pub ‖ identity_agree_pub)`; each of the first six 5-byte chunks of `h`, read as `uint40be mod 100000`, gives a group of five digits. There is one code per identity, not a combined one: only the joiner's keys need checking (the owner's arrive in the package, authenticated by `s`). For a link invite it is an optional check after joining, shown on both sides' member lists; for an approve-first invite the owner's dialog shows it before approving.

---

# 7. Content encryption

Each batch of document updates is sent as:

```text
header = {type: "update", list, epoch, author: identity_sign_pub, log_head}
         // snapshots: {type: "snapshot", list, epoch, covers_seq, author, log_head}
nonce  = 12 random bytes
ct     = AES-256-GCM(document_key(epoch), nonce, update, AD = header)
sig    = Ed25519(author, header || nonce || ct)
```

`ct` is `ciphertext ‖ tag`. `list` is the list id in hex; `author` and `log_head` are base64url, and `log_head` is the hash of the newest log entry the author had verified (the value the next entry's `prev` would take). It is always present; `covers_seq` appears on snapshots only.

`header` is canonically encoded (§4.1 rules, keys in the order shown) and travels as base64url of those exact bytes, so the server stores and relays the signed bytes unchanged. Because `type` and `covers_seq` are signed, the server can't pass a snapshot off as an update or misstate what it covers.

**Receiver:** checks the author was a member at `epoch`, verifies `sig`, decrypts, merges. A member at `epoch` is anyone who was a member at any point while that epoch was current, by the receiver's verified log: someone added mid-epoch counts; a `remove` starts a new epoch, so the removed member is in none from then on; someone who left stays in their last epoch's set until the next `rotate` (they held that key anyway, and the server refuses their writes). Catch-up may carry older epochs (history). A *live* update with an older epoch is applied but triggers the withheld-removal warning (§4.4).

**Server:** checks the session is `author`, `author` is a current member of `list`, and `epoch` is current; otherwise `forbidden` or `stale-epoch` ([Sync §5](./sync-protocol.md)). On `stale-epoch` the client re-encrypts its outbox under the new epoch and resends; updates are idempotent.

**Why sign as well as encrypt:** AEAD proves "some member wrote this"; the signature proves which one.

**Snapshots** are a cache, not an authority. Any member periodically uploads the encrypted full state, signed, covering `seq ≤ N`. A client always merges a snapshot with what it already holds, and the server keeps the underlying updates for the retention window, so a bad snapshot can't erase content.

---

# 8. Removing someone

## 8.1 The owner removes Bob

1. The owner's client generates `K_{e+1}`.
2. It appends `remove Bob`, `epoch e+1`, envelopes for every remaining member, `key_chain = AES-GCM(K_chain(e+1), K_e)`.
3. The server accepts it, **closes Bob's sockets, denies him all reads and writes on the list**, and rejects writes tagged with an older epoch.
4. Remaining members open their envelopes and write under `K_{e+1}` from then on.

Bob keeps whatever he already decrypted; the UI says so plainly. He never receives `K_{e+1}`, so even a colluding server can only give him ciphertext he can't read.

## 8.2 Bob leaves

Bob appends `leave`; he must not create the next key, because he'd know it. The next remaining member to come online appends `rotate`. The server has already cut Bob off, so only a colluding server could leak anything, and only until then.

## 8.3 Limits

Removal protects data encrypted under an epoch the removed identity never received. Data written between the removal and every writer switching keys is exposed to a colluding server in three cases: after a `leave` until someone rotates; members who haven't yet seen the `remove`; and a server hiding the removal from some members (detectable when histories meet, §4.4). The server pushes a `group` frame whenever a log grows so honest clients switch quickly. Writes are not blocked while a rotation is pending.

## 8.4 Deleting an account

Deleting an account deletes every list it owns, at once and for all members: the owner can't leave (rule 6) and there is no ownership transfer, so a list doesn't outlive its owner. To the other members this is an ordinary list deletion.

A list the account is only a member of is different. The server can't take an account out of it: membership is what the signed log says, and the server holds no key that could sign for a member. So the server refuses to delete an account that is still a member of a list it doesn't own, and the client appends a `leave` to each such list first. To the other members that is an ordinary leave (§8.2): the next remaining member to come online rotates the key. Nothing new appears in any log.

---

# 9. Devices, loss and recovery

## 9.1 Scenarios

| Situation | What the user does | Result |
|---|---|---|
| New device | Type the Account Key | Full access. |
| Lost the paper key, still logged in | Settings → Show account key | Nothing lost |
| Lost a device, or it was stolen, or the key leaked | Change account key (§9.2) | Its session ends and it can't log in again. Other devices sign in with the new key. |
| Lost the Account Key **and** every device | New account; list owners re-invite it | Shared lists restored; personal lists lost |

## 9.2 Change account key

On a logged-in device: generate a new Account Key, re-encrypt the Identity Bundle under the new wrap key, `POST /account/rekey` signed with the old auth key. The server swaps atomically and deletes all other sessions. The identity is unchanged.

## 9.3 No identity reset

The identity key is fixed at signup. Replacing it would end every membership bound to the old key, costing the user their Inbox and any list they own, so the only response to a stolen device or leaked key is changing the account key (§9.2) and revoking sessions. That locks the thief out of the server, but not out of identity keys they already extracted (§12).

---

# 10. Primitives and libraries

No custom primitives.

| Purpose | Choice |
|---|---|
| KDF | HKDF-SHA-256 |
| AEAD | AES-256-GCM, 96-bit random nonce |
| Signatures | Ed25519 (Go `crypto/ed25519` on the server) |
| Key wrap | HPKE RFC 9180 (X25519 / HKDF-SHA256 / AES-256-GCM) |
| Hash | SHA-256 |
| Key storage | OS keychain via Tauri |

**Signature edge cases.** Client and server must accept exactly the same signatures, and Ed25519 libraries differ on unusual inputs. Conformance means the verdicts of Go's `ed25519.Verify`: cofactorless, accepting small-order keys and non-canonical key encodings, rejecting a non-canonical `R` or `S`. `protocol/vectors/ed25519-edge.json` pins 31 such cases, replayed by both test suites; in `ed25519-dalek` this is plain `verify`, not `verify_strict`. Separately, the server refuses to register a non-canonical or small-order `auth_pub` or `identity_sign_pub`, since anyone can make a signature that verifies under a small-order key.

Any code that reproduces the vectors (`protocol/vectors`) byte for byte is conformant. The official client's implementation is described in [app.md §8](../../docs/app.md#8-the-vault-and-the-crypto).

## 10.1 Byte-level conventions

Covered by the vectors in `protocol/vectors/*.json` (made from fixed seeds, checked in Go by `internal/cryptovectors` and `internal/sync`). The files in this repository are the source of truth:

- **Labels** all start with `runer/v1/`. The prefix is fixed: changing it changes every derived key and signed message.
- **HKDF:** SHA-256, empty salt, `info` = the label bytes alone, 32-byte output (the invite id takes 16).
- **Other labelled inputs** (HPKE info, safety code, invite ADs, the join message): `label ‖ 0x00 ‖ fields`, with fixed-width raw fields (keys, hashes, list ids as 32 raw bytes) and integers as `uint64be`. Only a variable-length field gets a length prefix: the login URL's `uint16be` (§3.4).
- **AES-256-GCM** values with a random nonce are stored as `nonce(12) ‖ ciphertext ‖ tag`; where the nonce travels separately (§7) or is fixed (`key_chain`), just `ciphertext ‖ tag`.
- **Vectors** use lowercase hex for all bytes.

---

# 11. Server enforcement summary

- Session auth by Account Key challenge (§3.4); expiry; `Origin` check on mutations and the WebSocket upgrade.
- Log verification and compare-and-swap append (§4.3).
- Membership check on every read, subscribe and write; current-epoch check on writes.
- Socket close and access denial on removal.
- The Inbox: created with the account; not deletable; no `add` or `invite` on it.
- Invites: owner-only, single-use, ≤ 7-day expiry, revocable, rate-limited joins; a link invite's package stops being served once its log invite closes.
- Rate limits, size limits and proof-of-work ([Self-hosting](../../docs/self-hosting.md#limits-at-a-glance)).

The schema is `internal/store/migrations/` (`0001_schema.sql` plus later changes). Everything stored as `BYTEA` except the signed log is opaque ciphertext.

---

# 12. Accepted limitations

| Not included | Consequence | Matters for |
|---|---|---|
| Per-device keys | A stolen unlocked device equals permanent identity compromise | Revoking one device without re-invites |
| App lock (biometric before keychain release) | Local keys protected only by OS login | Keeping the account key unreadable on an unlocked device |
| Key transparency / gossip of log heads | Forks detected only when split members' updates meet | High-risk users |

**Why not MLS?** MLS gives per-message forward secrecy and deliberately hides history from new members. Shared lists need the opposite: new members must read everything. With small groups, an epoch rotation is a handful of HPKE wraps; signed logs with epoch keys give the same removal guarantee with far less machinery.
