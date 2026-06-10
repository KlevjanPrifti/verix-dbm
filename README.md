# verix-dbm

A low-footprint, self-hostable **web database manager** for **PostgreSQL** and
**Redis/Valkey**. It ships as a single static Go binary with a React workbench
UI baked in (`go:embed`), Keycloak OIDC login, and the SyncLink "HUD" theme.
The whole app backend, JSON API, and the compiled frontend is one
dependency-free distroless container.

---

## What it can do

### DataGrip-style workbench (React SPA, served at `/app`)
- **Database Explorer** a lazy-loaded tree of connections → schemas → tables →
  columns / keys / indexes, with right-click + kebab (`⋯`) context menus on every
  node.
- **Tabbed workspace** open as many tabs as you like, side by side:
  - **Grid** paginated, read-only table browse with `WHERE` / `ORDER BY`
    filters and per-column sort arrows.
  - **Console (Postgres)** run SQL with read/write modes, a 30s statement
    timeout, a 1000-row result cap, and a confirmation gate for destructive
    statements (`DROP`/`TRUNCATE`, unguarded `DELETE`/`UPDATE`).
  - **Doc** quick documentation view: columns, keys, indexes, and table
    comment.
  - **Usages** find-usages: inbound foreign keys that reference a table.
  - **Redis/Valkey** keyspace browser + command console (see below).
- **Toasts, modals, and context menus** for connection CRUD, DDL forms, and the
  audit log.

### PostgreSQL
- Schema/table tree with row estimates; paginated browse.
- SQL console (read/write) with statement timeout, row cap, and destructive-
  statement confirmation.
- **Code generators** (one click → clipboard): `CREATE TABLE` DDL, `SELECT`,
  `INSERT`, `UPDATE` skeletons.
- **Form-backed DDL**: add / modify column, rename table, create schema / table /
  index, drop table / column / index, truncate all admin/write-gated and
  audited.
- **CSV / JSON export** of a table's rows (honours the grid's `WHERE`/`ORDER BY`,
  same 1000-row cap a convenience snapshot, not a full dump).

### Redis / Valkey
- `SCAN`-based keyspace browser with `MATCH` (prefix-friendly).
- Type-aware value viewers (string / hash / list / set / zset) with TTLs.
- Command console with a read-only allowlist and `FLUSH*` confirmation.

### Auth, security & operations
- **Keycloak OIDC** login; realm roles map to **admin / write / read**, and
  access is **deny-by-default** (no role → 403). See [SECURITY.md](SECURITY.md).
- **DEV mode** (auto-login as a local admin) for local hacking opt-in only via
  `DBM_DEV_MODE=true`. In production the app **refuses to start** without OIDC
  rather than fall back to an open mode.
- Saved connection passwords **AES-256-GCM encrypted at rest** in SQLite (never
  sent to the browser); the **audit log redacts** role passwords.
- **CSRF** on every mutating request (form posts and the SPA's
  `X-CSRF-Token` header), including logout.
- **Security headers** (CSP with `frame-ancestors 'none'`, `nosniff`,
  `no-referrer`, HSTS over TLS) on every response.
- **Audit log** of all mutating actions, viewable by admins.
- **In-process rate limiting** on auth endpoints (brute-force / redirect-spam
  floor).
- Lazy, idle-closing connection pools.

---

## Technologies used

