# Security policy

## Reporting a vulnerability

Please report security problems privately, not in a public issue. Use **Report a vulnerability** under this repository's **Security** tab (GitHub private vulnerability reporting).

Include what you found, how to reproduce it, and what an attacker gains. You'll get an answer as soon as possible, and credit in the fix if you want it.

## What counts

The guarantees this project makes are in [E2EE §1](protocol/docs/e2ee-protocol.md). Anything that breaks one of them is in scope, for example:

- the server, or someone with its database, reading content or keys;
- joining a list without the owner's signature or a valid invite;
- forging another member's edits or membership entries;
- a removed member reading content written under a later key epoch;
- bypassing session checks, the `Origin` check, or rate limits and proof-of-work;
- a log entry or signed header that the Go validator accepts but the documented rules reject (or the reverse), since clients must reach the same verdict;
- the app accepting a log entry, signature or key the protocol rejects, or reaching a different verdict than the Go validator;
- the app leaking the Account Key or list keys: out of the Rust vault (`app/crates/runer-vault`), the OS keychain, or across the webview boundary into the UI.

The limits the protocol accepts on purpose are listed in E2EE §8.3 and §12; they aren't vulnerabilities, but better ideas are welcome as ordinary issues.
