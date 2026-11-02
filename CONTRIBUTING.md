# Contributing

## Before you start

Open an issue first for anything larger than a small fix, so the approach is agreed before you write it. Security problems go through [SECURITY.md](SECURITY.md), not issues.

## Working on the code

Before opening a pull request, run the checks of the side you changed (`make test` at the root runs both):

```sh
make -C app test       # fmt, clippy, lint, types, unit and e2e tests
make -C server check   # format, vet, lint, vulnerability scan, unit + end-to-end tests (needs Docker)
```

CI runs the same, plus a short fuzz run of the server's log validator.

- **Tests come with the change.** Behaviour the server enforces gets an end-to-end test in `server/e2e/`, driven by `server/internal/testclient`. The app's e2e suite in `app/e2e/` runs against the real server; the app's unit tests use an in-memory server (`app/packages/client-core/test/platform-node/memory-server.ts`), which changes with the API.
- **Schema changes** are a new numbered file in `server/internal/store/migrations/`. Never edit a migration that a deployed database has already run.
- **Comments cite the documents** as `E2EE §n` and `Sync §n`. Keep the citation right when you move code or renumber a section.
- Setup: [app/README.md](app/README.md) · [server/README.md](server/README.md).

## Changing the protocol

The documents in `protocol/docs/` and the data in `protocol/` are the contract both sides are built against.

1. Change the document (`protocol/docs/e2ee-protocol.md` or `protocol/docs/sync-protocol.md`) and the code on both sides in the same pull request.
2. Membership log rules: regenerate the fixtures with `GENERATE_FIXTURES=1 go test ./internal/grouplog/` in `server/`. The app's `runer-vault` must reach the same verdict on every file.
3. Crypto constructions: regenerate `protocol/vectors/` with `VECTORS_WRITE=1 cargo test -p runer-crypto --test generate the_committed` in `app/`. A vector is accepted when the server's independent check passes too: `go test ./internal/cryptovectors/ ./internal/sync/` in `server/`.
4. Never edit those files by hand. Say in the pull request whether existing apps and servers keep working with the new version.
