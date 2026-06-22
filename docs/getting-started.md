---
title: Getting started
nav_order: 2
---

# Getting started

verix-dbm is a single static Go binary with a React workbench baked in: one process serves a JSON API and an embedded SPA so you can browse and query Postgres, MySQL/MariaDB, SQLite, Redis/Valkey, and MongoDB from one governed, audited UI. This page gets you from zero to a running instance, then points you at the docs you need before any real deployment.

## Prerequisites

| Tool | Version | Needed for |
|------|---------|------------|
| Go | 1.26 | Building or running from source. `go.mod` pins `go 1.26`; CI and the Docker build float to the latest 1.26.x patch. |
| Node.js | 20 | Building the embedded SPA (`npm ci` + Vite build) and running the Vite dev server. |
| npm | bundled with Node 20 | SPA dependency install (`internal/web/spa/package-lock.json`). |
| Docker + Docker Compose | optional | The fastest path (the demo compose stack). Not needed for the from-source or binary flows. |

Notes:

- The Go binary is fully static: it builds with `CGO_ENABLED=0` and uses pure-Go SQLite (`modernc.org/sqlite`), so there is no cgo toolchain to set up.
- The SPA must be built before `go build` / `go run`, because it is embedded with `go:embed all:spa/dist` in `internal/web/spa.go`. The binary refuses to boot (`log.Fatalf`) if `internal/web/spa/dist/index.html` is missing. The only exception is the Vite dev server flow below, which serves the SPA itself and proxies the API.

## The fastest path: Docker demo compose

This is the one-command quickstart. It runs verix-dbm in DEV mode (no Keycloak) next to a demo Postgres and a demo Redis on a private Docker network. From the repo root:

```bash
docker compose -f docker/docker-compose-demo.yml up --build
```

Then open:

```
http://localhost:8080
```

You are auto-logged-in as a local admin (Dev Admin), so there is no login screen.

### What is and is not exposed

This stack is deliberately built so the databases never expose a port:

| Service | Published port | Reachable from your host? |
|---------|----------------|---------------------------|
| `dbm` (verix-dbm) | `8080:8080` | Yes (this is the only one) |
| `postgres` | none | No (closed to host + internet) |
| `redis` | none | No (closed to host + internet) |

verix-dbm reaches the databases over the internal Docker bridge network by service name (`postgres:5432`, `redis:6379`). You reach the databases only through verix-dbm's UI. Both backing services have healthchecks and `dbm` waits for them via `depends_on: condition: service_healthy`.

### Add the demo connections

