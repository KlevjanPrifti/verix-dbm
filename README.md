# verix-dbm

**Put your databases behind SSO and an audit log without exposing their ports.**

verix-dbm is a self-hosted web database manager for **PostgreSQL**,
**MySQL / MariaDB**, **SQLite**, **MongoDB**, and **Redis / Valkey**. Drop one container onto the same
private network as your database and reach it by hostname (`postgres:5432`) - the
database never has to publish a port to the host or the internet. The only thing
exposed is a workbench UI behind Keycloak OIDC login, role-based access, and a
full audit trail.

It ships as a single dependency-free distroless container: a static Go binary
with the React workbench baked in (`go:embed`). No agent, no sidecar, no external
service required to start.

## Why

The usual way to reach a production database is to publish its port and share the
password, or to stand up a VPN / SSH bastion that keeps no record of who ran what.
verix-dbm replaces that with a governed front door:

- **No exposed database.** The container talks to the DB over the internal Docker
  / Kubernetes network; the database keeps its port closed to everything else.
- **SSO, not shared passwords.** Login is Keycloak OIDC; access is deny-by-default,
  mapped to **admin / write / read** realm roles, and optionally scoped
  per-connection.
- **Every action is audited.** Queries, logins, credential use, and schema changes
  are logged, redacted of secrets, and exportable to your SIEM.
- **Least privilege by design.** Saved passwords are AES-256-GCM encrypted at rest
  and never sent to the browser; the database role you connect as stays the real
  security boundary (see [SECURITY.md](SECURITY.md)).

---

## What it can do

### IDE-style workbench (React SPA, served at `/app`)
- **Database Explorer** a lazy-loaded tree of connections → schemas → tables →
  columns / keys / indexes, with right-click + kebab (`⋯`) context menus on every
  node.
- **Tabbed workspace** open as many tabs as you like, side by side:
  - **Grid** paginated, read-only table browse with `WHERE` / `ORDER BY`
    filters and per-column sort arrows.
  - **Console (Postgres / MySQL / SQLite)** run SQL with read/write modes, a 30s
    statement timeout, a 1000-row result cap, and a confirmation gate for
    destructive statements (`DROP`/`TRUNCATE`, unguarded `DELETE`/`UPDATE`).
  - **Doc** quick documentation view: columns, keys, indexes, and table
    comment.
  - **Usages** find-usages: inbound foreign keys that reference a table.
  - **MongoDB** collection document browser + guarded command console (see below).
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

### MySQL / MariaDB
- Schema/table tree and paginated browse, same workbench as Postgres.
- SQL console (read/write) with statement timeout (`MAX_EXECUTION_TIME` hint on
  selects + context deadline), row cap, and destructive-statement confirmation.
- Engine-aware identifier/literal quoting and `MODIFY COLUMN`-style DDL forms
  (add / modify column, rename table, create/drop table / index).
- Non-admin users are blocked from `LOAD DATA INFILE`, `LOAD_FILE()`, and
  `INTO OUTFILE/DUMPFILE` as a backstop on top of the connection's DB role.

### SQLite
- Same workbench as Postgres/MySQL over an on-disk database file (pure-Go driver).
- Introspection from `sqlite_master` / PRAGMA functions; a single `main` schema.
- Read-only enforced per call with `PRAGMA query_only`; DDL is transactional.
- **Security**: a SQLite connection opens a server-side file, so it is gated by
  the `DBM_SQLITE_DIR` allowlist (paths must resolve under it; `..`/symlink
  escapes are rejected) and disabled entirely when the var is unset.

### MongoDB
- Explorer tree of databases → collections.
- Document browser with JSON filter / sort / projection and skip-based paging
  (30s timeout, 1000-document cap), plus per-collection index listing.
- Command console that runs a JSON command document, with a read-only allowlist
  and an admin + confirmation gate for destructive commands (`drop`,
  `dropDatabase`, ...).

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
- [go-sql-driver/mysql](https://github.com/go-sql-driver/mysql) MySQL/MariaDB
  driver (via `database/sql`)
- [go-redis v9](https://github.com/redis/go-redis) Redis/Valkey client
- [mongo-driver](https://github.com/mongodb/mongo-go-driver) MongoDB client
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) **pure-Go**
  SQLite (no cgo → fully static binary) for the SQLite engine + connection/audit metadata
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

## Quickstart (Docker)

Try the whole thing in one command. This brings up verix-dbm next to a Postgres
and a Redis on a private network, with **no database ports published** - only the
verix-dbm UI is reachable from your host:

```bash
docker compose -f docker/docker-compose-demo.yml up --build
# then open http://localhost:8080  (DEV mode: auto-logged-in as a local admin)
```

Add a connection from the UI to see the value prop work: a Postgres at host
`postgres` port `5432` (db/user/pass `demo`), or a Redis at host `redis` port
`6379` (user `default`). verix-dbm reaches them by hostname over the internal
network; the databases themselves expose nothing. See
[docker/docker-compose-demo.yml](docker/docker-compose-demo.yml) for the
annotated topology.

> DEV mode disables auth and is for local evaluation only. For a real deployment
> use OIDC + TLS - see [Deploy](#deploy-dokploy) and **[SECURITY.md](SECURITY.md)**.

## Run from source (DEV mode, no Keycloak)

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
internal/crypto     AES-GCM credential encryption (versioned, rotatable keys)
internal/store      metadata store: SQLite (default) or Postgres (HA)
internal/conn       pooled connection registry (idle-close)
internal/dbsql      engine-neutral SQL interface shared by postgres + mysql + sqlite
internal/postgres   pgx introspection / query / DDL / generators
internal/mysql      MySQL/MariaDB introspection / query / DDL
internal/sqlite     SQLite (file) introspection / query / DDL (pure-Go driver)
internal/redisdb    go-redis scan / value / command
internal/mongodb    MongoDB databases / collections / find / command console
internal/auth       OIDC + sessions (memory|redis) + RBAC + per-conn grants + CSRF
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
- **MongoDB**
- **SQLite** (browse/edit local `.db` files)
- **ClickHouse** and other analytical stores

The connection layer (`internal/conn` registry + per-engine packages like
`internal/postgres`, `internal/mysql`, and `internal/redisdb`, behind the
`internal/dbsql` interface) is structured so a new engine is a new package plus
its API/UI tabs adding databases shouldn't require touching the auth, crypto,
or workbench shell.

---

## License

verix-dbm is licensed under the **GNU Affero General Public License v3.0**
([LICENSE](LICENSE)). You are free to self-host, study, modify, and redistribute
it. If you run a modified version as a network service, the AGPL requires you to
offer that version's source to its users.

For a commercial license without the AGPL's network-copyleft obligation, contact
the maintainer.
