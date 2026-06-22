---
title: Deployment
nav_order: 9
---

# Deployment and operations

This page is for operators putting verix-dbm into production: how the image is built, how to run it behind a TLS-terminating reverse proxy with OIDC, how to scale it horizontally, and how to back it up, upgrade it, and probe its health.

verix-dbm ships as a single static Go binary with the React workbench baked in (`go:embed`). One process serves both the JSON API and the SPA, so a deployment is one container (or one binary) plus whatever metadata storage and session backend you choose.

## Packaging: the Docker image

The repo root `Dockerfile` is a three-stage build that produces a tiny distroless, nonroot image containing only the binary.

| Stage | Base image | What it does |
|-------|-----------|--------------|
| `spa` | `node:20-alpine` | Builds the React/Vite SPA into `dist/` so it can be embedded. `dist/` is gitignored, so it is produced in-image, never copied from the host. |
| `build` | `golang:1.26-alpine` | Static pure-Go compile of `./cmd/server`, and stages an empty `/data`. |
| runtime | `gcr.io/distroless/static-debian12:nonroot` | Final image: just the binary plus an owned `/data`. |

Key details:

- The `spa` stage copies `internal/web/spa/package.json` and `package-lock.json` first and runs `npm ci` (so the dependency layer caches), then copies the rest and runs `npm run build`.
- The `build` stage copies the SPA output to `./internal/web/spa/dist` so `go:embed all:spa/dist` resolves, then builds with:

  ```
  CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /verix-dbm ./cmd/server
  ```

  `CGO_ENABLED=0` yields a fully static binary (pure-Go `modernc.org/sqlite`, no cgo). `-trimpath` strips build paths; `-ldflags="-s -w"` strips the symbol table and DWARF for a smaller binary.
- `RUN mkdir -p /data` in the build stage creates an empty directory that the runtime image copies with `--chown=nonroot:nonroot`. Distroless has no shell or `chown`, so ownership is set at copy time; a freshly created named volume mounted at `/data` inherits this ownership, letting the app create the SQLite DB plus its WAL/SHM files.

Runtime image properties:

| Property | Value |
|----------|-------|
| Volume | `VOLUME ["/data"]` (metadata store location) |
| Port | `EXPOSE 8080` |
| User | `USER nonroot:nonroot` (distroless nonroot, uid `65532`) |
| Baked-in env | `DBM_ADDR=:8080`, `DBM_SQLITE_PATH=/data/verix-dbm.db` |
| Entrypoint | `ENTRYPOINT ["/verix-dbm"]` (no default args) |

