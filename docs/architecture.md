# Architecture

verix-dbm is a single static Go binary that serves a JSON API and an embedded React workbench, fronting many database engines through one pooled connection registry. This page explains how those parts fit together and how a request flows through them.

## The single-binary model

The whole product ships as one process: `cmd/server`. There is no separate API server, no separate web server, and no external asset host.

- One Go binary serves both the JSON API (`/api/*`) and the React single-page app (the workbench under `/app`).
- The SPA is built ahead of compilation (Vite, output `internal/web/spa/dist`) and baked into the binary with `go:embed all:spa/dist` in `internal/web/spa.go`. The Go build will not start the server without it: `spaHandler()` calls `log.Fatalf` at boot if `spa/dist` or `index.html` is missing (fail-closed). This is why `make build` runs the SPA build first and why `go build`/`go run` need the SPA present.
- The binary is fully static: `CGO_ENABLED=0` with pure-Go SQLite (`modernc.org/sqlite`), so it cross-compiles and runs without libc. The Dockerfile builds it into a `gcr.io/distroless/static-debian12:nonroot` image.

What that buys you: one artifact to ship, one port to expose (`DBM_ADDR`, default `:8080`), one router, and one auth/RBAC/CSRF/rate-limit middleware stack that protects the entire authenticated surface. Engines are pluggable packages inside the same binary, not separate services.

### Process bootstrap

`cmd/server/main.go` wires the object graph in a fixed order, failing closed at each step:

1. `config.Load()` reads env vars and validates them. In production it refuses to start without OIDC and without an encryption key (see [Configuration](configuration.md)).
2. `newLogger(cfg)` builds a `log/slog` logger (JSON by default, `text` when `DBM_LOG_FORMAT=text`; level from `DBM_LOG_LEVEL`, default `info`), written to stderr.
3. Data-dir mkdir for the SQLite metadata file (skipped when `DBM_STORE_DRIVER=postgres`).
4. `crypto.ParseKeyring(...)` builds the AES-256-GCM keyring (`*crypto.Box`).
5. `openStore(cfg)` opens the metadata store: Postgres when `DBM_STORE_DRIVER=postgres`, otherwise SQLite.
6. `conn.NewRegistry(box, cfg.PGPoolMaxConns, cfg.SQLiteDir)` creates the pooled connection registry and starts its idle-close janitor goroutine.
7. `auth.New(ctx, cfg)` builds the OIDC authenticator (validates issuer reachability at startup; skipped in dev mode).
8. `web.NewServer(...)` assembles the `*Server`.
9. Audit wiring: `st.OnAudit(srv.ObserveAudit)` mirrors every audit event to the structured log and the auth-outcome metric; `reg.OnCredentialAccess(...)` emits a `cred_access` audit event when a stored password is decrypted to open a pool.
10. Optional audit-retention goroutine when `DBM_AUDIT_RETENTION_DAYS > 0`.
11. `http.Server{Addr: cfg.Addr, Handler: srv.Router(), ReadHeaderTimeout: 10s}` starts listening.

Shutdown is graceful: on `SIGINT`/`SIGTERM` the server runs `httpSrv.Shutdown(ctx)` with a 5-second timeout, and `st.Close()` runs via a deferred close.

## Component diagram

```
                          Browser
                             |
            HTTPS (one origin, one port :8080)
                             |
                             v
   +-----------------------------------------------------------+
   |                    Go process (cmd/server)                |
   |                                                           |
   |   /app, /app/*  ->  embedded React SPA (go:embed)         |
   |        |                 ^                                |
   |        |   XHR/fetch     |  /api/me -> csrf token         |
   |        v                 |                                |
   |   chi router  ->  global + group middleware stack         |
   |   (RealIP?, RequestID, Recoverer, observe, headers,       |
   |    auth.Middleware, rate limit, CSRF per handler)         |
   |        |                                                  |
   |        v                                                  |
   |   JSON API handlers (internal/web/api_*.go)               |
   |        |                                                  |
   |        v                                                  |
   |   conn.Registry  (lazy shared pools, idle-close janitor)  |
   |        |                                                  |
   |   +----+----+----------+----------+----------+            |
   |   |    |    |          |          |          |            |
   |   v    v    v          v          v          v            |
   | postgres mysql sqlite redisdb  mongodb  (dbsql.Engine     |
   |  (pgx) (db)  (db)  (go-redis)(mongo-drv) for SQL family)  |
   +---|------|------|--------|----------|----------------------+
       |      |      |        |          |
       v      v      v        v          v
   Target  Target  file    Target     Target          <- the databases users connect to
   Postgres MySQL  (under  Redis      MongoDB
                   SQLITE_DIR)

   Side stores (not the connected databases):
   +---------------------------+    +----------------------------+
   | internal/store            |    | internal/auth/sessions     |
   | metadata: connections,    |    | sessions: memory (default) |
   | grants, audit log         |    | or redis (HA, shared)      |
   | SQLite or Postgres        |    +----------------------------+
   +---------------------------+
```

