# Database engines

verix-dbm connects to five engine families behind one workbench: PostgreSQL, MySQL/MariaDB, SQLite, MongoDB, and Redis/Valkey. The three SQL families share a single engine-neutral interface (`internal/dbsql`) so they reuse the grid/console/doc/usages tabs; MongoDB and Redis are non-SQL verticals with their own tabs and `/api/c/{id}/{mongo,redis}/*` endpoints. This page is the reference for what each engine can do, the guardrails they all enforce, how each enforces read-only, and the exact recipe to add a new one.

## Capability matrix

| Capability | PostgreSQL | MySQL/MariaDB | SQLite | MongoDB | Redis/Valkey |
|---|---|---|---|---|---|
| Family constant (`internal/dbsql`) | `FamilyPostgres` | `FamilyMySQL` | `FamilySQLite` | `FamilyMongo` | `FamilyRedis` |
| Implements `dbsql.Engine` | yes | yes | yes | no (vertical) | no (vertical) |
| Browse / explore | schemas -> tables -> cols/keys/indexes | databases -> tables | single `main` schema | databases -> collections | SCAN keyspace |
| Grid (paginated browse) | yes | yes | yes | doc browser (`mongo/docs`) | value viewer per key |
| SQL/command console | yes | yes | yes | DB command console (`mongo/cmd`) | command console (`redis/cmd`) |
| DDL (forms + designer) | yes (atomic) | yes (non-atomic) | partial (no users, no extra schemas, no modify-column) | none (use commands) | none |
| Code generators (`GenSelect`/`Insert`/`Update`/`CreateTableDDL`/`FindUsages`/`TableComment`) | full | full (DDL via `SHOW CREATE TABLE`) | partial (DDL via stored `sqlite_master` SQL; no comments) | n/a | n/a |
| Roles / users | yes (`pg_roles`) | yes (`mysql.user`, additive grants) | none | n/a | n/a |
| Export (CSV/JSON, `GET /c/{id}/export`) | yes | yes | yes | no | no |
| Read-only enforcement mechanism | `SET default_transaction_read_only` per call | `BeginTx(ReadOnly: true)` | `PRAGMA query_only=ON` (per pinned conn) | command allowlist (`readAllow`) | command allowlist (`redisReadAllow`) |
| Statement timeout (30s) | `SET statement_timeout='30s'` | `context` 30s + `MAX_EXECUTION_TIME` hint on bare SELECT | `context` 30s (+ `busy_timeout(5000)`) | `context` 30s (`opTimeout`) | none |
| Result / document cap | 1000 rows | 1000 rows | 1000 rows | 1000 docs (page default 50) | collection viewer cap 500; SCAN page 100 |
| Destructive-statement gate | `dbsql.NeedsConfirm` | `dbsql.NeedsConfirm` | `dbsql.NeedsConfirm` | `mongodb.NeedsConfirm` (admin + confirm) | `redisdb.NeedsConfirm` (admin + confirm) |
| Server-side exec / file-access screen (non-admin) | `IsServerSideExec` (`COPY ... PROGRAM`, `pg_read_file`, `lo_import`, ...) | `IsServerSideExec` (`LOAD DATA INFILE`, `INTO OUTFILE/DUMPFILE`, `LOAD_FILE`) | `IsServerSideExec` (`ATTACH`, `load_extension`, `VACUUM ... INTO`, `writefile`/`readfile`) | server-side JS block (`$where`, `$function`, `$accumulator`) | dangerous-command gate (EVAL/FUNCTION/MODULE/CONFIG/...) |
| Special gate | SQLSTATE 25006 guard on pooled conns; multi-statement rejected on read-only path | `AtomicDDL()=false`: mid-batch DDL failure leaves earlier stmts applied | `DBM_SQLITE_DIR` allowlist, fail-closed when unset | server-side JS blocked for non-admins | FLUSHALL/FLUSHDB and scripting require admin + confirm |

