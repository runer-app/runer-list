# Sync Protocol

**Related:** [E2EE Protocol](./e2ee-protocol.md) · [API Reference](./api-reference.md) (HTTP endpoints)

> The WebSocket protocol for list documents. Content is always ciphertext; this is about framing, ordering and catch-up. Code comments cite it as **Sync §n**.

---

# 1. Principles

- The server stores an **ordered log of encrypted updates per list** plus occasional encrypted snapshots. It never inspects them.
- Delivery is **at-least-once, in server `seq` order**. Updates are idempotent; the client also de-duplicates by `seq`.
- Frames are **JSON text**, binary fields **base64url**.
- One WebSocket per app instance, multiplexing all subscribed lists.
- The protocol version is in the URL (`/v1/…`).

---

# 2. Connection

```text
GET wss://host/v1/sync
Cookie: session=…
Origin: tauri://localhost   (the server's configured ORIGIN)
```

- `401` without a valid session, `403` on an `Origin` mismatch.
- The server pings every 30 s and closes a connection with no pong for 60 s.
- The session is re-checked on every frame and every ping; a connection whose session ended gets `bye` `session-revoked`.
- A client that stops reading is disconnected rather than slowing anyone else; it recovers through catch-up.
- Clients reconnect with exponential backoff (1 s, 2 s, 4 s … capped at 30 s, plus up to 1 s jitter), re-subscribe with their stored `since` per list, and resend their outbox (§4).

---

# 3. Messages

Every frame is `{ "t": "<type>", … }`. `cid` is a client-chosen id that correlates acknowledgements.

## Client → server

| `t` | Fields | Meaning |
|---|---|---|
| `sub` | `lists: [{list, since}]` | Subscribe; `since` is the highest `seq` the client holds (0 for none). |
| `unsub` | `lists: [{list}]` | Stop live delivery. |
| `update` | `cid, header, nonce, ct, sig` | Encrypted update ([E2EE §7](./e2ee-protocol.md)). `header` is base64url of the exact signed bytes of `{type: "update", list, epoch, author, log_head}`, canonically encoded. |
| `snapshot` | `cid, header, nonce, ct, sig` | Encrypted full state. Signed header `{type: "snapshot", list, epoch, covers_seq, author, log_head}`; covers every update with `seq ≤ covers_seq`, which must not exceed the list's head. |

## Server → client

| `t` | Fields | Meaning |
|---|---|---|
| `snapshot` | `list, epoch, covers_seq, msg` | Sent first in catch-up if one exists with `covers_seq > since`. |
| `update` | `list, seq, msg` | Catch-up or live. `msg` is `{header, nonce, ct, sig}` exactly as the author sent them. |
| `synced` | `list, head` | Catch-up for this list is complete; `head` is the highest `seq` sent. |
| `ack` | `cid, seq` | The client's update/snapshot was persisted. |
| `error` | `cid?, code, detail?` | §5. With `cid`, it applies to that frame only. |
| `group` | `group, head_seq` | A list was created, its membership log grew, or it was deleted. Sent live to members and at connect for every list. The client fetches the log over HTTP and re-verifies. |
| `bye` | `reason` | The server is closing. `session-revoked` (logout, rekey elsewhere, account deleted, expiry): log in again; a refused login means the device is signed out. `remove` / `leave`: reconnect and refetch lists. |

---

# 4. Catch-up and the outbox

**Catch-up** on `sub`, per list:

1. If a snapshot has `covers_seq > since`, send it, then every `update` with `seq > covers_seq`.
2. Otherwise send every `update` with `seq > since`.
3. Send `synced`.

Live updates arriving during catch-up are held and delivered after it, in order, without gaps. A snapshot is merged **with** local state, never instead of it.

**Outbox.** Every locally produced encrypted update is written to the local cache with its `cid` before sending, and removed on `ack`. On reconnect or after `stale-epoch`, unacknowledged items are resent, re-encrypted under the current epoch if needed. The editor never blocks on the network.

**Live delivery.** After `synced`, each new `update` is pushed in `seq` order to every subscriber except the author.

**Ordering per connection.** The server processes a connection's frames one at a time, in order, and every frame it sends on that connection (catch-up, live, `ack`, `error`) goes out in one ordered stream. So once `synced` for a list has arrived on a connection, and the list hasn't been unsubscribed since, an `ack {seq: X}` for an update to that list comes after every update to it with `seq < X`: other authors' updates are delivered first, and the client's own were acked earlier. Seqs are not contiguous, so a client can rely on this, not on gaps, to advance `since` to `X` on such an ack.

---

# 5. Error codes

| `code` | When | Client action |
|---|---|---|
| `stale-epoch` | `header.epoch` isn't the list's current epoch | Fetch the log, open the new envelope, re-encrypt the outbox, resend |
| `forbidden` | Not a member of the list (or it was deleted), or `header.author` isn't the session's identity | Unsubscribe; refresh lists |
| `too-large` | Frame over a limit, or too many subscriptions | Send smaller updates |
| `rate-limited` | Per-connection rate exceeded | Back off 1 s and retry |
| `bad-request` | Malformed frame, a header not in canonical form, `type` not matching the frame, or `covers_seq` beyond the head. Also sent when catch-up fails; resubscribe | Bug; log and drop |

---

# 6. Limits

| Limit | Default |
|---|---|
| `update` frame | 512 KiB |
| `snapshot` frame | 16 MiB |
| Writes per connection | 50/s |
| Subscribed lists per connection | 500 |
| Connections per account | 10 |

HTTP limits are in [Self-hosting](../../docs/self-hosting.md#limits-at-a-glance).

---

# 7. Snapshot policy

A client uploads a snapshot after it has synced and **either** 200 updates **or** 1 MiB of updates have accumulated since the last snapshot it knows of, after a random 0–30 s wait so clients don't all upload at once. The server keeps only the newest snapshot per `(list, epoch)`. After a removal, the first snapshot under the new epoch moves the list forward.

---

# 8. What the client does on startup

1. Load the Account Key and device cache key from the keychain; decrypt cached identity and list keys.
2. Open the WebSocket. On `group` frames, fetch and verify log growth and open new envelopes.
3. Subscribe to every list (lists are small, and the Today view needs them all).
4. Render from cache immediately; apply catch-up as it arrives.
5. Rebuild the search index and the Today view from decrypted state, and reschedule local reminders.