Once the UI is open, register the bundled databases (see [Registering your first connection](#registering-your-first-connection) for the full walkthrough):

Postgres:

| Field | Value |
|-------|-------|
| Kind | PostgreSQL |
| Host | `postgres` |
| Port | `5432` |
| Database | `demo` |
| Username | `demo` |
| Password | `demo` |

Redis:

| Field | Value |
|-------|-------|
| Kind | Redis / Valkey |
| Host | `redis` |
| Port | `6379` |
| Username | `default` |

Use the service names (`postgres`, `redis`) as the host, not `localhost`: verix-dbm dials them across the Docker network, and dev mode allows local/private targets so the egress guard does not block them.

### Why this is demo-only

The demo compose file is marked "DO NOT deploy as-is". It sets `DBM_DEV_MODE: "true"` (auth disabled, every request is a local admin) and ships a placeholder encryption key `DBM_ENC_KEY` (64 hex chars). For anything real, configure OIDC and TLS instead. See [docker/docker-compose-dokploy.yml](https://github.com/KlevjanPrifti/verix-dbm/blob/main/docker/docker-compose-dokploy.yml) and the deployment docs below.

## Run from source in DEV mode

This is the best loop for working on the code. It uses two terminals: the Go backend in dev mode, and the Vite dev server for instant SPA hot-reload. No Keycloak required.

Terminal 1: the backend.

```bash
DBM_DEV_MODE=true go run ./cmd/server
```

This starts the API on `:8080` and auto-logs every request in as a local admin (`Dev Admin`, all of read/write/admin). Dev mode also short-circuits the production fail-closed checks: it does not require OIDC, and an unset encryption key falls back to an ephemeral random key (saved-connection passwords become unreadable after restart, which is fine for a scratch session). Dev mode additionally relaxes the SSRF egress guard so local/private database targets are reachable.

Terminal 2: the SPA dev server.

```bash
cd internal/web/spa && npm install && npm run dev
```

Vite serves the SPA on its default port (`http://localhost:5173`) and proxies API calls to the backend. The proxy in `internal/web/spa/vite.config.ts` forwards two prefixes:

- `/api` -> `http://localhost:8080` (the JSON API)
- `/c` -> `http://localhost:8080` (the table export download at `/c/{id}/export`)

Open the Vite URL (printed in terminal 2) for live SPA editing. Edits to `internal/web/spa/src/**` hot-reload; backend changes need a restart of `go run` (or use a file watcher of your choice).

If you instead want the backend to serve the SPA itself (no Vite), build the SPA first so it embeds, then visit `http://localhost:8080` (the root `/` 302-redirects to `/app`). That is the binary flow below.

## Build and run the single static binary

For a production-like local run (or to produce a deployable artifact), build the SPA first so it gets embedded, then build the Go binary.

The simplest path uses the [Makefile](https://github.com/KlevjanPrifti/verix-dbm/blob/main/Makefile):

```bash
make build      # builds the SPA (npm ci + vite build), then the static Go binary -> bin/verix-dbm
DBM_DEV_MODE=true ./bin/verix-dbm   # then open http://localhost:8080
```

`make build` runs `npm --prefix internal/web/spa ci` + `run build` first (so `internal/web/spa/dist` exists for `go:embed`), then `CGO_ENABLED=0 go build -o bin/verix-dbm ./cmd/server`.

If you build by hand, the order matters: build the SPA before `go build`, or the embed of `spa/dist` is empty and the binary will not start.

```bash
npm --prefix internal/web/spa ci
npm --prefix internal/web/spa run build
CGO_ENABLED=0 go build -o bin/verix-dbm ./cmd/server
```

### Make targets

| Target | Command run | Purpose |
|--------|-------------|---------|
| `make build` | `spa` target, then `CGO_ENABLED=0 go build -o bin/verix-dbm ./cmd/server` | Build the embedded SPA then the static binary. Output: `bin/verix-dbm`. |
| `make spa` | `npm --prefix internal/web/spa ci` + `npm --prefix internal/web/spa run build` | Build only the embedded React SPA into `internal/web/spa/dist`. |
| `make run` | `go run ./cmd/server` | Run the backend. Needs the SPA already built for `/app`, or set `DBM_DEV_MODE=true`. |
| `make vet` | `go vet ./...` | Static analysis. |
| `make test` | `go test ./...` | Run the Go test suite. |
| `make vuln` | `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` | Dependency vulnerability scan. |
| `make tidy` | `go mod tidy` | Tidy `go.mod` / `go.sum`. |

Run a single Go test directly:

```bash
go test ./internal/web -run TestName -v
```

The official Docker image (multi-stage, distroless nonroot) does all of this for you: a Node 20 stage builds the SPA, a `golang:1.26-alpine` stage compiles the static binary with `-trimpath -ldflags="-s -w"`, and the final image is just the binary plus a nonroot-owned `/data` volume for the SQLite metadata store. See the [Dockerfile](https://github.com/KlevjanPrifti/verix-dbm/blob/main/Dockerfile).

## Registering your first connection

Connections are managed entirely from the UI. Creating, editing, and deleting connections is an admin-only action (in dev mode you are an admin).

1. In the Explorer on the left, click the `+` (New data source) button. It lists every supported database kind.
2. Pick a kind (PostgreSQL, MySQL, MariaDB, SQLite, MongoDB, Redis / Valkey, and Postgres-compatible variants like CockroachDB, Greenplum, Redshift, YugabyteDB, TimescaleDB, Aurora/RDS Postgres). The form pre-fills the default port for that kind (for example 5432 for Postgres, 3306 for MySQL, 6379 for Redis, 27017 for MongoDB).
3. Fill in the connection fields:
   - **Name**: a label for the source.
   - **Host** and **Port**: the database address. Tip: you can paste a full connection URL (for example `postgresql://user:pass@host:5432/db`) and the modal autofills the fields by scheme.
   - **Database**: database/schema name (for Redis this is the numeric logical DB; for SQLite this is a server-side file path, see below).
   - **Username** / **Password**: credentials. The password is encrypted at rest with AES-256-GCM and is never sent back to the browser.
   - **Options**: engine-specific options passed through to the driver (for example `sslmode=verify-full` for Postgres, `replicaSet=rs0&tls=true` for MongoDB).
   - **Read-only**: when set, the connection rejects every write at the API (HTTP 409).
4. Click **Test Connection** to verify reachability before saving. The probe runs server-side with a short timeout and returns ok/error inline.
5. Click **OK** to create the connection (the button is labeled **Save** when editing an existing one). The connection appears in the Explorer tree; expand it to browse schemas, tables, keys, and indexes (or the keyspace for Redis, databases/collections for MongoDB).

A few specifics worth knowing on first use:

- **SQLite** is opt-in and file-based: the connection's database field is a server filesystem path, and it must resolve under the `DBM_SQLITE_DIR` allowlist directory. SQLite is disabled entirely (fail-closed) when `DBM_SQLITE_DIR` is unset. The SQLite form hides host/port/user/password and shows a single "File path" field.
- **Egress guard**: outside dev mode, the server rejects connection targets that resolve to loopback, link-local, or cloud-metadata addresses (RFC1918 private ranges are allowed). Set `DBM_ALLOW_LOCAL_TARGETS=true` only if you must reach a loopback target.
- **Save as copy** (edit mode) clones a connection and reuses the stored password ciphertext, so the password never round-trips through the browser.
- **Per-connection grants** appear in the edit dialog only when scoped access is enabled (`DBM_SCOPED_ACCESS=true`) and you are an admin. See the security docs for the access model.

## Before you deploy for real

Dev mode is for local trials only. Before exposing verix-dbm to anyone else, read these:

- [Configuration](configuration.md): the full environment-variable reference and defaults (auth, encryption keys, store backend, sessions, observability, access scoping). In production the app fails closed and refuses to start without OIDC (`OIDC_ISSUER` + `OIDC_CLIENT_ID`) and an encryption key (`DBM_ENC_KEY` or `DBM_ENC_KEYS`).
- [Security](security.md) and the repo root [SECURITY.md](https://github.com/KlevjanPrifti/verix-dbm/blob/main/SECURITY.md): the OIDC login flow, deny-by-default RBAC (admin/write/read), per-connection grants, CSRF, credential encryption and rotation, security headers, rate limiting, and the SSRF egress guard.
- [Deployment](deployment.md): production topology, TLS, OIDC wiring, HA (shared Postgres metadata store + Redis sessions), and the [docker/docker-compose-dokploy.yml](https://github.com/KlevjanPrifti/verix-dbm/blob/main/docker/docker-compose-dokploy.yml) example behind a TLS-terminating proxy.

See also the annotated [.env.example](https://github.com/KlevjanPrifti/verix-dbm/blob/main/.env.example) and the project [README.md](https://github.com/KlevjanPrifti/verix-dbm/blob/main/README.md).