URL note: every SQL-family route is mounted under the literal `/api/c/{id}/pg/*` prefix (Postgres, MySQL, and SQLite alike). The prefix is historical; the engine is chosen per connection by `c.Engine()`, not by the path. See [API reference](api-reference.md).

## Shared guardrails

These apply to every query/console path. Preserve them when you touch a query handler.

### Statement timeout (30 seconds)

Each engine enforces the limit with the mechanism its driver supports:

| Engine | Mechanism |
|---|---|
| Postgres | `SET statement_timeout = '30s'` on the acquired conn in `Query`, and per transaction in `ExecScript` (`defaultStatementTimeout = "30s"`). |
| MySQL | `context.WithTimeout(ctx, 30s)` on every op, plus a `MAX_EXECUTION_TIME(30000)` optimizer hint injected only into bare `SELECT` (no `SET SESSION`, which would leak across the shared pool). |
| SQLite | `context.WithTimeout(ctx, 30s)`; the DSN also sets `busy_timeout(5000)`. |
| MongoDB | `opTimeout = 30 * time.Second` context on every operation. |
| Redis | No per-call statement timeout in `internal/redisdb`. |

### Result / document cap (1000)

- Postgres, MySQL, SQLite: `maxRows = 1000` in `Query` and browse; `Result.Truncated` is set when the cap trips. The default browse page size (when `limit <= 0 || limit > maxRows`) is **100**.
- MongoDB: `maxDocs = 1000` (cap on `limit`), default page `pageDocs = 50`; the engine fetches `limit + 1` to compute `HasMore` without a count.
- Redis: the collection viewer caps at `cap = 500` (lists via `LRANGE 0 499`, zsets via `ZRANGE 0 499`); SCAN page default `count = 100`.
- Across the three SQL engines a NULL is rendered as the sentinel `"∅"` in row sets.

### Destructive-statement confirmation gate

`dbsql.NeedsConfirm(sql)` (`internal/dbsql/dbsql.go`) is the SQL gate. It strips block (`/* */`) and line (`--`) comments, splits on `;`, and examines **every** statement, tripping when a statement:

- starts with `drop` or `truncate`, or
- starts with `delete` or `update` and contains no `where` clause.

It is a UX guard, not authorization: a write-capable user can always confirm. The naive `;` split over-prompts in edge cases (semicolons inside string literals), which is the safe direction. It catches a leading comment (`/*x*/ DROP ...`) and a destructive statement hidden after a harmless one (`SELECT 1; DROP TABLE t`).

The non-SQL verticals have their own confirm functions:

- `redisdb.NeedsConfirm(args)` for Redis dangerous commands.
- `mongodb.NeedsConfirm(name)` for Mongo dangerous commands.

For both verticals the handler treats a confirm-needing command as **admin-only AND confirm-gated**.

### Read-only enforcement, per engine

`BrowseWhere` is always run read-only (the WHERE filter is unparameterized raw SQL, so a read-only transaction is what actually stops mutation). Because pools are shared and connections are reused, every engine must avoid leaking session state across acquires.

| Engine | Read-only mechanism | Gotcha to preserve |
|---|---|---|
| Postgres | `SET default_transaction_read_only = on/off` on the acquired pooled conn on every call (a session GUC). | A conn left read-only and reused for a write fails with **SQLSTATE 25006**. Both `Query` (resets every call) and `ExecScript` (runs `SET TRANSACTION READ WRITE` first) guard against this. Multi-statement input is **rejected** on the read-only path (`"multi-statement scripts are not allowed in read-only mode; run one statement at a time"`) because the simple protocol could escalate past the GUC. |
| MySQL | `BeginTx(ctx, &sql.TxOptions{ReadOnly: true})`: a READ ONLY transaction pinned to one conn and rolled back on close, so the read-only path never touches shared pool state. | Session safety (`sql_mode`, `time_zone`, `charset`) is pinned at the DSN handshake (`store.DSNMySQL`), never via `SET SESSION`. |
| SQLite | `PRAGMA query_only=ON` on a pinned `conn`, reset to `OFF` before returning it to the pool. | The reset uses a **fresh `context.Background()` (5s)** so a cancelled request context cannot skip the reset and leave a permanently read-only pooled conn. The DSN has no read-only flag; it relies entirely on the per-call pragma. |
| MongoDB | Read-only users may run only commands in `readAllow` (`mongodb.ReadAllowed(name)`). | Inserts/drops/collection ops go through `mongo/cmd`, gated by the allowlist + admin/confirm gate. |
| Redis | Read-only users may run only commands in `redisReadAllow` (handler-side). | Dangerous commands additionally require admin + confirm. |

