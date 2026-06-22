# Metadata store and data model

This page describes exactly what verix-dbm persists: a small metadata store that holds saved connections, per-connection grants, and an audit log, and nothing from the databases users connect to. It covers the pluggable backend (SQLite or Postgres), the schema, the audit lifecycle, and the per-engine DSN builders. The implementation lives in `internal/store/store.go`.

## 1. The boundary: what is and is not persisted

The metadata store is the only place verix-dbm writes durable state of its own. It holds **three things and only three things**:

1. **Saved connections** (`connections` table): how to reach a target database (kind, host, port, dbname, username, options, read-only flag) plus the connection password as **AES-256-GCM ciphertext** (never plaintext).
2. **Per-connection grants** (`connection_grants` table): which Keycloak group or realm role gets `read`/`write` on a given connection.
3. **The audit log** (`audit` table): a row per mutating action (and login outcomes, credential decrypts, etc.).

It never holds **data from the connected databases**. Rows, tables, schemas, Redis keys, and Mongo documents are read live from the target over a pooled connection ([internal/conn/registry.go](../internal/conn/registry.go)) and streamed to the browser; none of it is cached or copied into the metadata store. The store is small, slow-changing, and safe to back up on its own.

A few consequences of that boundary:

- **Plaintext passwords never touch the store.** Encryption happens at the call site in `internal/web` via `s.box.Encrypt` before `CreateConnection`/`UpdateConnection`; the store column `password_enc` only ever contains ciphertext of the form `<keyID>$<base64(nonce||ciphertext)>`. See [Security](security.md) and `internal/crypto`.
- **The ciphertext is never returned to the browser.** `apiGetConnection` and `apiListConnections` serialize a `connDTO` that has no password field; "Save as copy" reuses the stored ciphertext server-side via `copyFrom` so the secret never round-trips through the client.
- **Audit rows are not foreign-keyed to connections.** `audit.conn_id` has no `REFERENCES`, so audit history **survives connection deletion**. Grants, by contrast, are FK'd and cascade-delete with their connection.

## 2. Pluggable backend: SQLite vs Postgres

The store backend is selected at startup and is one of two drivers. The choice is wired in `cmd/server/main.go` (`openStore`): `store.OpenPostgres(cfg.StoreDSN)` when `DBM_STORE_DRIVER=postgres`, otherwise `store.Open(cfg.SQLitePath)`.

| Backend | Selected by | Open func | `database/sql` driver | Default DSN/path | `SetMaxOpenConns` |
|---|---|---|---|---|---|
| SQLite (default) | `DBM_STORE_DRIVER=sqlite` (or unset) | `store.Open(path)` | `sqlite` (`modernc.org/sqlite`, pure Go) | `DBM_SQLITE_PATH` (default `./data/verix-dbm.db`) | `1` (serialize writes) |
| Postgres (HA) | `DBM_STORE_DRIVER=postgres` + `DBM_STORE_DSN` | `store.OpenPostgres(dsn)` | `pgx` (`github.com/jackc/pgx/v5/stdlib`) | caller-supplied libpq/pgx DSN | `10` (concurrent writers across replicas) |

- **SQLite** is the default single-node backend. It keeps the binary fully static and cgo-free (`CGO_ENABLED=0`), and serializes metadata writes with a single connection. The store DSN appends pragmas: `busy_timeout(5000)`, `journal_mode(WAL)`, `foreign_keys(1)`. The data directory is created at boot (`os.MkdirAll(filepath.Dir(cfg.SQLitePath), 0o755)`) only when the driver is not Postgres.
- **Postgres** is selected for shared, replicated metadata so several app replicas can share one metadata database (high availability). Pair it with Redis-backed sessions (`DBM_SESSION_BACKEND=redis`) so any replica can serve any session. The `DBM_STORE_DSN` is not validated by `config.Load()`; a missing/bad DSN fails when the store opens, not at config parse.

Both open paths route through `finishOpen`, which runs `migrate()` and closes the DB on migration error. The store also exposes `Ping(ctx)` (backs the `/readyz` readiness probe) and `Close()`.

> Note: `DBM_SQLITE_PATH` (the metadata DB file) is distinct from `DBM_SQLITE_DIR` (the allowlist directory for SQLite *target* databases users connect to). They are unrelated; see [Database engines](database-engines.md).

### The single-SQL-written-once design

To avoid maintaining two copies of every query, all store SQL is written once with `?` placeholders and adapted per driver at two seams:

