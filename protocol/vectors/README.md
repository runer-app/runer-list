# Crypto test vectors

Byte-exact examples of every construction in the E2EE protocol that isn't a membership log entry (those are in `../log-fixtures`). Each file starts with a `description` of the construction; E2EE §10.1 gives the shared conventions.

| File | Covers |
|---|---|
| `account.json` | Account Key → words, auth key, wrap key; login, rekey and identity messages (E2EE §3) |
| `identity-bundle.json` | Identity Bundle encryption (§3.3) |
| `list-keys.json` | Document key, key chain, HPKE envelopes (§5) |
| `content.json` | Update and snapshot headers, encryption and signature (§7) |
| `invite.json` | Invite id/enc/mac, package, join request, `invite_proof` (§6.2); under `link`: invite key, link package with the list key, join message and proof (§6.1) |
| `safety-code.json` | Safety code digits (§6.4) |
| `ed25519-edge.json` | Which unusual Ed25519 signatures verify (§10): each case's `valid` is the verdict of Go's `ed25519.Verify` |

All bytes are lowercase hex. Every input is derived from a fixed name, so the files don't depend on randomness.

**These files are the source of truth.** A client implementation is conformant when it reproduces them byte for byte; one that keeps its own copy must keep it identical to these.

- **Checked by** `go test ./internal/cryptovectors/ ./internal/sync/`: an independent Go implementation (standard library only) re-derives every value from the inputs in each file. CI runs it.
- **Changing or adding a vector:** edit the file here, with the E2EE doc if a construction changes. Any implementation may produce the bytes; they are accepted when the Go check passes. `ed25519-edge.json` needs no generator: its verdicts are whatever Go's `ed25519.Verify` returns, which the same test asserts.
