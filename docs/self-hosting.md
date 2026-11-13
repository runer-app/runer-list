# Self-hosting

Run your own server. Quickest is the released Docker image with a PostgreSQL database; to run it from source, see [Running from source](#running-from-source).

> **Self-hosting is provided as-is.** The server is open source (AGPL-3.0) and you're welcome to run it, but there is no support for self-hosted instances. Bug reports about the code are welcome as issues; questions about your own setup may go unanswered.
>
> **Keep your server up to date.** The app updates itself, so a server that falls behind may stop working with newer app versions. Update the server first, or together with the app.

## What to know before you start

- **One instance or several.** Instances behind a load balancer share their state through PostgreSQL: no sticky sessions, no Redis. A few limits count per instance ([Shape of a deployment](#shape-of-a-deployment)).
- **PostgreSQL 13 or newer, reached directly or through a pooler in session mode.** A pooler in transaction mode (e.g. PgBouncer with `pool_mode = transaction`) can't carry `LISTEN`, which live updates need, even on one instance ([Shape of a deployment](#shape-of-a-deployment)).
- **You run the backups.** Everything worth keeping is in PostgreSQL ([Backups](#backups)).
- **You own the TLS and proxy setup**, including `TRUSTED_PROXIES` ([Configuration](#configuration)).

## Quick start with Docker

The container listens on port 8080 and needs a PostgreSQL database. `DATABASE_URL`, `ORIGIN` and `PUBLIC_URL` are required; behind a reverse proxy, set `TRUSTED_PROXIES` too ([Configuration](#configuration)).

```sh
docker run -p 8080:8080 \
  -e DATABASE_URL='postgres://user:pass@db:5432/runer' \
  -e ORIGIN='tauri://localhost' \
  -e PUBLIC_URL='https://api.example.com' \
  -e TRUSTED_PROXIES='10.0.1.0/24' \
  ghcr.io/runer-app/runer-api:latest
```

Point the platform's health check at `GET /healthz`.

## Shape of a deployment

One container, one PostgreSQL database. There's no Redis, no object storage and no worker process. Migrations are embedded in the binary and run on startup. The retention job runs inside the server process.

```text
client ──HTTPS/WSS──► reverse proxy (TLS) ──► server:8080 ──► PostgreSQL
```

**Several instances.** Run as many containers as you like against the same database, behind a load balancer that spreads both HTTP requests and WebSockets in any way (round robin is fine; sticky sessions are not needed). Give every instance the same configuration, in particular the same `PUBLIC_URL`: the load balancer's. Instances started together, as in a rolling deploy, take turns migrating.

```text
                                      ┌──► server:8080 ──┐
client ──HTTPS/WSS──► load balancer ──┼──► server:8080 ──┼──► PostgreSQL
                         (TLS)        └──► server:8080 ──┘
```

What the instances share lives in PostgreSQL: login and rekey challenges, the join limit per invite, and live updates, which reach the other instances through `LISTEN/NOTIFY`. So each instance keeps **one PostgreSQL connection open for listening**: `DATABASE_URL` must reach PostgreSQL directly or through a pooler in session mode, not one in transaction mode, which can't carry `LISTEN`. That holds for a single instance too. Through such a pooler the server starts, but no live update arrives: every edit waits 10 s for its acknowledgement and then the socket closes. If an instance loses that connection, it closes its WebSockets and refuses new ones (`503`) until it listens again; the app reconnects and catches up.

Some limits are **per instance**, so N instances allow N times as much: the per-IP limits on login challenges, sign-ups and invite package reads, and the WebSocket connections per account. The proof-of-work, not the counter, is what makes sign-up spam expensive. The join limit per invite is shared.

The retention job runs on every instance. Its deletes are idempotent, so this only repeats work.

**Upgrading to several instances.** A rolling upgrade from a version that ran single-instance only is safe: old and new instances can run side by side for the overlap, and no edit is lost. Edits made through an old instance reach clients connected to new ones at their next catch-up rather than live. Each instance holds one more database connection than before (the listener), so leave room under PostgreSQL's `max_connections`.

## Configuration

All configuration comes from environment variables.

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `DATABASE_URL` | yes | | PostgreSQL connection string: PostgreSQL 13 or newer, directly or through a session-mode pooler, never a transaction-mode one |
| `ORIGIN` | yes | | The single `Origin` accepted on mutations and the WebSocket upgrade, e.g. `tauri://localhost` for the desktop app |
| `PUBLIC_URL` | yes | | The canonical API URL, e.g. `https://api.runer.app`. Login signatures cover it, so it must be exactly what clients connect to (a trailing `/` is stripped). |
| `LISTEN_ADDR` | no | `:8080` | Bind address |
| `TRUSTED_PROXIES` | no | none | Comma-separated CIDRs whose `X-Forwarded-For` header is trusted. **Set it behind a reverse proxy** (e.g. Traefik's Docker network), or every user shares the proxy's IP and one rate-limit bucket. |
| `SIGNUP_POW_BITS` | no | `20` | Sign-up proof-of-work difficulty, 8–28. Each bit doubles the work; 20 is about a second of CPU. Clients read it from every challenge. |
| `SIGNUPS_PER_IP_PER_HOUR` | no | `100` | Sustained sign-ups per client IP |
| `SIGNUP_BURST_PER_IP` | no | `20` | Sign-ups one IP can make at once before the hourly rate applies |
| `CHALLENGES_PER_IP_PER_MINUTE` | no | `120` | Login challenges per client IP |
| `INVITE_READS_PER_IP_PER_MINUTE` | no | `30` | Unauthenticated invite package reads per client IP |

**Why the per-IP limits are loose.** Many people share one public IP: offices, schools, mobile carriers (carrier-grade NAT), VPNs and iCloud Private Relay. A tight per-IP cap locks out real users and barely slows an attacker who rotates IPs anyway. The per-IP caps are only flood guards; the **proof-of-work** is what makes every account cost something, whoever creates it. If sign-up abuse shows up, raise `SIGNUP_POW_BITS` first. Every sign-up logs one `signup` line and every refused one a `signup rate-limited` line, so a flood is visible as a spike. The server logs the limits it runs with at startup.

**About `TRUSTED_PROXIES`:** per-IP rate limits use the client IP. If the proxy isn't trusted, every user appears to come from the proxy's address and they all share one rate-limit bucket. If you trust too much, clients can spoof their IP.

**Check it after deploying.** The server doesn't log client IPs, so test the limit: from one address, send more than `CHALLENGES_PER_IP_PER_MINUTE` requests to `POST /v1/auth/challenge` until they get `429`, then send one from a different address. It should still be answered normally. If it also gets `429`, every client shares one bucket: check `TRUSTED_PROXIES`, then that the proxy itself sees real client IPs (behind Docker Swarm's ingress network it needs host-mode ports or the PROXY protocol).

## Docker image

- Build: `docker build .` in `server/`. It's a static binary on `distroless/static:nonroot`, with no shell and base images pinned by digest.
- Health check: `GET /healthz` (it pings the database).
- Released images: `ghcr.io/runer-app/runer-api:X.Y.Z`, `:X.Y` and `:latest` (see [Releasing](releasing.md)). Multi-arch: linux/amd64 and linux/arm64.

## Security headers

Every response gets `nosniff`, `X-Frame-Options: DENY`, `CSP: default-src 'none'`, `Referrer-Policy: no-referrer`, and HSTS when served over TLS. HTTP bodies are capped at 1 MiB.

Request logs record the method, the **route pattern** (never the raw URL, because invite ids are credentials), status and latency.

The logs never include client IPs. Rate limits hold them in memory only.

## Data retention

A purge job runs at startup and then every 24 hours:

| What | When it's removed |
|---|---|
| A list's content: its log, updates and snapshots | Kept while the list exists |
| Deleted lists (with log, memberships, invites, updates and snapshots) | 30 days after deletion |
| Deleted accounts | 30 days, **if** the account is no longer in any group and authored no updates or invites. Otherwise the row stays, hidden, so other members can still verify who wrote what. |
| Sessions | At once on logout, account key change or account deletion; expired ones at the next purge |

Each run logs one `retention purge complete` line with counts.

## Limits at a glance

| Area | Limit |
|---|---|
| Login challenges | 120 per minute per IP per instance (configurable), valid 60 s |
| Sign-up | 20 at once then 100 per hour per IP per instance, + 20-bit proof-of-work (all configurable) |
| Sessions | 30 days idle, 180 days absolute |
| Invite package reads | 30 per minute per IP per instance (configurable) |
| Join requests and link-invite accepts | 10 per invite (burst), then 10 a day, shared by all instances |
| Invite lifetime | 7 days |
| Log entry `ts` | at most 5 minutes in the future |

The WebSocket's limits are in [Sync §6](protocol/sync-protocol.md#6-limits).

## Backups

Everything worth keeping is in PostgreSQL. Back up the database, and test a restore. A leaked database dump exposes metadata, never content ([E2EE §1](protocol/e2ee-protocol.md)).

## Running from source

You need Go (the version in `server/go.mod`) and PostgreSQL 13 or newer (the migrations use the built-in `gen_random_uuid()`). From `server/`:

```sh
# 1. A database
docker run -d --name runer-pg -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=runer -p 5432:5432 postgres

# 2. The server (migrations run automatically on startup)
make run
```

`make run` uses these defaults. Override any of them on the command line, e.g. `make run ORIGIN=http://localhost:5173`:

| Variable | Default in `make run` |
|---|---|
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:5432/runer?sslmode=disable` |
| `ORIGIN` | `tauri://localhost` |
| `PUBLIC_URL` | `http://localhost:8080` |
| `LISTEN_ADDR` | `:8080` |

Check it's alive:

```sh
curl localhost:8080/healthz   # → ok
```

All settings are under [Configuration](#configuration).