**Placeholder rebinding (`rebind`).** Every query goes through the wrappers `exec`, `query`, and `queryRow`, which call `s.rebind(q)` first. For `driver == "postgres"`, `rebind` rewrites each `?` left-to-right into `$1`, `$2`, `$3`, ...; for SQLite the query is returned unchanged. DDL in `migrate()` is run via `s.db.Exec` directly (no placeholders to rebind).

```go
// internal/store/store.go (paraphrased)
func (s *Store) rebind(q string) string {
    if s.driver != "postgres" {
        return q // SQLite keeps "?"
    }
    // naive left-to-right scan: each "?" becomes "$1", "$2", ...
}
```

> Gotcha: `rebind` is a naive byte scan that replaces **every** `?` regardless of context. This is safe only because no store query embeds a literal `?` inside a string. Do not add one.

**Per-driver schema differences (handled in `migrate()`).** There are exactly two substituted SQL fragments in the `CREATE TABLE` text, both chosen from `s.driver`:

| Concern | SQLite fragment | Postgres fragment |
|---|---|---|
| id columns (`idCol`) | `INTEGER PRIMARY KEY AUTOINCREMENT` | `BIGSERIAL PRIMARY KEY` |
| conn-id reference columns (`connRef`) | `INTEGER` | `BIGINT` |

The audit actor column is not substituted: it is hardcoded as the quoted identifier `"user"` in the DDL (and in every query that touches it), which parses on both engines (`user` is reserved in Postgres; SQLite accepts the quoted form too).

`CreateConnection` uses `INSERT ... RETURNING id`, which works on both SQLite (>= 3.35) and Postgres, so there is no `LastInsertId` divergence to special-case. Timestamps are always stored as RFC3339 UTC strings in `TEXT` columns, so lexical comparison equals chronological comparison (important for the retention purge, which compares `ts < cutoff` as strings).

## 3. Schema (exact DDL)

All tables are created with `CREATE TABLE IF NOT EXISTS`. `<idCol>` and `<connRef>` below are substituted per driver as described above.

### `connections`

The saved target databases. Password is **encrypted at rest** (`password_enc` holds only AES-256-GCM ciphertext) and is **never returned to the browser**.

| Column | Type | Constraints | Notes |
|---|---|---|---|
| `id` | `<idCol>` | PK | autoincrement / serial |
| `name` | `TEXT` | `NOT NULL` | display name |
| `kind` | `TEXT` | `NOT NULL` | dbkinds id: `postgres`, `mysql`, `mariadb`, `sqlite`, `mongodb`, `redis`, `cockroach`, ... |
| `host` | `TEXT` | `NOT NULL` | empty for SQLite (no network host) |
| `port` | `INTEGER` | `NOT NULL` | |
| `dbname` | `TEXT` | `NOT NULL DEFAULT ''` | pg/mysql/mongo database; Redis logical DB number (as string); **SQLite target file path** |
| `username` | `TEXT` | `NOT NULL DEFAULT ''` | |
| `password_enc` | `TEXT` | `NOT NULL DEFAULT ''` | AES-GCM ciphertext only (`<keyID>$<base64>`); never plaintext, never sent to the client |
| `options` | `TEXT` | `NOT NULL DEFAULT ''` | engine-specific, e.g. `sslmode=disable` (pg), `k=v&k2=v2` (MySQL), key-prefix hint (Redis) |
| `readonly` | `INTEGER` | `NOT NULL DEFAULT 0` | connection-level read-only flag (`1`/`0`) |
| `created_by` | `TEXT` | `NOT NULL DEFAULT ''` | actor email at creation |
| `created_at` | `TEXT` | `NOT NULL` | RFC3339 UTC |

### `audit`

One row per audited event. Actor column is the quoted reserved word `"user"`. Not foreign-keyed, so rows outlive the connection they reference.

| Column | Type | Constraints | Notes |
|---|---|---|---|
| `id` | `<idCol>` | PK | |
| `ts` | `TEXT` | `NOT NULL` | RFC3339 UTC; set by `AddAudit`, ignoring any caller value |
| `"user"` | `TEXT` | `NOT NULL DEFAULT ''` | actor email (quoted, reserved word) |
| `conn_id` | `<connRef>` | `NOT NULL DEFAULT 0` | target connection id, or `0` for non-connection events; **no FK** |
| `action` | `TEXT` | `NOT NULL` | event label, e.g. `sql_query`, `create_connection`, `cred_access` |
| `detail` | `TEXT` | `NOT NULL DEFAULT ''` | free-form detail, **already redacted** by the call site (see below) |
| `success` | `INTEGER` | `NOT NULL DEFAULT 1` | `1`/`0` |

