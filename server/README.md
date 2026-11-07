# runer-api

The server for a private list app: tasks and notes in lists you can share, end-to-end encrypted. It is written in Go and stores everything in PostgreSQL.

What it stores and sees is in the [README](../README.md); what a malicious server can and cannot do is in [E2EE §1](../docs/protocol/e2ee-protocol.md).

## Quick start

You need Docker.

```sh
make up                        # PostgreSQL + the server on http://localhost:8080
curl localhost:8080/healthz    # → ok
```

To run the server from source against your own PostgreSQL, see [Self-hosting](../docs/self-hosting.md#running-from-source).

```sh
make test     # unit + end-to-end tests (end-to-end needs Docker)
make check    # format, vet, lint, vulnerability scan and tests: what CI gates on
```

## Documentation

The documents are listed in the [README](../README.md#documentation).

## Clients

The official client is open source too, in [`app/`](../app). Everything a compatible client needs is in this repository: the protocol documents, the conformance data above, and [`internal/testclient`](internal/testclient), a small Go client the end-to-end tests use to sign up, build log entries, invite and sync.

The client is part of the trusted base ([E2EE §1](../docs/protocol/e2ee-protocol.md)): the server code lets you check what the server does, and the client code what is done with your keys.

## Code layout

One Go binary and one PostgreSQL database. Rate limits and login challenges live in process memory, so a deployment is a single instance.

| Package (`internal/`) | What it does |
|---|---|
| `server` | Wires middleware, routes and error handling; `cmd/server` only starts it |
| `auth` | Challenge, login, logout |
| `account` | The account, its settings, the key bundle, rekey, delete |
| `grouplog` | The membership log: parsing, the validity rules (E2EE §4.2, the same verdicts as the app's vault), compare-and-swap append |
| `invite` | The invite flows (E2EE §6); stores opaque bytes, never the invite secret |
| `sync` | The WebSocket: subscriptions, ordered relay, catch-up |
| `store` | PostgreSQL access and embedded migrations |
| `limits`, `config`, `httpx`, `retention` | Rate limits and proof-of-work, config, HTTP helpers, the daily purge |
| `testclient` | The small Go client the end-to-end tests use |
| `cryptovectors` | Checks `protocol/vectors` with an independent Go implementation |

## Running it

The released image is `ghcr.io/runer-app/runer-api`. How to run it, its settings, limits and data retention: [Self-hosting](../docs/self-hosting.md).

## Development

You need Go (the version in `go.mod`), Docker for the end-to-end tests (they start their own Postgres through testcontainers), and optionally `golangci-lint` for `make lint`. Running a server: [Self-hosting](../docs/self-hosting.md).

### Things that trip people up

- **Every mutation needs the right `Origin` header.** A `POST`, `PUT`, `PATCH` or `DELETE` under `/v1` without exactly `Origin: <ORIGIN>` gets `403 origin_mismatch`. So does the WebSocket upgrade. With curl, add `-H "Origin: tauri://localhost"`.
- **The session cookie is `Secure`.** Browsers and most HTTP clients only send it over HTTPS (or to `localhost`).
- **Login needs real cryptography.** There's no username/password. You need an Ed25519 key pair to sign the challenge, and a proof-of-work for sign-up. The Go test client in `internal/testclient` is the easiest working example.

### Make targets

| Command | What it does |
|---|---|
| `make run` | Start the server |
| `make build` | Build `bin/server` |
| `make test` | Unit + end-to-end tests (e2e needs Docker) |
| `make test-unit` | Everything except `e2e/`, no Docker needed |
| `make test-e2e` | Real-server, real-Postgres scenarios |
| `make fuzz` | Fuzz the log parser/validator (`FUZZTIME=30s` by default) |
| `make fmt` / `vet` / `lint` / `vuln` | Formatting, vet, golangci-lint, govulncheck |
| `make check` | All of the above: what CI gates on |
| `make up` / `down` / `reset` | The compose stack: server + PostgreSQL in Docker on :8080 (`reset` deletes its data) |

### The tests, briefly

- **Unit tests** sit next to the code. The biggest suite is `internal/grouplog`, which runs the shared log fixtures in `../protocol/log-fixtures/` plus a fuzz test.
- **End-to-end tests** in `e2e/` start a real server and a real Postgres, then drive them with `internal/testclient`, a deliberately simple Go client that can sign challenges, build log entries and send WebSocket frames. One file per area: accounts, groups, invites, sync, removal.
- **Log fixtures** (`../protocol/log-fixtures/*.json`) are valid and invalid membership logs. They're the contract between this server and any client: both must accept and reject exactly the same ones.

## Security

Please report vulnerabilities privately; see [SECURITY.md](../SECURITY.md).

## Contributing

See [CONTRIBUTING.md](../CONTRIBUTING.md).

## License

Copyright (C) 2026 Morten Sieker Andreasen.

This program is free software: you can redistribute it and/or modify it under the terms of the [GNU Affero General Public License](../LICENSE), version 3, as published by the Free Software Foundation. It comes with no warranty.

In short: if you run a modified version as a network service, you must offer its users the source of your version.