### Server-side exec / file-access screen

`IsServerSideExec(sql)` (on each `dbsql.Dialect`) is a conservative keyword screen, **blocked for non-admin users** (`serverSideBlocked` in `internal/web/security.go`; admins are exempt). It is defense in depth, not authorization. A read-only transaction does NOT block these primitives, which is why this separate screen exists.

| Engine | Matched primitives |
|---|---|
| Postgres | `COPY ... PROGRAM`, `pg_read_file`, `pg_read_binary_file`, `pg_ls_dir`, `pg_stat_file`, `pg_ls_logdir`, `pg_ls_waldir`, `lo_import`, `lo_export`, `pg_execute_server_program`, `pg_read_server_files`, `pg_write_server_files` |
| MySQL | `LOAD DATA [LOCAL] INFILE`, `INTO OUTFILE`, `INTO DUMPFILE`, `LOAD_FILE(` |
| SQLite | `ATTACH`, `load_extension(`, `VACUUM ... INTO`, `writefile(`, `readfile(` |

The blocked message is `"blocked: server-side program execution / file access is restricted to admins see SECURITY.md"`. The documented real control is a least-privileged DB role on the target. See [../SECURITY.md](../SECURITY.md).

## PostgreSQL (`internal/postgres`)

The reference SQL engine, built on pgx v5 (`*pgxpool.Pool`). Family `FamilyPostgres` also serves the Postgres-wire kinds `cockroach`, `greenplum`, `redshift`, `yugabyte`, `timescale`, and `aurorapg` (see `kindFamily`).

- **Introspection** reads `pg_catalog` (`pg_namespace`, `pg_class`, `pg_attribute`, `pg_attrdef`, `pg_index`, `pg_constraint`, `pg_roles`). `Schemas` LEFT JOINs from `pg_namespace` so empty schemas still appear; it excludes `pg_catalog`, `information_schema`, `pg_toast%`, `pg_temp%`, `pg_toast_temp%`. `relkind` maps `r`/`p` -> table, `v` -> view, `m` -> matview. `Roles` reads `pg_roles` and skips the `pg_*` predefined roles.
- **Query** acquires a conn, sets `statement_timeout`, then `search_path` (`SET search_path TO <schema>, public` when a schema is given, else `SET search_path TO DEFAULT`), then the read-only GUC. When the extended protocol raises 42601 for a multi-command string (`isMultiCommand`), the write path re-runs via the simple protocol (`querySimple`) and returns the last result.
- **Transactional DDL via `ExecScript`** (`internal/postgres/ddl.go`): one transaction; it runs `SET TRANSACTION READ WRITE` then `SET statement_timeout`, executes each non-blank statement, and rolls back on any error so a table is never left half-altered. `AtomicDDL()` returns **true**.
- **Code generators** introspect then build strings: `GenSelect` (named columns + `LIMIT 100`), `GenInsert` (all columns, `null` values), `GenUpdate` (skips PK columns, `WHERE <condition>`), `CreateTableDDL` (reconstructs `CREATE TABLE` + standalone indexes via `pg_get_indexdef`, constraints via `pg_get_constraintdef`), `TableComment` (`obj_description`), and `FindUsages` (inbound FKs via `pg_constraint contype='f'`).
- **Form DDL** (`FormSQL`, with audit labels): `add-column` -> `pg_ddl_add_column`, `modify-column` -> `pg_ddl_modify_column` (`ALTER COLUMN TYPE` + SET/DROP NOT NULL + SET/DROP DEFAULT), `rename-table` -> `pg_ddl_rename_table`, `new-schema` -> `pg_ddl_create_schema` (optional `AUTHORIZATION`), `new-table` -> `pg_ddl_create_table`, `new-index` -> `pg_ddl_create_index`, `create-user` -> `pg_ddl_create_role`.
- **Roles**: `CREATE ROLE ... WITH` options; `ALTER ROLE` emits explicit negatives (`NOLOGIN`/`NOSUPERUSER`/...) to set privileges exactly; RENAME is a separate trailing statement.