### `connection_grants`

Per-connection grants. Cascade-deleted with their connection (FK), and unique per `(conn_id, subject)`.

| Column | Type | Constraints | Notes |
|---|---|---|---|
| `id` | `<idCol>` | PK | |
| `conn_id` | `<connRef>` | `NOT NULL REFERENCES connections(id) ON DELETE CASCADE` | cascade delete |
| `subject` | `TEXT` | `NOT NULL` | Keycloak group path **or** realm-role name |
| `level` | `TEXT` | `NOT NULL` | `read` or `write` (`GrantRead` / `GrantWrite`) |
| `created_by` | `TEXT` | `NOT NULL DEFAULT ''` | actor email |
| `created_at` | `TEXT` | `NOT NULL` | RFC3339 UTC |
| (table) | | `UNIQUE(conn_id, subject)` | one row per subject per connection |

Grants are managed by admins regardless of whether scoping is on, but they only **take effect** when `DBM_SCOPED_ACCESS=true`. A grant scopes *which* connections a non-admin reaches; it never raises a user above their global capability (a `read`-role user with a `write` grant still only reads). Admins bypass scoping entirely. The resolution logic lives in `internal/web/access.go`; see [Security](security.md).

### Go record types

```go
type Connection struct {
    ID int64; Name, Kind, Host string; Port int
    DBName, Username, PasswordEnc, Options string
    ReadOnly bool; CreatedBy string; CreatedAt time.Time
}
type Grant struct {
    ID, ConnID int64; Subject, Level, CreatedBy string; CreatedAt time.Time
}
type Audit struct {
    ID int64; TS time.Time; User string; ConnID int64
    Action, Detail string; Success bool
}
```

Grant level constants: `GrantRead = "read"`, `GrantWrite = "write"`; `ValidGrantLevel(level)` reports whether a string is one of those two.

### Store methods at a glance

| Area | Methods |
|---|---|
| Connections | `ListConnections`, `ListConnectionsForSubjects`, `GetConnection`, `CreateConnection` (returns new id via `RETURNING id`), `UpdateConnection(c, updatePw)`, `UpdatePasswordEnc(id, enc)`, `DeleteConnection` (cascades grants) |
| Grants | `ListGrants(connID)`, `SetGrant` (upsert), `DeleteGrant(connID, id)`, `GrantForSubjects(connID, subjects)` (highest level wins; write outranks read; `nil` if none), `ListConnectionsForSubjects(subjects)` |
| Audit | `AddAudit`, `ListAudit(limit)`, `IterAudit(fn)`, `PurgeAuditOlderThan(cutoff)`, `OnAudit(sink)` |
| Lifecycle | `Open`, `OpenPostgres`, `Close`, `Ping(ctx)` |

`UpdateConnection(c, updatePw)` only rewrites `password_enc` when `updatePw == true`; an empty password field in the edit form means "keep the existing secret". It never touches `created_by`/`created_at`. `UpdatePasswordEnc` rewrites only the ciphertext column and is used by key-rotation re-encryption so no other field is disturbed.

`GrantForSubjects` and `ListConnectionsForSubjects` build their `IN (...)` clause and args via the `placeholders(subjects, lead...)` helper; both return `(nil, nil)` immediately when `subjects` is empty (fail-closed: no subjects means no scoped access, never an invalid `IN ()`).

## 4. The audit event lifecycle

Audit rows are written for every mutating action and a few security-relevant events. The flow is: handler builds an `Audit`, redacts secrets, calls `AddAudit`, the store stamps the time and inserts, then an optional sink mirrors the event.

### Where audit rows come from

- **Mutating API handlers** (`internal/web/api_*.go`) call `st.AddAudit` after the action. Exact action strings include: `create_connection`, `update_connection`, `delete_connection`, `grant_set`, `grant_delete`, `sql_browse`, `sql_query`, `sql_tx`, `sql_ddl_table`, `sql_ddl_drop_table`, `sql_ddl_truncate`, `sql_ddl_drop_column`, `sql_ddl_drop_index`, `sql_ddl_drop_schema`, `sql_ddl_alter_schema`, `sql_ddl_drop_role`, `sql_ddl_alter_role`, `redis_cmd`, `mongo_cmd`, `pg_export_csv`, `pg_export_json`, `reencrypt`, plus the form-driven action label returned by `eng.FormSQL` (e.g. `pg_ddl_add_column`, `mysql_ddl_create_table`).
- **Login outcomes** are audited by `internal/auth` (via the sink set with `a.SetAudit` in `cmd/server/main.go`): `auth_login` on success, `auth_login_failed` on failure (with a specific failure detail).
- **Credential decrypts** fire a separate `cred_access` event: when the connection registry decrypts a stored password to open a pool, the `OnCredentialAccess` callback writes `store.Audit{ConnID, Action:"cred_access", Detail:c.Name, Success:true}`.