The metadata store and the session backend are deliberately drawn to the side: they hold verix-dbm's own state (saved connections, grants, audit log, login sessions), never the data in the databases users connect to.

## Package map

| Package | Responsibility |
|---|---|
| `cmd/server` | Entrypoint. Loads config, builds the object graph (logger, keyring, store, registry, auth, server), starts the HTTP server, handles graceful shutdown and audit retention. |
| `internal/config` | Env-var configuration. One `Config` type and `Load()`; fails closed in production (OIDC + encryption key required). |
| `internal/crypto` | AES-256-GCM credential encryption. Versioned, rotatable keyring (`Box`): `<keyID>$<base64(nonce\|\|ciphertext)>`, primary key for writes plus retained keys for decrypt-during-rotation. `Provider` seam for a future KMS. |
| `internal/store` | Metadata store only: saved connections, per-connection grants, audit log. Pluggable backend (SQLite default, Postgres for HA). Also holds the per-connection DSN builders and the SQLite path allowlist resolver. |
| `internal/conn` | `Registry` of lazy, idle-closing, shared connection pools keyed by connection ID. Decrypts passwords, opens/pings/caches pools, dispatches SQL ops via `Engine()`, exposes `Redis`/`Mongo` getters, `Forget`, and the `cred_access` callback. |
| `internal/dbsql` | Engine-neutral SQL interface (`Dialect` + `Engine`) shared by all SQL engines, plus family constants and kind->family mapping, the destructive-statement gate (`NeedsConfirm`), and shared DTOs. |
| `internal/postgres` | pgx v5 introspection, query, DDL, and code generators. `dbsql.Engine` implementation. |
| `internal/mysql` | go-sql-driver introspection, query, DDL for MySQL/MariaDB. `dbsql.Engine` implementation. |
| `internal/sqlite` | `modernc.org/sqlite` introspection, query, DDL over a server-side file. `dbsql.Engine` implementation. |
| `internal/redisdb` | go-redis SCAN browse, type-aware value viewers, command execution, and the dangerous-command gate (`NeedsConfirm`). Non-SQL vertical. The read-only command allowlist (`redisReadAllow`) lives in the web handler `internal/web/api_redis.go`. |
| `internal/mongodb` | mongo-driver databases/collections/find, command console with a read allowlist, dangerous-command gate, and a server-side-JS block for non-admins. Non-SQL vertical. |
| `internal/auth` | OIDC authorization-code-with-PKCE login, sessions (memory or redis), RBAC capabilities (admin/write/read, deny-by-default), per-connection grants, and CSRF. |
| `internal/web` | chi router, JSON API (`api_*.go`), shared helpers (`handlers_*.go`), security headers, SSRF egress guard, rate limiting, observability, SPA embedding. One flat package; handlers are methods on `*Server`. |
| `internal/web/spa` | React 18 + TypeScript 5 + Vite 5 workbench, embedded via `go:embed`. Explorer tree plus a tabbed workspace (grid, console, redis, mongo, doc, usages). |

## Middleware stack

`Server.Router()` (`internal/web/server.go`) builds one chi router. Global middleware runs on every request in this exact order:

1. `middleware.RealIP` - only when `DBM_TRUST_PROXY=true`. Without it, `X-Forwarded-For` is not trusted and the client IP is the direct peer (anti-spoofing, fail-closed default).
2. `middleware.RequestID` - assigns a request id used in logs.
3. `middleware.Recoverer` - turns a panic into a 500 instead of crashing the process.
4. `s.observe` - structured request log line (`http_request`) plus Prometheus metrics. Skips the infra paths `/healthz`, `/readyz`, `/metrics` so probes/scrapes do not drown the signal.
5. `securityHeaders(s.cfg)` - sets `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, the Content-Security-Policy (`frame-ancestors 'none'`, `script-src 'self'`, no `unsafe-eval`), and `Strict-Transport-Security` when `DBM_BASE_URL` is https.

After the global stack, routes split into three tiers:

- Unauthenticated operational routes registered directly: `GET /healthz` (static `ok`), `GET /readyz` (200 only if the metadata store pings within 2s, else 503), `GET /metrics` (optionally Bearer-gated by `DBM_METRICS_TOKEN`).
- Auth-endpoint group with its own limiter `newRateLimiter(20, time.Minute)` (20/min per client IP): `GET /auth/login`, `GET /auth/callback`. `POST /auth/logout` is registered outside that group (POST + CSRF only, never GET, so a cross-site page cannot force a logout).
- Authed group (`r.Group`) with two more middlewares in order:
  1. `s.auth.Middleware` - validates the OIDC session, requires the `read` capability (deny-by-default: a valid session with no role gets a 403), and injects the `User` into the request context. In dev mode it auto-logs-in a local admin; otherwise an unauthenticated request gets a 302 to `/auth/login`.
  2. `authedLimit.middlewareBy(sessionKey)` - `newRateLimiter(600, time.Minute)`, 600/min keyed per user email (or per IP when unauthenticated). Over limit returns 429.

  Inside this group: `GET /` (302 to `/app`), `GET /c/{id}/export` (CSV/JSON download, outside `/api` because it streams a file), the `/api` mount (`r.Route("/api", s.mountAPI)`), and the SPA at `/app` and `/app/*`.

CSRF is not a global middleware. It is enforced per handler on every mutating request via `s.auth.CheckCSRF(r)`, which compares the `X-CSRF-Token` header (or the `csrf` form field) against the session's token using a constant-time compare. The SPA fetches its token from `GET /api/me`.

For the full security model (OIDC flow, RBAC, CSRF, crypto, headers, SSRF guard, rate limiting), see [Security](security.md) and [../SECURITY.md](../SECURITY.md).

## The connection registry

`internal/conn/registry.go` is the single place that opens, caches, and reclaims connections to target databases. It exists so that handlers never deal with credentials, pool lifecycle, or driver wiring.

- Lazy, shared, cached pools. One `map[int64]*<engine>Entry` per engine family (`pg`, `mysql`, `sqlite`, `redis`, `mongo`), keyed by saved-connection ID. The first request for a connection decrypts its password, opens the pool, pings it, and caches it; later requests reuse the cached pool.
- Pool sizing. `pgMaxConns` comes from `DBM_PG_POOL_MAX_CONNS` (default 4; values `<= 0` fall back to 4). Despite the Postgres-flavored name, this same value caps the Postgres, MySQL, and SQLite pools (`MaxConns` for pgx, `SetMaxOpenConns` for the `database/sql` engines). Redis and Mongo use their drivers' internal pools.
- Idle-close janitor. A goroutine started in `NewRegistry` ticks every minute; any pool idle longer than `idleTTL = 5 * time.Minute` is closed and removed. Pool entries also carry `MaxConnIdleTime`/`SetConnMaxIdleTime = idleTTL`.
- `Forget(id)` drops and closes any cached pool/client for one connection across all five maps. It is called after a connection is updated, deleted, or re-encrypted, so the next request re-decrypts and re-dials with fresh state.
- Fail-on-connect, not fail-on-cache. If the ping fails, the freshly opened pool is closed and the error returned, so a broken connection is never cached.
- Never leak session state across acquires. Because pools are shared and connections are reused, per-request session state must be reset every call. This is the explicit reason Postgres resets `default_transaction_read_only` and `search_path` per call (a conn left read-only would otherwise fail a later write with SQLSTATE 25006), MySQL pins safety settings via the DSN at handshake and uses a read-only transaction rather than `SET SESSION`, and SQLite resets `PRAGMA query_only` on a fresh `context.Background()` so a cancelled request cannot leave a permanently read-only pooled conn.
- Credential access auditing. `password(c)` decrypts `PasswordEnc` via the keyring; on a successful decrypt it fires the `onCred` callback, which writes a `cred_access` audit row.

`Engine(ctx, c)` is the single dispatch seam for SQL operations: it switches on `c.Engine()` (`dbsql.FamilyMySQL` -> `mysql.New`, `dbsql.FamilySQLite` -> `sqlite.New`, default including Postgres -> `postgres.New`) and returns a `dbsql.Engine`. Redis and Mongo are not routed through `Engine`; handlers call `reg.Redis(...)` and `reg.Mongo(...)` directly.

## The engine plug-in model

Each database engine is its own package behind the shared registry, so adding one does not touch auth, crypto, or the workbench shell. There are two shapes.

SQL-family engines fit the `dbsql.Engine` interface (`internal/dbsql/dbsql.go`) and reuse the grid/console/doc/usages workbench tabs for free. Today these are PostgreSQL (pgx), MySQL/MariaDB (go-sql-driver), and SQLite (`modernc.org/sqlite`). Each has a compile-time assertion `var _ dbsql.Engine = (*Engine)(nil)`. The family is chosen per connection by `c.Engine()` (which maps the connection's `kind` to a family via `dbsql.Family`), not by the URL. Note the gotcha: the `/api/c/{id}/pg/*` route prefix is a legacy name that serves all three SQL families, not Postgres only.

Non-SQL verticals have their own data model and their own tabs, and do not go through `Engine()`. Today these are Redis/Valkey (go-redis) and MongoDB (mongo-driver), with endpoints under `/api/c/{id}/redis/*` and `/api/c/{id}/mongo/*`.

Shared guardrails live across the engines: a 30s statement timeout (implemented per engine), a 1000-row result cap, and a destructive-statement confirmation gate (`dbsql.NeedsConfirm` for SQL, `redisdb.NeedsConfirm`, `mongodb.NeedsConfirm`). Read-only enforcement and the server-side-exec/file-access screen are also per engine.

For the full step-by-step recipe to add an engine (the files to touch and in what order), the per-engine specifics, and the exact guardrail mechanisms, see [Database engines](database-engines.md).

## Request lifecycle: one query

This traces a SQL console query (`POST /api/c/{id}/pg/query`) from browser to response. Other endpoints follow the same spine.

1. Edge middleware. Optional `RealIP` (if `DBM_TRUST_PROXY=true`), then `RequestID`, `Recoverer`, `observe` (starts the latency timer), and `securityHeaders` (response headers).
2. Authentication. `s.auth.Middleware` validates the OIDC session, requires the `read` capability (else 403), and injects the `User` into the context. Dev mode auto-admins; an unauthenticated request would 302 to `/auth/login`.
3. RBAC and rate limit. The user's capabilities (`Admin`/`Write`/`Read`) are already resolved on the `User`. `authedLimit.middlewareBy(sessionKey)` enforces 600/min per user; over limit returns 429.
4. Routing and CSRF. chi matches the route to `apiQuery` in `internal/web/api_sql.go`. Because it is a mutating POST, the handler calls `s.auth.CheckCSRF(r)`; a missing or mismatched token returns 403.
5. Connection resolution and scoped-access check. `connFor(r)` loads the connection by id and requires `s.access(ctx, u, c).Read`. In scoped mode (`DBM_SCOPED_ACCESS=true`) a non-admin needs a per-connection grant on one of their groups/roles; a grant scopes *where* a user acts, never above their global capability. An inaccessible connection returns the same 404 as a missing one, so existence is never disclosed.
6. Read-only resolution. The effective `readOnly` flag is `c.ReadOnly || !s.access(ctx, u, c).Write`, computed before anything is run.
7. Guardrails before execution. `apiQuery` runs these checks before it acquires the engine. The server-side-exec/file-access screen (`serverSideBlocked`) blocks restricted primitives for non-admins. If the query is not read-only and `dbsql.NeedsConfirm(sql)` trips (DROP/TRUNCATE, unguarded DELETE/UPDATE) and the request did not set `confirm`, the handler returns `{needConfirm:true, sql}` instead of running it. (DDL/CRUD handlers that go through `apiSQL`/`apiRequireWrite` instead acquire the engine first and surface a connect failure as HTTP 502.)
8. Engine acquire and execute. The handler then obtains the engine via `reg.Engine(ctx, c)` (lazy open/ping). On this console path a connect failure is returned as HTTP 200 with an `{error}` field, not 502. `eng.Query(ctx, sql, readOnly, schema)` runs against a shared, idle-managed pool under the 30s statement timeout and the 1000-row cap; read-only is enforced per engine as described above.
9. Audit. The handler records an audit row (here `sql_query`) via `st.AddAudit`; sensitive values in the detail are redacted first. The `OnAudit` sink mirrors it to the structured log and, for auth events, the metric. The credential decrypt that opened the pool emits its own `cred_access` event.
10. Response. The handler writes JSON via `writeJSON`. Console-style endpoints return engine/SQL errors at HTTP 200 with an `{error}` field so the SPA can render them inline; DDL/CRUD endpoints use real HTTP error codes (400/403/404/409/502). On return, `observe` emits the `http_request` log line (method, matched route pattern, path, status, bytes, `duration_ms`, ip, `request_id`, and `user` if present) and records the Prometheus `verixdbm_http_*` metrics labeled by method and chi route pattern.

## Cross-links

- [Security](security.md) and [../SECURITY.md](../SECURITY.md) - OIDC, RBAC, per-connection grants, CSRF, credential encryption, headers, SSRF guard, rate limiting.
- [Database engines](database-engines.md) - the engine plug-in recipe, per-engine specifics, and the shared guardrails in detail.
- [Data model](data-model.md) - the metadata store schema (connections, grants, audit), DSN builders, and the SQLite path allowlist.
- [API reference](api-reference.md) - the full route table, request/response shapes, and capability gates.
- [Configuration](configuration.md) - every env var, default, and gated subsystem.
- Repo root: [../README.md](../README.md), [../.env.example](../.env.example), [../Makefile](../Makefile).