## MySQL / MariaDB (`internal/mysql`)

One driver (`go-sql-driver/mysql`) serves both kinds (`mysql`, `mariadb`) over a `*sql.DB` pool. Identifiers are backtick-quoted; `quoteLiteral` escapes both backslash and `'`.

- **Introspection** reads `information_schema` (`SCHEMATA`, `TABLES`, `COLUMNS` via `COLUMN_TYPE`, `STATISTICS`, `TABLE_CONSTRAINTS`, `KEY_COLUMN_USAGE`, `REFERENTIAL_CONSTRAINTS`). System DBs excluded: `mysql`, `sys`, `performance_schema`, `information_schema`.
- **Roles**: reads `mysql.user`. On privilege error **1142** (table-level) or **1227** (global priv) it returns `(nil, nil)`, i.e. an empty list rather than an error. `CanLogin` is always reported true (lock state is not read). The role editor is **additive** (`grantsFor` issues GRANTs only, never REVOKEs); `AlterUserSQL` handles RENAME USER, IDENTIFIED BY, and ACCOUNT LOCK/UNLOCK. Users are `'name'@'host'` (default host `%`).
- **DDL**: `CreateTableDDL` uses `SHOW CREATE TABLE` (indexes + FKs inline). `AtomicDDL()` returns **false** (MySQL/MariaDB DDL implicitly commits, so a mid-batch DDL failure leaves earlier statements applied; pure DML batches on InnoDB are atomic). The `schema` arg is ignored in `Query` (no per-statement `search_path`; a `USE` would mutate the pooled conn). `returnsRows` (routes to `QueryContext`) covers `SELECT, SHOW, DESC, DESCRIBE, EXPLAIN, WITH, TABLE, VALUES, CALL, ANALYZE, CHECK, HELP`.
- **MODIFY COLUMN**: `modify-column` (`mysql_ddl_modify_column`) uses `MODIFY`, which redefines the whole column. Other form labels: `mysql_ddl_add_column`, `mysql_ddl_rename_table`, `mysql_ddl_create_schema` (`CREATE DATABASE`), `mysql_ddl_create_table`, `mysql_ddl_create_index`, `mysql_ddl_create_user`. `DropSchemaSQL` maps to `DROP DATABASE` (cascade ignored); `AlterSchemaSQL` returns `nil` (no rename/owner, and the SPA hides schema-alter for MySQL).
- **Blocked for non-admins**: `LOAD DATA [LOCAL] INFILE`, `INTO OUTFILE`, `INTO DUMPFILE`, and `LOAD_FILE(` via `IsServerSideExec` (see the shared screen above).

## SQLite (`internal/sqlite`)

Pure-Go `modernc.org/sqlite` over a `*sql.DB`, targeting a **server-side file path** (stored in `Connection.DBName`). It exposes a synthetic single schema `mainSchema = "main"`. There are no users/roles (`Roles` -> nil; `AlterUserSQL`/`DropUserSQL`/`DropSchemaSQL`/`AlterSchemaSQL` -> nil), no table comments (`TableComment` -> ""), and `est_rows` is always 0.