### `AddAudit` behavior

`AddAudit(ctx, a)` is **best-effort and never returns an error or blocks the request**:

- It overwrites `a.TS = time.Now().UTC()` (the caller cannot backdate a row).
- It inserts into `audit (ts, "user", conn_id, action, detail, success)`. Any insert error is discarded (`_, _ =`), so a failing audit write never breaks the user-facing action.
- If a sink is registered (`OnAudit`), it is called synchronously after the insert, so the sink must be cheap and non-blocking.

### Password / secret redaction

Redaction happens **at the call site in `internal/web` (the `auditDetail` helper in `internal/web/security.go`) before `AddAudit`** - the store itself stores `detail` verbatim and does no redaction. `auditDetail` runs regex replacements then truncates to 500 characters. It scrubs:

| Source | Example | Becomes |
|---|---|---|
| SQL `PASSWORD '...'` / `IDENTIFIED BY '...'` (quoted, handles `''` escapes) | `CREATE ROLE x PASSWORD 'secret'` | `... PASSWORD '***'` |
| SQL dollar-quoted password (`$$...$$`, `$tag$...$tag$`) | `PASSWORD $$secret$$` | `... '***'` |
| JSON `"pwd"` / `"password": "..."` (Mongo/JSON) | `{"password":"secret"}` | `{"password":"***"}` |
| Redis `requirepass <val>` | `config set requirepass secret` | `... ***` |
| Redis line-anchored `auth <val>` | `auth secret` | `auth ***` |

Redaction is applied to the detail of `sql_query`, `sql_tx`, all DDL actions, `redis_cmd`, and `mongo_cmd`. Stored connection passwords are never written into a detail in the first place (they live encrypted in `password_enc`).

### Reading and exporting the log

| Method | Order | Used by |
|---|---|---|
| `ListAudit(ctx, limit)` | `ORDER BY id DESC LIMIT ?` (newest first) | admin audit view, `GET /api/audit` (`apiAudit`, last 200 rows) |
| `IterAudit(ctx, fn)` | `ORDER BY id` (oldest first), streamed | full-log export (`apiAuditExport`) without buffering |

Export is admin-only at `GET /api/audit/export?format=...`:

- `format=jsonl` (default): `Content-Type: application/x-ndjson`, filename `audit.jsonl`, one JSON object per line.
- `format=csv`: `Content-Type: text/csv`, filename `audit.csv`, header `ts,user,conn_id,action,detail,success`. The `user`, `action`, and `detail` cells are run through `csvSafe`, which prefixes a leading `=`/`+`/`-`/`@`/tab/CR with `'` to neutralize spreadsheet formula injection.
- Any other format value returns `400`.

Because `IterAudit` streams every row ordered by `id`, exports cover the full retained history, not just the last 200.

### Retention purge (`DBM_AUDIT_RETENTION_DAYS`)

Audit rows are kept **forever by default** (`DBM_AUDIT_RETENTION_DAYS=0`, or any negative/unparseable value, which falls back to `0`). When set to a positive `N`, a background goroutine purges old rows:

- Scheduling lives in `cmd/server/main.go` (`retainAudit`): it runs `purge()` once at startup, then on a `24h` ticker.
- Each purge computes `cutoff = now - N*24h` and calls `store.PurgeAuditOlderThan(ctx, cutoff)` with a 30s timeout. The store runs `DELETE FROM audit WHERE ts < ?` using `cutoff.UTC().Format(RFC3339)` (string comparison works because timestamps are RFC3339 UTC). The number of rows removed is returned and logged when non-zero.

See [Configuration](configuration.md) for the full env var reference.

## 5. DSN builder functions per engine

The store also owns the functions that turn a saved `Connection` plus a decrypted password into an engine-specific DSN. These are consumed by the connection registry ([internal/conn/registry.go](../internal/conn/registry.go)) when it opens a target pool. They are methods on `Connection` (except `SQLiteDSN`, a package function).

