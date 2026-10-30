# Self-hosting

Run your own server. Quickest is the released Docker image with a PostgreSQL database; to run it from source, see [Running from source](#running-from-source).

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

Rate limits and login challenges are kept **in process memory**. That's correct for a single instance. Running several instances behind a load balancer would need shared state (e.g. Redis) first.

## Configuration

All configuration comes from environment variables.

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `DATABASE_URL` | yes | | PostgreSQL connection string |
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
| Deleted lists (with log, memberships, invites, updates and snapshots) | 30 days after deletion |
| Deleted accounts | 30 days, **if** the account is no longer in any group and authored no updates or invites. Otherwise the row stays, hidden, so other members can still verify who wrote what. |
| Ended sessions | 90 days after they ended |

Each run logs one `retention purge complete` line with counts.

## Limits at a glance

| Area | Limit |
|---|---|
| Login challenges | 120 per minute per IP (configurable), valid 60 s |
| Sign-up | 20 at once then 100 per hour per IP, + 20-bit proof-of-work (all configurable) |
| Sessions | 30 days idle, 180 days absolute |
| Invite package reads | 30 per minute per IP (configurable) |
| Join requests and link-invite accepts | 10 per invite (burst), then 10 a day |
| Invite lifetime | 7 days |
| Log entry `ts` | at most 5 minutes in the future |
| WebSocket | 10 connections per account, 500 lists each, 50 writes/s, 512 KiB update, 16 MiB snapshot |

## Backups

Everything worth keeping is in PostgreSQL. Back up the database, and test a restore. A leaked database dump exposes metadata, never content ([E2EE §1](../protocol/docs/e2ee-protocol.md)).

## Running from source

You need Go (the version in `server/go.mod`) and PostgreSQL (any recent version; the migrations use `gen_random_uuid()`). From `server/`:

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