- **Introspection** reads `sqlite_master` (excluding `sqlite_%`) plus table-valued pragmas (`pragma_table_info`, `pragma_index_list`, `pragma_index_info`, `pragma_foreign_key_list`, `pragma_database_list`). `AutoInc` is approximated as a PK column whose type contains `INT` (rowid alias). `CreateTableDDL` returns the original `sql` from `sqlite_master`. `returnsRows` covers `SELECT, WITH, VALUES, PRAGMA, EXPLAIN`.
- **DDL**: `AtomicDDL()` returns **true** (SQLite DDL is transactional). `TruncateSQL` emits `DELETE FROM ...` (no `TRUNCATE`). Form labels: `add-column` -> `sqlite_ddl_add_column`, `rename-table` -> `sqlite_ddl_rename_table`, `new-table` -> `sqlite_ddl_create_table`, `new-index` -> `sqlite_ddl_create_index`. `FormSQL` returns **errors** (not invalid SQL) for `modify-column` (`"SQLite cannot modify a column in place..."`), `new-schema` (single namespace), and `create-user` (no users or roles).
- **`PRAGMA query_only`**: read-only is enforced by `PRAGMA query_only=ON` on a pinned conn, reset to `OFF` on a fresh `context.Background()` (5s) before returning the conn to the pool. The DSN has no read-only flag (see read-only table above).
- **Blocked for non-admins**: `ATTACH`, `load_extension(`, `VACUUM ... INTO`, `writefile(`, `readfile(`.

### `DBM_SQLITE_DIR` allowlist (fail-closed)

A SQLite connection opens only files resolving under `DBM_SQLITE_DIR`. Validation happens once, in `store.ResolveSQLitePath`, the single open choke point called by `Registry.SQLite` before `sql.Open`.

- **Fail closed when unset**: an empty/whitespace `allowDir` returns `"sqlite connections are disabled: set DBM_SQLITE_DIR to a directory of allowed database files"`. The registry default `sqliteDir = ""` disables SQLite entirely; the engine is opt-in.
- An empty target path returns `"sqlite file path is required"`.
- `allowDir` is `filepath.Abs` then `filepath.EvalSymlinks` (real path); if not accessible it returns `"DBM_SQLITE_DIR %q is not accessible"`.
- The target path is `filepath.Abs`, then `resolveExistingPrefix` (symlink-resolves the longest existing prefix and re-appends not-yet-created components, so a symlink planted on an **intermediate** directory of a not-yet-created file is still caught, since SQLite creates the file on first open), then `filepath.Clean`.
- Containment uses `filepath.Rel(root, abs)`; it is rejected if the result is `.`, `..`, or starts with `..` + separator (`"sqlite path %q is outside the allowed directory (DBM_SQLITE_DIR)"`). So both `..` traversal and escaping symlinks are refused.
- `store.SQLiteDSN(path)` = `path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"` (no connection-level read-only flag). Note `DBM_SQLITE_DIR` (target-file allowlist) is distinct from `DBM_SQLITE_PATH` (the metadata store file). See [Configuration](configuration.md).

## MongoDB (`internal/mongodb`)

A non-SQL vertical on `go.mongodb.org/mongo-driver` (`*mongo.Client`), reached at `/api/c/{id}/mongo/*` and NOT through `Registry.Engine`. System DBs `admin`, `local`, `config` are hidden. **No DDL**: there are no schema/table form endpoints; create/drop run through the command console.

