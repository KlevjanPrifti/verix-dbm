# HTTP / JSON API reference

This page documents the verix-dbm HTTP surface: the JSON API mounted at `/api`, the one file-streaming route at `/c/{id}/export`, and the conventions (auth, CSRF, capability gates, error shape, guardrails) that apply to all of them. It is the reference for anyone integrating with or debugging the API. The route table lives in `internal/web/api.go`; handlers are split by domain across `internal/web/api_connections.go`, `api_sql.go`, `api_redis.go`, `api_mongo.go`, `api_audit.go`, and `handlers_export.go`.

For the auth model behind these routes (OIDC login, RBAC, CSRF, per-connection grants) see [Security](security.md) and [../SECURITY.md](../SECURITY.md). For what each engine does behind the SQL routes see [Database engines](database-engines.md).

## API conventions

### Base path and content type

- The whole JSON surface is mounted under `/api` via `r.Route("/api", s.mountAPI)` (`internal/web/server.go`). There is one workbench route outside `/api`: `GET /c/{id}/export` (a file download, see [Export download](#export-download)).
- Requests with a body must send JSON; the SPA client sets `Content-Type: application/json` whenever a body is present. Request bodies are capped at 1 MiB (`http.MaxBytesReader`, `readJSON` in `internal/web/api.go`).
- Responses are JSON: `Content-Type: application/json; charset=utf-8` (`writeJSON`). The export route and the audit export stream files instead (CSV / NDJSON).

### Authentication (session cookie)

- Every route under `/api` (and `/`, `/app`, `/c/{id}/export`) sits inside the authed group: it passes through `s.auth.Middleware` (OIDC session check + RBAC) before reaching a handler. See [Security](security.md#3-sessions) for the login flow.
- The session is a cookie named `dbm_session` (`HttpOnly`, `SameSite=Lax`, `Secure` only when `DBM_BASE_URL` is `https`, 12 hour TTL). Send it with `credentials: 'same-origin'` (the SPA does this automatically).
- Deny-by-default: an authenticated user with no `read` capability gets `403` plain text; an unauthenticated browser request is redirected (`302`) to `/auth/login`. A valid session alone is not sufficient.
- In dev mode (`DBM_DEV_MODE=true`) the middleware auto-logs-in a local admin and mints a cookie + CSRF token, so the API is reachable with no Keycloak. Never enable this in production.

### CSRF: `X-CSRF-Token` on mutating requests

- Every state-changing request (`POST`, `PUT`, `DELETE`) is validated by `s.auth.CheckCSRF(r)`. The check requires the `dbm_session` cookie plus a token that matches the per-session CSRF token (constant-time compared).
- The token is read from the `X-CSRF-Token` header, falling back to a `csrf` form field. The SPA sends the header on every non-`GET` request.
- Fetch the current token from `GET /api/me` (the `csrf` field). It is also surfaced on the user via `User.CSRF`.
- A failed CSRF check returns `403` with `{"error":"bad csrf"}`.
- Gotcha: `GET /c/{id}/export` is a `GET` that nonetheless checks CSRF (it sends `X-CSRF-Token`). `GET /api/audit/export` does not.

### Capability gates

`auth.User` carries three implied capability flags: `admin` -> `write` -> `read`. Realm roles map to them (`OIDC_ADMIN_ROLE`/`OIDC_WRITE_ROLE`/`OIDC_READ_ROLE`, defaults `dbm-admin`/`dbm-write`/`dbm-read`). Per-connection effective access is resolved by `s.access(ctx, u, c) -> {Read, Write}`. When `DBM_SCOPED_ACCESS=true`, a non-admin reaches a connection only through a grant, and a grant scopes *where* a user acts, never above their global capability (admins bypass scoping). See [Security](security.md#deny-by-default-rbac).

The shared gate helpers in `internal/web/api.go` produce these standard outcomes:

| Helper | Sequence | Failure codes |
|---|---|---|
| `connFor(r)` | load connection by id, then require `access(...).Read` | `404` if missing OR inaccessible (existence is not disclosed) |
| `apiSQL(w,r)` | `connFor` (read), then `reg.Engine` open/ping | `404`, then `502` `connect: ...` |
| `apiRequireAdmin(w,r)` | CSRF, then `u.Admin` | `403` `bad csrf`, then `403` `admin required` |
| `apiRequireWrite(w,r,admin)` | CSRF -> optional admin -> `connFor` (read) -> `access(...).Write` -> `c.ReadOnly` -> `reg.Engine` | `403` `bad csrf`, `403` `admin required`, `404`, `403` `write access required`, `409` `connection is read-only`, `502` |

404 cloaking: an inaccessible connection returns the same `404` as a missing one, so scoped users cannot enumerate connections they cannot see.

### Standard error shape

Errors use `apiErr(w, status, msg)`, which writes:

```json
{ "error": "human readable message" }
```

Two response styles coexist:

- DDL / CRUD / introspection endpoints use real HTTP status codes (`400`, `403`, `404`, `409`, `500`, `502`) with the `{"error":...}` body.
- Interactive console / probe endpoints (`apiQuery`, `apiRedisCmd`, `apiMongoCmd`, `apiTestConnection`, and the connect-error path of `apiExplorer`) return `HTTP 200` with an `error` field in the body, so the SPA can render the error inline next to the editor instead of treating it as a transport failure.

### Rate limiting

The whole authed surface is rate limited at 600 requests/minute, keyed per user (by email, falling back to client IP). Over the limit returns `429` `too many requests`. The auth endpoints (`/auth/login`, `/auth/callback`) are separately limited to 20/minute per IP. `/healthz`, `/readyz`, and `/metrics` are not rate limited. See [Security](security.md#9-in-process-rate-limiting).

## Per-connection route prefix `/api/c/{id}/...`

Workbench routes that act on a saved connection are nested under `/api/c/{id}/...`, where `{id}` is the saved-connection id (`int64`; a parse failure yields id `0`). The sub-trees and which engines they serve:

| Sub-prefix | Engines served | Handlers file |
|---|---|---|
| `/api/c/{id}/explorer` | all engines (dispatches by `c.Engine()`) | `api_sql.go` |
| `/api/c/{id}/pg/*` | **all SQL families: PostgreSQL, MySQL/MariaDB, SQLite** | `api_sql.go` |
| `/api/c/{id}/grid` | SQL families | `api_sql.go` |
| `/api/c/{id}/redis/*` | Redis / Valkey | `api_redis.go` |
| `/api/c/{id}/mongo/*` | MongoDB | `api_mongo.go` |

Naming gotcha: the `pg/` segment is historical. Those routes serve all three SQL engines; the engine is chosen per-connection by `c.Engine()` (`dbsql.Family(c.Kind)`), never by the URL. A MySQL connection's columns are fetched from `/api/c/{id}/pg/columns`. See [Database engines](database-engines.md) for the family mapping.

## Guardrails affecting responses

These engine-level guards shape responses on the query/console/grid/export paths. Preserve them when touching query handlers.

- **Row / document cap (1000).** SQL `Query`/`BrowseWhere`, plus the export route, cap results at 1000 rows; the result DTO sets `truncated: true` when the cap is hit. MongoDB caps at 1000 documents. The grid honors a client-selectable page size but clamps it to the 1000 cap (sizes `<= 0` or `> 1000` fall back to the default page size of 100).
- **30s statement timeout.** Every SQL/Mongo operation runs under a 30 second timeout (implemented per engine: Postgres `statement_timeout`, MySQL/SQLite/Mongo a context deadline plus a MySQL optimizer hint). A timeout surfaces as an `error`.
- **Destructive-statement confirmation gate.** On write paths, `dbsql.NeedsConfirm` (SQL), `redisdb.NeedsConfirm` (Redis), and `mongodb.NeedsConfirm` (Mongo) flag dangerous operations: `DROP`/`TRUNCATE` and unguarded `DELETE`/`UPDATE` for SQL; FLUSH/EVAL/CONFIG/etc. for Redis; drop/shutdown/etc. for Mongo. When triggered without `confirm: true`, the handler returns `HTTP 200` with `{ "needConfirm": true, ... }` (echoing the `sql`/`cmd`) instead of executing. Re-send with `confirm: true` to run. The gate is a UX guard, not authorization: a write user can always confirm; for Redis/Mongo the dangerous set is additionally admin-only.
- **Server-side exec / file-access screen.** For non-admins, SQL fragments that touch server-side program execution or file access (e.g. `COPY ... PROGRAM`, `LOAD DATA INFILE`, `ATTACH`) are blocked. On the grid path this returns `403`; on the console path it returns `HTTP 200` `{error:...}`. Admins are exempt. This is a best-effort screen, not the real control (use a least-privileged DB role).

## Endpoint reference

In the tables below: **Gate** is the capability required beyond baseline authentication; **CSRF** is whether `CheckCSRF` is enforced (always so on `POST`/`PUT`/`DELETE`). Unless noted, request/response bodies are JSON. The complete `/api` route table is registered in `mountAPI` (`internal/web/api.go`).

### Session

| Method | Path | Gate | CSRF | Purpose |
|---|---|---|---|---|
| GET | `/api/me` | authed | no | Current user + tokens. Returns `{ user:{name,email,admin,write}, csrf, scopedAccess }`. `scopedAccess` reflects `DBM_SCOPED_ACCESS`. Use this to obtain the CSRF token. |

### Connections (CRUD + test)

Handlers in `api_connections.go`. The `connDTO` shape is `{ id, name, kind, host, port, dbname, username, options, readOnly }`. The stored password ciphertext is intentionally never returned to the browser.

| Method | Path | Gate | CSRF | Purpose |
|---|---|---|---|---|
| GET | `/api/connections` | authed | no | List connections as `{ connections: [connDTO] }`. Scoped non-admins see only granted connections (`ListConnectionsForSubjects`); others see all. |
| POST | `/api/connections` | admin | yes | Create. Body adds `password` and optional `copyFrom` (source id for "Save as copy", reusing stored ciphertext). Runs the SSRF egress guard (`400` on block, SQLite exempt). Encrypts the password. Audits `create_connection`. Returns `{ id }`. |
| POST | `/api/connections/test` | admin | yes | Probe a candidate connection (6s timeout). Dispatches by `c.Engine()` (`pingPG`/`pingMySQL`/`pingSQLite`/`pingRedis`/`pingMongo`). Always returns `HTTP 200` `{ ok: bool [, error] }`, including for a blocked egress target. |
| GET | `/api/connections/{id}` | admin | no | Single connection as `{ connection: connDTO }`. |
| PUT | `/api/connections/{id}` | admin | yes | Update. SSRF guard (`400`). Re-encrypts the password only when a non-empty `password` is sent (empty means keep existing). Drops the cached pool (`reg.Forget`). Audits `update_connection`. Returns `{ ok: true }`. |
| DELETE | `/api/connections/{id}` | admin | yes | Delete (cascades grants), drops the cached pool. Audits `delete_connection`. Returns `{ ok: true }`. |

### Grants

Per-connection grants (`grantDTO` = `{ id, subject, level }`; `level` is `read` or `write`). Grant management works regardless of `DBM_SCOPED_ACCESS` (so access can be configured ahead of time); grants only take effect when scoping is on. See [Security](security.md#scoped-per-connection-grants-dbm_scoped_access).

| Method | Path | Gate | CSRF | Purpose |
|---|---|---|---|---|
| GET | `/api/connections/{id}/grants` | admin | no | List grants -> `{ grants: [grantDTO] }`. |
| PUT | `/api/connections/{id}/grants` | admin | yes | Upsert one `{ subject, level }` grant. `404` if the connection is missing; `400` if `subject` is empty or `level` is invalid. Audits `grant_set`. |
| DELETE | `/api/connections/{id}/grants/{gid}` | admin | yes | Delete grant `{gid}` scoped to `{id}`. Audits `grant_delete`. |

### Explorer + SQL introspection

Handlers in `api_sql.go`. `columnDTO` = `{ name, type, typeText, cat, notNull, default, pk, autoInc }`.

| Method | Path | Gate | CSRF | Purpose |
|---|---|---|---|---|
| GET | `/api/c/{id}/explorer` | read | no | Engine-aware tree root. Redis -> `{kind:"redis"}` (no connection opened). MongoDB -> `{kind:"mongodb", databases [, error]}`. SQL -> `{kind:<family>, schemas [, error]}`. Connect/query failures here surface as `HTTP 200` with an `error` field, not `502`. |
| GET | `/api/c/{id}/pg/columns` | read | no | Columns for `?schema&table` -> `{ columns: [columnDTO] }`. |
| GET | `/api/c/{id}/pg/indexes` | read | no | Indexes for `?schema&table` -> `{ indexes }`. |
| GET | `/api/c/{id}/pg/keys` | read | no | Keys (PK/FK/unique/check) for `?schema&table` -> `{ keys }`. |
| GET | `/api/c/{id}/pg/doc` | read | no | Combined `{ schema, table, columns, keys, indexes, comment }` for the documentation tab. |
| GET | `/api/c/{id}/pg/usages` | read | no | Inbound foreign keys referencing the table -> `{ schema, table, usages [, error] }`. |
| GET | `/api/c/{id}/pg/generate` | read | no | Code generation by `?kind=select\|insert\|update\|create` (plus `?schema&table`). Unknown kind -> `400` `unknown kind`. Returns `{ sql }`. |
| GET | `/api/c/{id}/pg/roles` | admin | no | Cluster roles / users -> `{ roles }`. Admin-gated even for reads because it exposes accounts. |

### Grid browse

| Method | Path | Gate | CSRF | Purpose |
|---|---|---|---|---|
| GET | `/api/c/{id}/grid` | read | no | Paginated browse. Params `?schema&table&where&order&page&size`. Server-side-blocked fragments -> `403`. `page` clamped `>= 0`; `size` defaults to 100 and is clamped to the 1000 cap. Always read-only at the engine. Audits `sql_browse`. Returns `{ result: resultDTO, readOnly, page [, error] }`, where `readOnly = c.ReadOnly || !access.Write`. |

`resultDTO` = `{ columns, rows ([][]string), isSelect, rowsAffected, command, duration (string), truncated }`. The NULL sentinel inside `rows` is `∅`.

### SQL query / exec

| Method | Path | Gate | CSRF | Purpose |
|---|---|---|---|---|
| POST | `/api/c/{id}/pg/query` | read (manual gates) | yes | SQL console. Body `{ sql, confirm, schema }`. `readOnly = c.ReadOnly || !access.Write` is passed to the engine (write gating is implicit; this handler does not use `apiRequireWrite`). Empty SQL, server-side-blocked SQL, and engine errors all return `HTTP 200` with `{error:...}`. On a destructive write without `confirm`, returns `{ needConfirm: true, sql }`. Audits `sql_query`. Success returns `{ readOnly, result }`. |
| POST | `/api/c/{id}/pg/tx` | write | yes | Atomic batch (grid manual-transaction mode). `apiRequireWrite`. Body `{ statements: [], confirm }`. `400` if no statements; `403` if any is server-side-blocked; `{ needConfirm: true }` if any needs confirmation and `confirm` is false. Runs `ExecScript`. Audits `sql_tx`. Returns `{ ok: true, count }`. |

### DDL forms + table designer

All in `api_sql.go`. Note the admin gating on drop/truncate operations.

| Method | Path | Gate | CSRF | Purpose |
|---|---|---|---|---|
| GET | `/api/c/{id}/pg/form` | write | no | Prefill for the modify-column modal. For `?kind=modify-column&schema&table&column` returns live `{ name, type, nullable, default }`. |
| POST | `/api/c/{id}/pg/ddl/run` | write (admin if `create-user`) | yes | Form-driven DDL. Builds a `dbsql.FormSpec` from the body (`kind, schema, table, column, name, type, default, columns, nullable, unique, owner, password, login, createdb, createrole, superuser, host`). `kind="create-user"` additionally requires admin. Audit action is the form's own action label (e.g. `pg_ddl_add_column`). Returns `{ ok: true }` or `400`. |
| POST | `/api/c/{id}/pg/table/apply` | write | yes | Apply the table-designer statement list atomically. Body `{ action, statements: [] }`. `400` if empty. Audit action = `action` or default `sql_ddl_table`. Returns `{ ok: true }`. |
| POST | `/api/c/{id}/pg/table/drop` | **admin** | yes | Drop table. Audits `sql_ddl_drop_table`. |
| POST | `/api/c/{id}/pg/table/truncate` | **admin** | yes | Truncate table. Audits `sql_ddl_truncate`. |
| POST | `/api/c/{id}/pg/column/drop` | **admin** | yes | Drop column. Audits `sql_ddl_drop_column`. |
| POST | `/api/c/{id}/pg/index/drop` | **admin** | yes | Drop index. Audits `sql_ddl_drop_index`. |
| POST | `/api/c/{id}/pg/schema/drop` | **admin** | yes | Drop schema. Body `{ schema, cascade }`. `400` if schema empty. Audits `sql_ddl_drop_schema`. |
| POST | `/api/c/{id}/pg/schema/alter` | write | yes | Rename / re-own schema. Body `{ schema, newName, owner }`. `400` `nothing to change` if no-op. Audits `sql_ddl_alter_schema`. |
| POST | `/api/c/{id}/pg/roles` (GET) | admin | no | (listed above) cluster roles. |
| POST | `/api/c/{id}/pg/role/drop` | **admin** | yes | Drop role/user. Body `{ name, host }` (`host` is the MySQL user host, ignored by Postgres). `400` if name empty. Audits `sql_ddl_drop_role`. |
| POST | `/api/c/{id}/pg/role/alter` | **admin** | yes | Alter role/user. Body `{ name, newName, password, login, createdb, createrole, superuser, host }`. `400` if name empty or no-op. Audits `sql_ddl_alter_role`. |

DDL note: MySQL/MariaDB DDL is not transactional (`AtomicDDL()` is false), so a mid-batch DDL failure can leave earlier statements applied. Postgres and SQLite roll back. See [Database engines](database-engines.md).

### Redis

Handlers in `api_redis.go`. The read allowlist (`redisReadAllow`) covers `get, mget, type, ttl, pttl, scan, hget, hgetall, hkeys, hlen, lrange, llen, smembers, scard, zrange, zcard, exists, strlen, info, dbsize, ping, memory, object`.

| Method | Path | Gate | CSRF | Purpose |
|---|---|---|---|---|
| GET | `/api/c/{id}/redis/keys` | read | no | SCAN keyspace. `?match` (defaults `*`), `?cursor`. Page size is 100. Connect failure -> `502`. Returns `{ keys, cursor }`. |
| GET | `/api/c/{id}/redis/value` | read | no | Type-aware value for `?key` -> `{ value }`. |
| POST | `/api/c/{id}/redis/cmd` | read (manual gates) | yes | Command console. Body `{ cmd, confirm }`. In read-only context, a command not in the allowlist returns `HTTP 200` `{error:...}`. A dangerous command requires admin and confirmation (`{ needConfirm: true, cmd }` when not confirmed). Audits `redis_cmd`. Errors are inline (`HTTP 200` `{error}`); success returns `{ out }`. |

### MongoDB

Handlers in `api_mongo.go`. There is no dedicated databases/collections/insert/drop endpoint: databases come from `/explorer`, and inserts/drops/collection ops go through `/mongo/cmd` (e.g. `{insert:...}`, `{drop:...}`).

| Method | Path | Gate | CSRF | Purpose |
|---|---|---|---|---|
| GET | `/api/c/{id}/mongo/docs` | read | no | Paginated find. `?db&coll` (required, else `400`), `?filter&sort&projection` (relaxed extended JSON), `?size` (default 50), `?page` (`>= 0`). Non-admin use of server-side JS (`$where`/`$function`/`$accumulator`) -> `403`. Connect failure -> `502`. Returns `{ docs, hasMore, page }`. |
| GET | `/api/c/{id}/mongo/indexes` | read | no | Collection indexes. `?db&coll` (required, else `400`) -> `{ indexes }`. |
| POST | `/api/c/{id}/mongo/cmd` | read (manual gates) | yes | DB command console. Body `{ db, cmd, confirm }`. Empty `db` or an unparseable command -> `HTTP 200` `{error}`. In read-only context, a command not in the read allowlist -> `{error}`. A dangerous command requires admin and confirmation (`{ needConfirm: true, cmd }` when not confirmed). Audits `mongo_cmd`. Success returns `{ out }`. |

### Audit + admin

Handlers in `api_audit.go`. `auditDTO` = `{ ts (RFC3339), user, connId, action, detail, success }`. Audit details are redacted of secrets before storage/export.

| Method | Path | Gate | CSRF | Purpose |
|---|---|---|---|---|
| GET | `/api/audit` | admin | no | Most recent 200 audit rows -> `{ rows: [auditDTO] }`. |
| GET | `/api/audit/export` | admin | no | Streams the full audit log (not buffered). `?format=jsonl` (default; `application/x-ndjson`, filename `audit.jsonl`) or `csv` (`text/csv`, filename `audit.csv`, header `ts,user,conn_id,action,detail,success`). Other format -> `400`. CSV cells are formula-injection-neutralized. |
| POST | `/api/admin/reencrypt` | admin | yes | Key-rotation step: re-encrypt all stored credentials under the primary key. Idempotent (skips already-primary and empty passwords); drops affected pools. Audits `reencrypt`. Returns `{ primaryKey, checked, rewritten, failed }`. |

### Export download

Defined in `handlers_export.go`. This is the one workbench route outside `/api` because it streams a file.

| Method | Path | Gate | CSRF | Purpose |
|---|---|---|---|---|
| GET | `/c/{id}/export` | read | **yes** | Stream a table snapshot as CSV (default) or JSON (`?format=json`); other params `?schema&table&where&order`. CSRF is checked even though this is a `GET` (`403 bad csrf` on failure). Server-side-blocked fragments -> `403`. Always uses a hard 1000-row cap (a convenience snapshot, not a full dump). Audits `pg_export_csv` / `pg_export_json`. Sends `Content-Disposition: attachment; filename="<schema>_<table>.<fmt>"`; CSV cells are formula-injection-neutralized. |

## Audit actions

Mutating handlers write an audit row; the exact `action` strings are: `create_connection`, `update_connection`, `delete_connection`, `grant_set`, `grant_delete`, `sql_browse`, `sql_query`, `sql_tx`, `sql_ddl_table` (default), `sql_ddl_drop_table`, `sql_ddl_truncate`, `sql_ddl_drop_column`, `sql_ddl_drop_index`, `sql_ddl_drop_schema`, `sql_ddl_alter_schema`, `sql_ddl_drop_role`, `sql_ddl_alter_role`, `redis_cmd`, `mongo_cmd`, `pg_export_csv`, `pg_export_json`, `reencrypt`, plus the form-specific action returned by `eng.FormSQL` (e.g. `pg_ddl_create_table`, `mysql_ddl_create_user`). Decrypting a stored password to open a pool additionally emits `cred_access` (from the connection registry). Audit details run through `auditDetail`, which redacts SQL/JSON/Redis passwords and truncates to 500 characters. See [Observability](observability.md) for how audit events mirror to the structured log.

## Operational endpoints (no auth, not under `/api`)

These sit outside the auth and rate-limit groups and are excluded from request logging and metrics. Detail in [Observability](observability.md).

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/healthz` | none | Liveness. Always `200` body `ok`. |
| GET | `/readyz` | none | Readiness. `200` `{"status":"ok"}` when the metadata store pings (2s timeout), else `503` `{"status":"unavailable"}`. |
| GET | `/metrics` | optional Bearer | Prometheus metrics. Open when `DBM_METRICS_TOKEN` is unset; otherwise requires `Authorization: Bearer <token>` (`401` on mismatch). |

## Auth endpoints (not under `/api`)

| Method | Path | CSRF | Purpose |
|---|---|---|---|
| GET | `/auth/login` | no | Start the OIDC authorization-code + PKCE flow (302 to the IdP). Rate limited 20/min per IP. |
| GET | `/auth/callback` | no | OIDC redirect handler; on success sets the session cookie and 302s to `/`. Rate limited 20/min per IP. |
| POST | `/auth/logout` | yes | Log out (POST only, CSRF required, deliberately not `GET` so a cross-site page cannot force logout). |

The SPA shell: `GET /` redirects (302) to `/app`; `/app` and `/app/*` serve the embedded React workbench.

## See also

- [Security](security.md) and [../SECURITY.md](../SECURITY.md): the OIDC flow, RBAC, per-connection grants, CSRF, and the SSRF egress guard behind these routes.
- [Database engines](database-engines.md): the engine families behind the `pg/`, `redis/`, and `mongo/` sub-trees, the `dbsql.Engine` interface, and the shared guardrails.
- [Architecture](architecture.md): the router, middleware stack, and the connection registry that opens pools per request.
- [Configuration](configuration.md) and [../.env.example](../.env.example): every environment variable that gates the behaviors above.