| Builder | Output format | Key details / gotchas |
|---|---|---|
| `DSN(password)` | Postgres URL `postgres://user:pass@host:port/dbname?opts` | Built with `net/url` (`url.UserPassword`) so credentials/host/db are URL-encoded (prevents parse breakage / param injection). `Options` is passed verbatim as the raw query string. **Default when `Options==""` is `sslmode=prefer`** (try TLS, fall back to plaintext), not `disable`. Prefer explicit `sslmode=verify-full` for remote/prod. |
| `DSNMySQL(password)` | go-sql-driver `FormatDSN()` output | Pins session safety at handshake (never per-query `SET SESSION`): `ParseTime=true`, `Loc=UTC`, `InterpolateParams=false`, `Net=tcp`, plus params `charset=utf8mb4`, `sql_mode='STRICT_ALL_TABLES,NO_ENGINE_SUBSTITUTION'`, `time_zone='+00:00'`. Admin `Options` (split on `&` into `k=v`) are merged and **win** over the pinned defaults (e.g. `tls=skip-verify`). |
| `DSNMongo(password)` | `mongodb://[user:pass@]host:port/dbname?opts` | URL-encoded creds/host/db. The user block is added only when `Username != ""`. The raw query is set only when `Options != ""` (e.g. `replicaSet=rs0&tls=true&authSource=admin`). |
| `SQLiteDSN(path)` (package func) | `path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"` | No `journal_mode(WAL)` (unlike the metadata store's own DSN). No connection-level read-only flag; read-only relies on the per-call `query_only` pragma in `internal/sqlite`. `path` must already be validated via `ResolveSQLitePath`. |

Redis has no DSN method: the registry builds a `redis.NewClient` config inline from `Host`/`Port`/`Username` (default `default`)/`DBName` (parsed as the numeric DB, default `0`).

`Connection.Engine()` returns the engine family via `dbsql.Family(c.Kind)` (`postgres` | `mysql` | `sqlite` | `mongodb` | `redis`), mirroring the SPA's `dbkinds.ts` mapping; unknown kinds default to the Postgres family.

### SQLite path allowlist (fail-closed)

SQLite targets are a server-side file path stored in `Connection.DBName`. `ResolveSQLitePath(allowDir, p)` is the single choke point that validates that path against `DBM_SQLITE_DIR` before the registry opens the file:

- **Empty `allowDir` (i.e. `DBM_SQLITE_DIR` unset) returns an error and disables SQLite entirely** (fail closed): `sqlite connections are disabled: set DBM_SQLITE_DIR to a directory of allowed database files`.
- Empty `p` -> `sqlite file path is required`.
- `allowDir` is `filepath.Abs`'d then `EvalSymlinks`'d; if it is not accessible -> `DBM_SQLITE_DIR %q is not accessible` (the directory must exist).
- The target path is `filepath.Abs`'d, then `resolveExistingPrefix` symlink-resolves the **longest existing prefix** and re-appends not-yet-created components (SQLite creates the file on first open, so the final component may not exist, and a symlink planted on an intermediate directory is still caught), then `filepath.Clean`'d.
- Containment is checked with `filepath.Rel(root, abs)`: the path is rejected if the result is `.`, `..`, or starts with `..` + separator -> `sqlite path %q is outside the allowed directory (DBM_SQLITE_DIR)`. Both `..` traversal and escaping symlinks are rejected.

See [Database engines](database-engines.md) for how the registry consumes these DSNs and manages target pools.

## 6. See also

- [Security](security.md) and [../SECURITY.md](../SECURITY.md): credential encryption (AES-256-GCM keyring, rotation, `cred_access`), RBAC, per-connection grants, CSRF, audit redaction.
- [Configuration](configuration.md) and [../.env.example](../.env.example): `DBM_STORE_DRIVER`, `DBM_STORE_DSN`, `DBM_SQLITE_PATH`, `DBM_SQLITE_DIR`, `DBM_SCOPED_ACCESS`, `DBM_AUDIT_RETENTION_DAYS`, and the rest of the env var reference.
- [Observability](observability.md): how audit events are mirrored to the structured log (`OnAudit` -> the `audit` log line) and the auth-outcome metric, plus the `/readyz` probe that calls `Store.Ping`.
- [Database engines](database-engines.md): the connection registry, engine families, and per-engine pool/DSN handling.
- Source: [internal/store/store.go](../internal/store/store.go), [internal/conn/registry.go](../internal/conn/registry.go), [internal/crypto](../internal/crypto), [internal/web/security.go](../internal/web/security.go), [cmd/server/main.go](../cmd/server/main.go).