- **Browse**: `Databases` returns `[]DBInfo{Name, Collections}` (collections sorted), surfaced by `apiExplorer`. `Indexes` returns `[]IndexInfo{Name, Keys, Unique}`. `Find` returns `DocPage{Docs []string, HasMore}` where each doc is pretty relaxed extended JSON; it parses relaxed extended JSON `filter`/`sort`/`projection` and fetches `limit + 1` to set `HasMore` without a count. Default page `size` is 50; `maxDocs = 1000`.
- **Command console**: `RunCommand`. Read-only users may run only commands in `readAllow` (`ReadAllowed(name)`): `find, aggregate, count, distinct, listcollections, listindexes, listdatabases, dbstats, collstats, connectionstatus, explain, getmore, ping, hello, ismaster, buildinfo, serverstatus`.
- **Dangerous commands** (`dangerous`, admin-only AND confirm-gated via `NeedsConfirm(name)`): `drop, dropdatabase, dropindexes, dropconnections, shutdown, fsync, killallsessions, killallsessionsbypattern, killop, killcursors, setparameter, setfeaturecompatibilityversion, replsetreconfig, replsetstepdown, logrotate, flushrouterconfig, mapreduce, eval` (the last two run arbitrary JS).
- **Server-side JS blocked for non-admins** (`serverJSKeys`): `$where`, `$function`, `$accumulator`. `UsesServerJS(docs...)` walks the filter/sort/projection JSON at any depth; an unparseable doc is treated as JS-free so `Find` surfaces the real parse error. The `mongo/docs` handler returns 403 when a non-admin uses any of these.

## Redis / Valkey (`internal/redisdb`)

A non-SQL vertical on `go-redis/v9` (`*redis.Client`), reached at `/api/c/{id}/redis/*`. There is no statement timeout in this package.

- **SCAN browse**: `Scan` (cursor + match pattern, default match `*`, default count 100) returns `KeyPage{Keys, Cursor}` with per-key Type + TTL. TTL strings: `-1` -> `"no expiry"`, `-2` -> `"-"`, else a truncated-to-second duration.
- **Type-aware viewers**: `Get` renders by type (string / hash / list / set / zset); collections are capped at **500**; type `none` returns an error; an unsupported type returns a hint to use the command console.
- **Command allowlist + FLUSH confirm**: read-only users may run only the handler-side `redisReadAllow` set (`get, mget, type, ttl, pttl, scan, hget, hgetall, hkeys, hlen, lrange, llen, smembers, scard, zrange, zcard, exists, strlen, info, dbsize, ping, memory, object`). The dangerous-command gate `reDangerous` (anchored, case-insensitive, matched against arg[0]) covers: `flushall, flushdb, shutdown, debug, eval, evalsha, eval_ro, evalsha_ro, fcall, fcall_ro, function, script, module, config, slaveof, replicaof, migrate, restore, swapdb, acl, cluster, failover, save, bgsave, bgrewriteaof, lastsave, reset, latency, monitor`. `NeedsConfirm(args)` returns true for these, and the handler treats them as admin-only AND confirm-gated (covering FLUSHALL/FLUSHDB flushes, EVAL/FUNCTION/FCALL scripting, MODULE load, CONFIG-based file writes, and replication takeover). `Command` runs arbitrary `c.Do(...)`; `ParseArgs` splits on whitespace (no quote handling).

## The `dbsql.Engine` / `Dialect` abstraction

`internal/dbsql/dbsql.go` defines the engine-neutral contract the web layer talks to. The web layer never imports a concrete driver package; it holds a `dbsql.Engine`. To avoid an import cycle, `dbsql` imports no engine package, and each engine copies its driver rows into the shared DTOs field-for-field.

**`Dialect`** is the pure (no I/O) half: identifier/literal quoting and the SQL string builders whose syntax differs per engine.

```go
type Dialect interface {
    QuoteIdent(s string) string
    QuoteLiteral(s string) string
    Qualified(schema, table string) string

    DropTableSQL(schema, table string) string
    TruncateSQL(schema, table string) string
    DropColumnSQL(schema, table, column string) string
    DropIndexSQL(schema, table, name string) string

    DropSchemaSQL(name string, cascade bool) []string
    AlterSchemaSQL(name, newName, owner string) []string
    AlterUserSQL(name, newName string, a RoleAttrs) []string
    DropUserSQL(name, host string) []string

    FormSQL(spec FormSpec) (stmts []string, action string, err error)
    IsServerSideExec(sql string) bool
    AtomicDDL() bool
}
```

