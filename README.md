# runer

A private list app: tasks and notes in lists you can share, end-to-end encrypted ([runer.app](https://runer.app)). The server stores and relays ciphertext. It never sees list names, content or the keys that open them, and holds no name or email.

| Directory | What it is |
|---|---|
| [`app/`](app) | The client: one Tauri 2 app for desktop and iOS. TypeScript core, Svelte UI, Rust crates for keys, crypto and membership logs |
| [`server/`](server) | The server: Go and PostgreSQL, shipped as the Docker image `ghcr.io/runer-app/runer-api` |
| [`protocol/`](protocol) | The contract between them: the [E2EE](protocol/docs/e2ee-protocol.md) and [Sync](protocol/docs/sync-protocol.md) protocols, the [API reference](protocol/docs/api-reference.md), and the conformance data both sides test against (`vectors/`, `log-fixtures/`) |

The protocol is implemented twice, in Go and in Rust, and the two must agree byte for byte. Both test suites read the same files in `protocol/`, so a protocol change lands in one pull request with both sides.

## Documentation

| Document | What it covers |
|---|---|
| [protocol/docs/](protocol/docs) | The rules both sides implement: [E2EE](protocol/docs/e2ee-protocol.md), [Sync](protocol/docs/sync-protocol.md), [API reference](protocol/docs/api-reference.md) |
| [docs/app.md](docs/app.md) | How the app is built: layers, the vault, sync, screens |
| [server/README.md](server/README.md) | How the server is built and developed |
| [docs/self-hosting.md](docs/self-hosting.md) | Running your own server: Docker, configuration, limits, retention, backups |
| [docs/releasing.md](docs/releasing.md) | Versions, tags, and the one-time release setup |

## Commands

```sh
make            # list the targets; app/ and server/ have their own Makefiles with more
make test       # both sides' full suites (the server's end-to-end tests need Docker)
make up         # the server and PostgreSQL in local Docker on :8080
make dev-local  # the desktop app against that local server
```

Each side's README has its setup: [app](app/README.md) · [server](server/README.md).

## Releases

App and server are versioned and released separately, with tags `app-vX.Y.Z` and `server-vX.Y.Z` made by `make bump-app` and `make bump-server`. See [docs/releasing.md](docs/releasing.md).

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md). Report security problems privately, as [SECURITY.md](SECURITY.md) describes.

## License

Copyright (C) 2026 Morten Sieker Andreasen.

This program is free software: you can redistribute it and/or modify it under the terms of the [GNU Affero General Public License](LICENSE), version 3, as published by the Free Software Foundation. It comes with no warranty.

In short: if you run a modified version as a network service, you must offer its users the source of your version.
