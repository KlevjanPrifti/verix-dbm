# verix-dbm

A low-footprint web database manager for **PostgreSQL** and **Redis/Valkey**.
Single static Go binary serving an HTMX UI (server-rendered, ~tens of MB browser
RAM), Keycloak OIDC login, and the SyncLink "HUD" theme.

## Features

- Register multiple Postgres and Redis/Valkey targets (passwords AES-256-GCM
  encrypted at rest in SQLite).
- **Postgres:** schema/table tree with row estimates, paginated table browse,
  SQL console with read/write, 30s statement timeout, 1000-row result cap,
  destructive-statement confirmation (DROP/TRUNCATE, unguarded DELETE/UPDATE).
- **Redis/Valkey:** `SCAN`-based keyspace browser with `MATCH` (prefix-friendly),
  type-aware value viewers (string/hash/list/set/zset), command console with a
  read-only allowlist and `FLUSH*` confirmation.
- **Auth/RBAC:** Keycloak OIDC; realm roles map to admin / write / read.
  Runs in DEV mode (auto-login local admin) when OIDC is unconfigured.
- **Audit log** of mutating actions, viewable by admins.
- CSRF on all mutating forms; lazy, idle-closing connection pools.

## Run locally (DEV mode, no Keycloak)

```bash
go run ./cmd/server
# open http://localhost:8080  (auto-logged-in as Dev Admin)
```

Register a connection from the dashboard, e.g. a Postgres at `127.0.0.1:5432`
db `seal`, or a Valkey at `127.0.0.1:6379` (username `default`).

## Configuration

See [.env.example](.env.example). Key vars: `DBM_BASE_URL`, `DBM_ENC_KEY`
(64 hex chars — `openssl rand -hex 32`), and the `OIDC_*` set.

## Deploy (Dokploy)

1. Create a Keycloak client `verix-dbm`, redirect URI `${DBM_BASE_URL}/auth/callback`,
   and roles `dbm-admin` / `dbm-write`.
2. Set env (incl. `DBM_ENC_KEY`, `OIDC_*`) in Dokploy.
3. Deploy [docker/docker-compose-dokploy.yml](docker/docker-compose-dokploy.yml);
   map your domain to the `dbm` service port 8080 behind Traefik (TLS). It joins
   the external `dokploy-network` to reach the shared Postgres/Valkey by hostname
   (verify that network name matches your setup).

## Layout

```
cmd/server        entrypoint
internal/config   env config
internal/crypto   AES-GCM credential encryption
internal/store    SQLite (connections, audit)
internal/conn     pooled connection registry (idle-close)
internal/postgres pgx introspection / query
internal/redisdb  go-redis scan / value / command
internal/auth     OIDC + sessions + RBAC + CSRF
internal/web      chi router, handlers, html/template + HTMX, HUD theme
```

## Notes / roadmap

- Inline row edit/insert UI (current writes go through the SQL/command console).
- Self-host fonts (currently Google Fonts `@import`) for a strict CSP.
- Sessions are in-memory (lost on restart) — fine for a single instance; move to
  the shared Valkey to survive restarts / scale out.
- Per-connection ACLs and a saved-queries library.