**`Engine`** embeds `Dialect` and adds the live operations the SQL handlers call: introspection (`Schemas`, `DatabaseName`, `Columns`, `Indexes`, `Keys`, `Roles`), execution (`Query(ctx, sql, readOnly, schema)`, `BrowseWhere`, `Exec`, `ExecScript`), and generators (`GenSelect`, `GenInsert`, `GenUpdate`, `CreateTableDDL`, `TableComment`, `FindUsages`).

Each engine carries a compile-time assertion `var _ dbsql.Engine = (*Engine)(nil)` (postgres `engine.go`, mysql `mysql.go`, sqlite `sqlite.go`).

Shared DTOs include `Table` (`Kind: table|view|matview`), `Schema`, `Column`, `Index`, `Key` (`Type: primary|foreign|unique|check|other`), `Role`, `Usage`, `Result` (`Columns, Rows, IsSelect, RowsAffected, Command, Duration, Truncated`), `RoleAttrs`, and `FormSpec`. `Column.TypeText()` shortens Postgres type spellings (`character varying` -> `varchar`, etc.) and appends `" (auto increment)"` when `AutoInc`; `Column.Cat()` returns an icon category (`pk`/`num`/`text`/`time`/`bool`/`json`/`col`).

### Family constants and kind mapping

Family constants and the `kindFamily` map live in `internal/dbsql/dbsql.go` and mirror the SPA `internal/web/spa/src/dbkinds.ts` so the backend and the connection picker agree.

| Constant | Value | Kinds |
|---|---|---|
| `FamilyPostgres` | `"postgres"` | `postgres`, `cockroach`, `greenplum`, `redshift`, `yugabyte`, `timescale`, `aurorapg` |
| `FamilyMySQL` | `"mysql"` | `mysql`, `mariadb` |
| `FamilySQLite` | `"sqlite"` | `sqlite` |
| `FamilyMongo` | `"mongodb"` | `mongodb` |
| `FamilyRedis` | `"redis"` | `redis` |

`dbsql.Family(kind)` looks up the map and **defaults unknown kinds to `FamilyPostgres`** (the historical fallback), so an unmapped kind silently uses the Postgres engine. `store.Connection.Engine()` returns `dbsql.Family(c.Kind)`.

## Connection registry pooling model

`internal/conn/registry.go` holds lazy, idle-closing pools keyed by connection ID (`int64`), one map per engine: `pg` (`*pgxpool.Pool`), `mysql` and `sqlite` (`*sql.DB`), `redis` (`*redis.Client`), `mongo` (`*mongo.Client`).

- **Lazy open with cache**: each getter (`PG`, `MySQL`, `SQLite`, `Redis`, `Mongo`) checks the cache (bumping `lastUsed`), else decrypts the password, opens, and pings; on ping failure it closes the freshly opened pool/client and returns the error, so failed connects are never cached. Ping timeout is 5s for MySQL/SQLite/Redis/Mongo; Postgres uses `pool.Ping(ctx)`.
- **Pool sizing is shared**: `NewRegistry(box, pgMaxConns, sqliteDir)` falls back to **4** when `pgMaxConns <= 0`. `pgMaxConns` (env `DBM_PG_POOL_MAX_CONNS`, default 4) caps **Postgres, MySQL, AND SQLite** pools alike (`MaxConns` for pgx, `SetMaxOpenConns` for the others). The Postgres-flavored field name is shared. Redis and Mongo use their drivers' internal pools.
- **Idle close**: `idleTTL = 5 * time.Minute`. A `janitor` goroutine ticks every minute and closes any pool/client idle longer than `idleTTL` (Mongo closes via `client.Disconnect` with a 5s context). `Forget(id)` drops and closes the cached pool/client for one connection across all five maps (called after connection update/delete and per re-encrypt rewrite).
- **`Engine(ctx, c)` dispatch**: the single seam for all SQL ops, switching on `c.Engine()`: `FamilyMySQL` -> `mysql.New(db)`, `FamilySQLite` -> `sqlite.New(db)`, default (the Postgres family) -> `postgres.New(pool)`. Redis and Mongo are NOT routed through `Engine`; handlers call `reg.Redis(ctx, c)` / `reg.Mongo(ctx, c)` directly.
- **Never leak session state**: pools are shared and connections are reused, which is exactly why Postgres resets `default_transaction_read_only`/`search_path` per call, MySQL pins session safety via the DSN, and SQLite resets `query_only` on a fresh context.
- **Credential audit**: `password(c)` returns `""` for an empty `PasswordEnc`, else `box.Decrypt(...)`; on a successful decrypt it fires the `onCred` callback (registered via `OnCredentialAccess`), which emits the `cred_access` audit event.