**Backend (Go 1.25)**
- [chi](https://github.com/go-chi/chi) HTTP router
- [pgx v5](https://github.com/jackc/pgx) PostgreSQL driver + introspection
- [go-redis v9](https://github.com/redis/go-redis) Redis/Valkey client
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) **pure-Go**
  SQLite (no cgo → fully static binary) for connection + audit metadata
- [go-oidc](https://github.com/coreos/go-oidc) + `golang.org/x/oauth2` Keycloak
  OIDC
- `crypto/aes` (GCM) credential encryption
- `html/template` legacy server-rendered pages (the React SPA is the primary UI)

**Frontend (`internal/web/spa`)**
- [React 18](https://react.dev) + [TypeScript 5](https://www.typescriptlang.org)
- [Vite 5](https://vite.dev) (base `/app/`, content-hashed assets) built to
  `dist/` and embedded via `go:embed`
- Custom HUD theme (`src/styles/hud.css`)

**Packaging / deploy**
- Multi-stage **Docker** build (Node → Go → distroless),
  `gcr.io/distroless/static-debian12:nonroot`
- Dokploy + Traefik (TLS) for production

---

## Run locally (DEV mode, no Keycloak)

```bash
# 1. Backend (auto-logged-in as Dev Admin). DEV mode is opt-in and must be set
#    explicitly without it (and without OIDC) the server refuses to start.
DBM_DEV_MODE=true go run ./cmd/server

# 2. Frontend (in another terminal) Vite dev server proxies /api + /c to :8080
cd internal/web/spa
npm install
npm run dev
```

- React workbench: the Vite dev URL it prints (proxies the API to `:8080`).
- Embedded/built UI + legacy pages: <http://localhost:8080> (the SPA is mounted
  at <http://localhost:8080/app> once built).

To run everything from the single binary, build the SPA first so it gets embedded:

```bash
npm --prefix internal/web/spa run build
go run ./cmd/server   # SPA now served at /app
```

Register a connection from the UI, e.g. a Postgres at `127.0.0.1:5432` db `seal`,
or a Valkey at `127.0.0.1:6379` (username `default`).

## Configuration

See [.env.example](.env.example). Key vars: `DBM_BASE_URL`, `DBM_ENC_KEY`
(64 hex chars `openssl rand -hex 32`), the `OIDC_*` set, and the role names
(`OIDC_ADMIN_ROLE` / `OIDC_WRITE_ROLE` / `OIDC_READ_ROLE`). For local development
without Keycloak set `DBM_DEV_MODE=true`. **Read [SECURITY.md](SECURITY.md)
before deploying** least-privilege DB roles and the deny-by-default model
matter for safe operation.

## Deploy (Dokploy)

1. Create a Keycloak client `verix-dbm`, redirect URI `${DBM_BASE_URL}/auth/callback`,
   and roles `dbm-admin` / `dbm-write`.
2. Set env (incl. `DBM_ENC_KEY`, `OIDC_*`) in Dokploy.
3. Deploy [docker/docker-compose-dokploy.yml](docker/docker-compose-dokploy.yml);
   map your domain to the `dbm` service port 8080 behind Traefik (TLS). It joins
   the external `dokploy-network` to reach the shared Postgres/Valkey by hostname
   (verify that network name matches your setup).

The [Dockerfile](Dockerfile) builds the SPA, compiles the static Go binary, and
ships only the binary + an empty `/data` (for SQLite) in a distroless nonroot
image.

## Layout

```
cmd/server          entrypoint
internal/config     env config
internal/crypto     AES-GCM credential encryption
internal/store      SQLite (connections, audit)
internal/conn       pooled connection registry (idle-close)
internal/postgres   pgx introspection / query / DDL / generators
internal/redisdb    go-redis scan / value / command
internal/auth       OIDC + sessions + RBAC + CSRF
internal/web        chi router, JSON API (api.go) + legacy html/template pages,
                    DDL / export / workbench handlers, rate limiter
internal/web/spa    React + TypeScript + Vite workbench (embedded via go:embed)
```

---

## Roadmap

### More features
- **Inline grid editing** edit / insert / delete rows directly in the data grid
  (today writes go through the SQL/command console or form-backed DDL).
- **Saved queries library** and query history per connection.
- **Per-connection ACLs** finer-grained access than the global admin/write/read
  roles.
- **Full table export** beyond the 1000-row snapshot cap (streamed dump, server-
  side pagination).
- **Schema diff / migration helpers** built on the existing DDL generators.
- **Redis editing** type-aware value editors (SET/HSET/LPUSH/…) with the same
  confirmation gating as Postgres.
- Self-host fonts (currently Google Fonts `@import`) for a strict CSP.
- Move sessions out of memory (currently lost on restart) to the shared
  Valkey, so they survive restarts and scale out.

### More database connections
- **MySQL / MariaDB**
- **MongoDB**
- **SQLite** (browse/edit local `.db` files)
- **ClickHouse** and other analytical stores

The connection layer (`internal/conn` registry + per-engine packages like
`internal/postgres` and `internal/redisdb`) is structured so a new engine is a
new package plus its API/UI tabs adding databases shouldn't require touching
the auth, crypto, or workbench shell.