Because the image is distroless there is no shell, `curl`, or `wget`, so a container-level `HEALTHCHECK` is not possible. The `/healthz` and `/readyz` endpoints exist and must be probed externally (for example by your reverse proxy or orchestrator). See [Health checks](#health-checks).

For local non-Docker builds, the [Makefile](https://github.com/KlevjanPrifti/verix-dbm/blob/main/Makefile) `build` target builds the SPA then runs `CGO_ENABLED=0 go build -o bin/verix-dbm` (note: it does not pass `-trimpath`/`-ldflags`, unlike the Dockerfile and CI). The build context for both compose files below is the repo root (`context: ..`) so the root `Dockerfile` and the Go/SPA sources are in scope.

## Demo topology (dev-only)

`docker/docker-compose-demo.yml` is a one-command quickstart. It is DEV MODE and must not be deployed as-is.

```bash
docker compose -f docker/docker-compose-demo.yml up --build
# then open http://localhost:8080
```

It brings up verix-dbm next to a Postgres and a Redis on one private bridge network (`verix`). The point of the topology is what is **not** published:

| Service | Published ports | Reachability |
|---------|-----------------|--------------|
| `dbm` | `8080:8080` | the only port reachable from the host |
| `postgres` | none | closed to host and internet; reachable only as `postgres:5432` on the internal network |
| `redis` | none | closed to host and internet; reachable only as `redis:6379` on the internal network |

The databases never expose a port. verix-dbm reaches them over the internal Docker network by service name, and you reach the databases only through the governed, audited workbench UI. The `dbm` service waits for both DBs via `depends_on ... condition: service_healthy`.

Why this is dev-only:

- `DBM_DEV_MODE: "true"` disables OIDC entirely and auto-logs every request in as a local admin. Dev mode short-circuits all production validation (no real auth, and it implies `DBM_ALLOW_LOCAL_TARGETS`).
- `DBM_BASE_URL: http://localhost:8080` is plain HTTP, so session cookies are not `Secure` and HSTS is not sent.
- `DBM_ENC_KEY` is a hardcoded demo key (`00112233...`). Generate your own with `openssl rand -hex 32` for anything real.
- `DBM_LOG_FORMAT: text` is set for readability rather than SIEM ingestion.

Demo connection details (created in the UI): Postgres host `postgres` port `5432` db `demo` user `demo` password `demo`; Redis host `redis` port `6379` user `default`.

## Production with Dokploy and Traefik (TLS)

`docker/docker-compose-dokploy.yml` is the reference production topology: a single `dbm` service behind Traefik (TLS) with OIDC, no bundled database. verix-dbm connects to the targets you register, including a shared Postgres/Valkey, by joining an external Docker network.

```yaml
services:
  dbm:
    build:
      context: ..
      dockerfile: Dockerfile
    environment:
      DBM_ADDR: ":8080"
      DBM_BASE_URL: ${DBM_BASE_URL:?public https URL, e.g. https://dbm.example.com}
      DBM_SQLITE_PATH: /data/verix-dbm.db
      DBM_ENC_KEY: ${DBM_ENC_KEY:?DBM_ENC_KEY must be set (64 hex chars)}
      OIDC_ISSUER: ${OIDC_ISSUER}
      OIDC_CLIENT_ID: ${OIDC_CLIENT_ID}
      OIDC_CLIENT_SECRET: ${OIDC_CLIENT_SECRET}
      OIDC_REDIRECT_URL: ${OIDC_REDIRECT_URL:-}
      OIDC_ADMIN_ROLE: ${OIDC_ADMIN_ROLE:-dbm-admin}
      OIDC_WRITE_ROLE: ${OIDC_WRITE_ROLE:-dbm-write}
    volumes:
      - dbm_data:/data
    expose:
      - "8080"
    networks: [default, shared]
    restart: unless-stopped
```

Notes on the service definition:

- `expose: ["8080"]` (not `ports:`) means the port is not published to the host. Only Traefik and other services on the network reach it. In Dokploy you map your domain to this service on port 8080 and let Traefik terminate TLS.
- The `${VAR:?message}` form makes Compose refuse to start if `DBM_BASE_URL` or `DBM_ENC_KEY` is unset. The `${VAR:-default}` form supplies the role-name defaults.
- There is no container healthcheck (distroless has no shell/wget); Traefik probes the HTTP route and `restart: unless-stopped` covers crashes. `/healthz` is available for the proxy to probe.

### External shared network

```yaml
networks:
  default:
  shared:
    external: true
    name: dokploy-network
```

The `shared` network is `external: true` and points at the network your shared Postgres/Valkey instances live on. The Dokploy default is `dokploy-network`; verify the name matches your setup. Joining it lets verix-dbm resolve those databases by their internal hostnames when you register them as connections.

### OIDC client and redirect URI

Configure a Keycloak (or other OIDC) client and set:

| Env var | Purpose |
|---------|---------|
| `OIDC_ISSUER` | Issuer URL, e.g. `https://keycloak.example.com/realms/yourrealm`. Required in production; the app refuses to start without it (unless `DBM_DEV_MODE=true`). |
| `OIDC_CLIENT_ID` | Client id. Also required in production. |
| `OIDC_CLIENT_SECRET` | Client secret. Needed for the auth code flow, though not validated at boot by config loading. |
| `OIDC_REDIRECT_URL` | Optional; defaults to `${DBM_BASE_URL}/auth/callback` when empty. |
| `OIDC_ADMIN_ROLE` / `OIDC_WRITE_ROLE` / `OIDC_READ_ROLE` | Realm roles that map to admin / write / read (defaults `dbm-admin` / `dbm-write` / `dbm-read`). |

The client's redirect URI in Keycloak must be `${DBM_BASE_URL}/auth/callback`. The login flow is authorization-code with PKCE; realm roles map to admin / write / read with deny-by-default (a user with none of the roles gets HTTP 403). See [Security](security.md) for the full flow and RBAC model.

Production also requires an encryption key: set `DBM_ENC_KEY` (64 hex chars) or `DBM_ENC_KEYS`. The app refuses to start without one rather than mint an ephemeral key that would be unreadable after a restart and would diverge across replicas. See [Configuration](configuration.md) for the full required-in-production set.

## Reverse proxy and TLS

verix-dbm is meant to run behind a TLS-terminating reverse proxy (Traefik, nginx, a cloud LB). A few behaviors depend on how you wire that proxy.

- **`DBM_BASE_URL` drives Secure cookies and HSTS.** The session and OIDC-state cookies get the `Secure` attribute only when `DBM_BASE_URL` starts with `https`. Likewise, the `Strict-Transport-Security` header (`max-age=63072000; includeSubDomains`) is sent only when `DBM_BASE_URL` is `https`. Always set `DBM_BASE_URL` to your public `https://...` URL in production, even though TLS is terminated at the proxy, so cookies are marked Secure and HSTS is emitted.
- **`DBM_TRUST_PROXY` controls forwarded-header trust.** Off by default (anti-spoof, fail-closed): the app uses the direct peer address as the client IP and ignores `X-Forwarded-For` / `X-Real-IP`. Set `DBM_TRUST_PROXY=true` only when verix-dbm sits behind a proxy that overwrites those headers. When enabled, chi's `RealIP` middleware is added and the client IP used for rate limiting and request logs is taken from the forwarded headers. Enabling it behind a proxy that does not strip client-supplied forwarded headers would let clients spoof their address.
- **Security headers** are set on every response: `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, a strict CSP with `frame-ancestors 'none'`, plus HSTS over HTTPS. The CSP `form-action` is `'self'` plus the OIDC issuer origin so the RP-initiated logout redirect to the IdP is not blocked. Do not strip these at the proxy. See [Security](security.md).
- The proxy should forward to the container's port `8080` (or whatever you set `DBM_ADDR` to). Point your TLS route at the service and let Traefik probe `/healthz`.

## High availability

Run two or more identical replicas behind a load balancer. Two subsystems must be made shared for replicas to be interchangeable; both are pluggable and opt-in by env.

### Metadata store: Postgres

The metadata store holds saved connections, per-connection grants, and the audit log only (never target data). By default it is single-node SQLite at `/data`. For HA, point all replicas at one Postgres:

| Env var | Value |
|---------|-------|
| `DBM_STORE_DRIVER` | `postgres` |
| `DBM_STORE_DSN` | e.g. `postgres://user:pass@host:5432/dbmmeta?sslmode=verify-full` |

When `DBM_STORE_DRIVER=postgres`, the app skips the `/data` directory creation and uses the shared Postgres for metadata. The same SQL is written once and rebound to `$N` placeholders for Postgres; the Postgres metadata pool uses up to 10 connections so multiple replicas can write concurrently. This buys you a single source of truth for connections, grants, and audit across every replica.

### Sessions: Redis

Sessions are pluggable and default to in-memory (`memory`), which is lost on restart and not shared. For HA use Redis so any replica can serve any session:

| Env var | Value |
|---------|-------|
| `DBM_SESSION_BACKEND` | `redis` |
| `DBM_SESSION_REDIS_URL` | e.g. `redis://[:pass@]host:port/db` |

Sessions are stored as JSON under the `dbm:sess:` key prefix in a shared keyspace. The Redis backend pings at startup with a 5s timeout, so a misconfigured URL fails the process at boot rather than at first login.

### What you get

Pair Redis sessions with a Postgres store and N identical replicas. Each piece buys:

- **Postgres store**: shared connections/grants/audit; no per-node metadata drift; audit log is complete across replicas.
- **Redis sessions**: a logged-in user keeps their session if the load balancer routes them to a different replica or a replica restarts; no forced re-login on deploy or node loss.

With both, the replicas survive restarts and node loss with no session loss. The encryption key must be identical on every replica (use `DBM_ENC_KEY` / `DBM_ENC_KEYS`), or one replica cannot decrypt credentials another wrote.

### Per-target pool size

Each replica maintains its own lazy, idle-closing pool per registered database target. `DBM_PG_POOL_MAX_CONNS` (default `4`) caps that per-target pool. Despite the Postgres-flavored name, this value sizes the Postgres, MySQL, and SQLite target pools alike. Pools are reclaimed after 5 minutes idle. With N replicas, total connections a target can see is roughly `N * DBM_PG_POOL_MAX_CONNS` per engine, so size the value with your replica count and the target's `max_connections` in mind. This is separate from the metadata store pool (the Postgres metadata pool is fixed at 10).

The reference `docker/docker-compose-dokploy.yml` does not set the HA variables; add `DBM_STORE_DRIVER`, `DBM_STORE_DSN`, `DBM_SESSION_BACKEND`, and `DBM_SESSION_REDIS_URL` to the service environment and scale the `dbm` service to run HA.

## Persistence and backups

What needs persisting depends on the metadata backend.

- **Default (SQLite store).** The metadata DB is a file at `DBM_SQLITE_PATH` (default `/data/verix-dbm.db`), on the `/data` volume. SQLite runs in WAL mode, so the DB is accompanied by `-wal` and `-shm` files. Back up the whole `/data` volume, ideally while the process is quiesced or using a consistent snapshot, so the WAL is captured with the main file. The file contains saved connections (with AES-GCM-encrypted passwords), per-connection grants, and the audit log; it never contains target database data.
- **Postgres store.** When `DBM_STORE_DRIVER=postgres`, the `/data` volume is not used for metadata and there is nothing SQLite to back up. Back up the metadata Postgres database with your normal Postgres backup process.

Either way, back up your encryption key material (`DBM_ENC_KEY` / `DBM_ENC_KEYS`) out of band: without it, saved connection passwords in the store cannot be decrypted. For zero-downtime key rotation, set `DBM_ENC_KEYS="v2:new,v1:old"`, trigger re-encryption (admin UI or `POST /api/admin/reencrypt`), then drop the old key. See [Security](security.md).

Optional audit retention: set `DBM_AUDIT_RETENTION_DAYS` to purge audit rows older than N days (a background job runs once at startup and then daily). Unset or `0` keeps the audit log forever.

## Upgrades

verix-dbm carries the SPA and schema migrations inside the binary, so an upgrade is a binary/image swap.

- Pull the new image (or build a new tag) and restart the container. The store runs idempotent `CREATE TABLE IF NOT EXISTS` migrations on open.
- **Single node (SQLite store):** restarting drops in-memory sessions, so users re-login unless you are using the Redis session backend.
- **HA (Postgres store + Redis sessions):** do a rolling restart of replicas. Shared store and sessions mean a replica can be replaced without metadata or session loss. The graceful shutdown gives in-flight requests up to 5 seconds to finish (the server is given a 5s shutdown timeout on SIGTERM/Interrupt) before the process exits.
- Keep the encryption key and OIDC config identical across the upgrade; changing the key without re-encrypting first makes existing saved passwords undecryptable.

## Health checks

Two unauthenticated probe endpoints, excluded from request logging and metrics:

| Method | Path | Behavior |
|--------|------|----------|
| GET | `/healthz` | Liveness. Always returns `200` with body `ok`. Signals only that the process is up; no dependency checks. |
| GET | `/readyz` | Readiness. Pings the metadata store with a 2s timeout. `200` `{"status":"ok"}` on success; `503` `{"status":"unavailable"}` if the store is unreachable. The raw store error is logged server-side (WARN, `readyz_store_unavailable`), not returned to the caller. |

Use `/healthz` for liveness (restart on failure) and `/readyz` for load-balancer readiness / pre-traffic gating, so a replica that cannot reach its metadata store is pulled from rotation. OIDC reachability is validated once at startup, not re-probed by `/readyz`.

A third infra endpoint, `GET /metrics`, exposes Prometheus metrics and is open by default; gate it with `DBM_METRICS_TOKEN` if the metrics port is publicly reachable. See [Observability](observability.md).

## See also

- [Configuration](configuration.md): full environment-variable reference and defaults.
- [Security](security.md) and [SECURITY.md](https://github.com/KlevjanPrifti/verix-dbm/blob/main/SECURITY.md): OIDC, RBAC, per-connection grants, CSRF, credential encryption, headers, SSRF egress guard.
- [Observability](observability.md): structured logging, Prometheus metrics, the `/metrics` token gate, and audit mirroring.
- Repo root: [README.md](https://github.com/KlevjanPrifti/verix-dbm/blob/main/README.md), [.env.example](https://github.com/KlevjanPrifti/verix-dbm/blob/main/.env.example), [Makefile](https://github.com/KlevjanPrifti/verix-dbm/blob/main/Makefile).