DSN builders live on `store.Connection`: `DSN(pw)` (Postgres, default `sslmode=prefer`), `DSNMySQL(pw)` (pins charset/sql_mode/time_zone; admin `Options` win), `DSNMongo(pw)` (URL-encoded creds, options verbatim), and the package func `store.SQLiteDSN(path)`. Redis is built inline from Host/Port/Username (default `"default"`)/DBName (parsed as the DB number, default 0).

## Extending: adding an engine

Two recipes, depending on whether the engine speaks SQL. Both are verified against `internal/conn/registry.go` and `internal/dbsql/dbsql.go`. Neither touches auth, crypto, or the workbench shell.

### A. SQL-family engine (implements `dbsql.Engine`)

Fits `dbsql.Engine` and reuses the grid/console/doc/usages tabs for free. Mirror `internal/mysql` or `internal/sqlite`.

1. **`internal/<engine>/`** implementing `dbsql.Engine` (add the compile-time assertion `var _ dbsql.Engine = (*Engine)(nil)`).
2. **`internal/dbsql/dbsql.go`**: add `Family<X>` and its `kindFamily` entries.
3. **`internal/conn/registry.go`**: add a pool entry struct + map + getter, the idle-close in `janitor` and `Forget`, and a dispatch arm in `Engine()`.
4. **`internal/store/store.go`**: add a `DSN...` builder if the connection shape differs.
5. **`internal/web/api.go`** `apiTestConnection` switch + a `ping<X>` in `internal/web/handlers_workbench.go`.
6. **SPA `internal/web/spa/src/dbkinds.ts`** row (+ a brand icon in `icons.tsx`). No new tab is needed.

### B. Non-SQL vertical (own data model)

Has its own data model and does NOT touch `Engine()`. Mirror `internal/redisdb` or `internal/mongodb`.

1. **`internal/<engine>/`** browse/query/command helpers (not a `dbsql.Engine`).
2. **`internal/dbsql/dbsql.go`**: add `Family<X>` + kind mapping (the family constant lives here even for non-SQL engines).
3. **`internal/conn/registry.go`**: add a client entry + map + getter, plus its idle-close in `janitor` and `Forget`.
4. **`internal/store/store.go`**: add a `DSN...` builder.
5. **`internal/web/api_<engine>.go`**: handlers registered in `mountAPI`, an `apiExplorer` branch, and a `ping<X>`.
6. **SPA**: a `dbkinds.ts` row, a new `TabView` variant + tab component, an Explorer branch, a `Tabs.tsx` route, plus an icon + accent.

## See also

- [Architecture](architecture.md) - process bootstrap, router/middleware stack, request lifecycle, registry wiring.
- [API reference](api-reference.md) - the JSON route table, capability gates, and the `/pg/` engine-neutral prefix.
- [Frontend](frontend.md) - the SPA tab kinds, `dbkinds.ts` registry, and how engine accents/icons are wired.
- Repo root: [../README.md](../README.md), [../SECURITY.md](../SECURITY.md), [../.env.example](../.env.example), [../Makefile](../Makefile).
