# Frontend / SPA

verix-dbm ships a single React workbench baked into the Go binary: an IDE-style SPA that calls one JSON API and is served from the same process. This page is the working reference for engineers touching the React side under `internal/web/spa`.

## 1. Stack and where it lives

The SPA source tree is `internal/web/spa`. It is a Vite project that builds into `internal/web/spa/dist`, which the Go binary embeds via `go:embed` (see [section 3](#3-build-and-embedding)).

| Item | Value | Source |
|------|-------|--------|
| Framework | React `18.3.1` + `react-dom` `18.3.1` | `internal/web/spa/package.json` |
| Language | TypeScript `5.9.3` | `internal/web/spa/package.json` |
| Bundler | Vite `5.4.21` + `@vitejs/plugin-react` `4.7.0` | `internal/web/spa/vite.config.ts` |
| UI icons | `lucide-react` `1.17.0` | `internal/web/spa/src/icons.tsx` |
| Brand logos | `react-icons` `5.6.0` (`si`/`fa` families) | `internal/web/spa/src/icons.tsx` |
| Theme | custom HUD theme `src/styles/hud.css`, imported in `main.tsx` | `internal/web/spa/src/main.tsx` |
| Entry | `main.tsx` mounts `<App/>` into `#root` inside `<StrictMode>` via `createRoot` | `internal/web/spa/src/main.tsx` |

Package metadata: name `verix-dbm-spa`, `"type": "module"`, `"private": true`. npm scripts: `dev` = `vite`, `build` = `tsc -b && vite build`, `preview` = `vite preview`.

Icons are centralized in `internal/web/spa/src/icons.tsx`: it re-exports Lucide glyphs, exposes `<Ico name>` for type/category icons, and a `BRAND` map for engine logos. Postgres / Cockroach / Timescale / MySQL / MariaDB / SQLite / MongoDB / Redis use Simple Icons (`si`) logos; Redshift and Aurora/RDS Postgres borrow `FaAws`; Greenplum and YugabyteDB fall back to the generic `Database` cylinder. An unknown `Ico` name resolves to `Circle`.

## 2. The workbench layout

The shell ([`src/App.tsx`](../internal/web/spa/src/App.tsx)) renders a `<header class="topbar">` (brand `VERIXDBM`, nav, userbox) above `<main class="container"><div class="ide">`, which holds two panes: the **Explorer** tree on the left and the **Tabs** workspace on the right.

- **Topbar nav**: `Connections` is always shown; `Audit` and `Re-encrypt` appear only when `me.user.admin`. The userbox shows `me.user.name` plus a role suffix (` · ADMIN` / ` · WRITE` / ` · READ`). Logout is a `<form method="post" action="/auth/logout">` with a hidden `csrf` field (POST + CSRF only, deliberately not a GET link).
- **Boot guard**: while `me` is `null` the app renders only `loading…`; nothing else mounts until `GET /api/me` resolves.
- **Tabs stay mounted.** This is the core UX invariant: in [`components/Tabs.tsx`](../internal/web/spa/src/components/Tabs.tsx) every tab is always rendered and inactive ones are hidden with inline `style={{ display: active ? 'flex' : 'none' }}`. Switching tabs never unmounts them, so console text, grid filters, sort state, queued edits, and scroll position all survive. Do not "optimize" this into conditional rendering: it would discard tab state.
- **Engine accent tint**: a `useEffect` in `App.tsx` sets `document.documentElement.dataset.accent` from the active tab's connection engine: `redis`/`mongodb` -> `emerald`, `mysql` -> `amber`, `sqlite` -> `violet`, else (Postgres family) -> `cyan` (also the default when no connection is active).

### Mobile / off-canvas drawer

The Explorer collapses to an off-canvas drawer on small screens. App state holds a `drawer` boolean; `<Explorer open={drawer}>` gets the `.explorer.open` class and a `.drawer-backdrop` (click to close) renders while open. The `Tabs` tab bar pins a `drawer-toggle` (`PanelLeft` icon) that flips `drawer`, and `openTab` auto-closes the drawer after opening a tab. All context menus (`ContextMenu`, `CellMenu`, `TabMenu`) switch from a floating menu to a full-width bottom `.sheet` under `(max-width: 900px)`. Note: the media check is read at render time via `window.matchMedia` and is not reactive to live resizes.

## 3. Build and embedding

### Vite config (`internal/web/spa/vite.config.ts`)

```ts
export default defineConfig({
  base: '/app/',
  plugins: [react()],
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/c': 'http://localhost:8080',
    },
  },
})
```

- `base: '/app/'`: every asset URL resolves under the `/app` mount, so the embedded SPA works without any rewrite.
- `build.outDir: 'dist'` with `emptyOutDir: true`: output lands in `internal/web/spa/dist` and is wiped on each build. `dist/` is gitignored.
- Asset filenames are **content-hashed** by Vite, which is what makes the immutable cache header in the Go handler safe.
- Dev proxy forwards `/api` and `/c` to the Go server on `:8080`. Note the second prefix is `/c`, not `/api/c`: the CSV/JSON export download route lives at `GET /c/{id}/export` outside `/api`.

### Embedding and serving (`internal/web/spa.go`)

The built tree is embedded with `//go:embed all:spa/dist` into `var spaAssets embed.FS`, then `fs.Sub(spaAssets, "spa/dist")`. At startup `spaHandler()` reads `index.html`; if `spa/dist` or `index.html` is missing it calls `log.Fatalf("spa: index.html missing (run the SPA build): ...")`. This is fail-closed: the binary will not boot without a built SPA, so the SPA build is a hard prerequisite of `go build` / `go run`.

Routing inside the handler (after stripping the `/app` prefix and a leading `/`):

| Request path | Served | Cache-Control |
|--------------|--------|---------------|
| `""` or `index.html` | `index.html` | `no-cache` |
| unknown path (`sub.Open` errors) | `index.html` (client-side routing fallback) | `no-cache` |
| a directory (e.g. `/app/assets`) | `index.html` (directory listing suppressed) | `no-cache` |
| a real file under `assets/` | the file via `http.FileServer` | `public, max-age=31536000, immutable` |
| any other real file | the file via `http.FileServer` | (none set) |

The SPA is reachable at both `/` (which 302-redirects to `/app`) and `/app` + `/app/*`. Both `/app` and `/app/*` go through `spaHandler()`. See [Architecture](architecture.md) for the full router and middleware stack.

## 4. The API client and shared app context

### `src/api.ts`

A thin typed `fetch` wrapper. Key behaviors:

- **Base path**: every endpoint is a literal string under `/api` (except the two blob downloads under `/c` and `/api/.../export`).
- **CSRF injection**: a module-level `let csrf = ''` holds the token. `setCSRF(token)` is called by `App` right after `me()` resolves (the token is returned by `GET /api/me` as `csrf`). The internal `req(method, url, body?)` sets header `X-CSRF-Token: <csrf>` for any non-`GET` method, sets `Content-Type: application/json` when a body is present, and always sends `credentials: 'same-origin'`.
- **Error / toast handling**: `req` reads the response text, JSON-parses it (falling back to `{ error: text }`), and on `!res.ok` throws `ApiError(data?.error || "<status> <statusText>")`. `ApiError extends Error`. Callers catch it and surface the message through `notify()` (toasts). Note that console/command endpoints (`query`, `redisCmd`, `mongoCmd`, `testConnection`) return errors at HTTP 200 with an `{ error }` body so the SPA renders them inline rather than as thrown errors; see [API Reference](api-reference.md).
- `qs(o)` builds a query string, skipping `undefined` and empty-string values and prefixing `?` only when non-empty.
- `ConnInput` shape: `{ name, kind, host, port, dbname, username, password?, copyFrom?, options, readOnly }`.

Two endpoints bypass `req()` because they are blob downloads via a synthetic `<a download>` click:
- `exportTable` (`GET /c/{id}/export?...`) is a GET that still sends `X-CSRF-Token` (the server checks CSRF on this GET). Filename `{schema}_{table}.{format}`, sanitized with `replace(/[^a-zA-Z0-9_.-]/g, '_')`.
- `auditExport` (`GET /api/audit/export?format=jsonl|csv`) is a GET with no CSRF header. Filename `audit.{format}`.

The full endpoint map lives in `api.ts`; see [section 5](#5-tab-kinds-and-routing) and [API Reference](api-reference.md) for the routes each function targets.

> **Gotcha:** all SQL-engine introspection and DDL routes use the literal `/pg/` URL segment (e.g. `/api/c/{id}/pg/columns`) even for MySQL and SQLite connections. The prefix is historical; the Go side dispatches by engine family, so never infer Postgres from the URL.

### App context (`src/appctx.ts`)

`AppContext = createContext<AppActions>(...)`, read via the `useApp()` hook. `App` builds a memoized `actions: AppActions` object that is the single imperative surface every component reaches, so components rarely thread props for shared behavior.

`AppActions` members include: `caps`, `conns`, `activeView`, `connById`, `openTab`, `closeTab`, `closeTabsFor`, `copy`, `notify`, `confirm`, `prompt`, `refreshConn`, `refreshToken`, `openCtx`, `openConnModal` (default kind `'postgres'`), `openEditModal`, `openDDL`, `openTableDesigner`, and `reloadConns`.

- `caps: Caps = { admin, write, csrf, scopedAccess }` is derived from `me` (each defaults to `false`/`''`). Components gate UI affordances off `caps`; the server still enforces.
- `copy()` uses `navigator.clipboard.writeText` and toasts `Copied` (no-op when the clipboard API is absent).
- `notify()` pushes a `Notice` that auto-removes after `3200` ms.
- `confirm(spec)` / `prompt(spec)` each return `Promise<string|null>` resolved by the single global `<Dialog>`. The confirm default button is `{ label: 'OK', value: 'ok', variant: 'cta' }`. Any dismissal (Esc, overlay click, Cancel, ×) resolves `null`.

Top-level App state (all `useState` unless noted) includes: `me`, `conns`, `tabs`, `active`, the context-menu state `ctx`, the modal flags `connModal` / `ddl` / `designer` / `auditOpen`, `drawer`, `notices`, `refreshTokens` (a `Record<number, number>` of per-connection refetch counters), and `dialog` (the single pending dialog plus its promise `resolve`). `noticeId` is a `useRef` counter. On mount, App calls `api.me()` (-> `setMe` + `setCSRF`) then `reloadConns()` (`api.listConnections()`).

Tab helpers on App: `openTab` dedupes by `t.key` (no duplicate tabs) and closes the drawer; `closeTab` falls back to the neighbour tab; plus `closeOthers`, `closeAll`, `closeRight`, and `closeTabsFor(pred)` (closes every tab whose `view` matches a predicate, used when the underlying object is dropped). `refreshConn(id)` bumps `refreshTokens[id]`; `refreshToken(id)` reads it, used as a refetch dependency for a connection's subtree and roles.

Important shared types in `appctx.ts`:
- `NodePayload.type`: `'conn' | 'schema' | 'table' | 'col' | 'key' | 'index' | 'roles' | 'role' | 'mongo-db' | 'mongo-coll'`.
- `TabView` union: `grid`, `console` (`sql?`, `schema?`), `redis`, `mongo` (`db`, `coll`), `doc`, `usages`. Every variant carries `connId`.
- `TabDef = { key, title, icon: 'grid' | 'console', view }`.
- `DDLKind`: `add-column | modify-column | rename-table | new-schema | new-table | new-index | create-user | alter-schema | alter-user`.

The DTO types the API returns are mirrored in `src/types.ts` (e.g. `Me`, `Connection`, `Schema`, `Table`, `Column`, `Key`, `Index`, `QueryResult`, `GridResponse`, `RedisKeysResponse`, `MongoDocsResponse`, `AuditRow`). Keep `types.ts` in sync with the DTO structs in `internal/web/api.go`.

## 5. Tab kinds and routing

[`components/Tabs.tsx`](../internal/web/spa/src/components/Tabs.tsx) owns the tab bar and the workspace. The tab bar renders the `drawer-toggle`, then one `.tab` per open tab (icon is `Terminal` when `t.icon === 'console'`, else `Table2`), each with a close `X`. When there are no tabs it shows a `.tab-hint` plus a `.welcome` box.

`TabContent` maps a `TabView` to a component by `v.type`:

| `view.type` | Component | What the tab does |
|-------------|-----------|-------------------|
| `grid` | `GridTab` | Paginated table browse with WHERE / ORDER BY, per-column sort, inline add/edit/delete, transaction modes, row selection, data extractors, and a cell inspector. |
| `console` | `RedisTab` if `connById(v.connId)?.kind === 'redis'`, else `ConsoleTab` | SQL console (or Redis console when the connection is Redis). |
| `redis` | `RedisTab` | Keyspace browser + read-only-aware command console. |
| `mongo` | `MongoTab` | Per-collection document browser + command console. |
| `doc` | `DocTab` | Read-only "Quick documentation": columns, keys, indexes, comment. |
| `usages` | `UsagesTab` | Inbound foreign keys that reference the table. |

Tabs are keyed by deterministic strings so reopening the same object reuses its tab: `grid:{id}:{schema}.{table}`, `console:{id}` (or `console:{id}:{schema}`), `doc:...`, `usages:...`, `query:...`, `mongo:{id}:{db}.{coll}`, and the seeded delete console `console:{id}:delete:{schema}.{table}`.

Right-clicking a tab opens `TabMenu` (Close, Close Other Tabs, Close Tabs to the Right, Close All Tabs, Copy Name, and Copy Qualified Name when the view has a `table`). `Close`'s hint key is `Ctrl+F4`. Below `(max-width: 900px)` the menu renders as a bottom `.sheet`.

> **Gotcha:** a connection-level console opened on a Redis connection routes through `RedisTab`, not `ConsoleTab`. The decision happens in `Tabs.TabContent` based on `conn.kind === 'redis'`.

### Tab components in detail

- **`tabs/GridTab.tsx`** - Browse with `CodeField` autocomplete for WHERE/ORDER BY and full-text search. Page size is persisted in `localStorage["verix.grid.pageSize"]` (`PAGE_SIZES = [10,100,250,500,1000]`, default `100`; 1000 is the honest max because the server caps results at 1000 rows). Pagination is COUNT-free: `hasNext = rows.length === size`. `readOnly` resolves to `data?.readOnly ?? (conn ? conn.readOnly || !app.caps.write : true)` (fail-closed). Identifier quoting is engine-aware (backticks for MySQL, double quotes otherwise). Transaction modes (`TxMode = 'auto' | 'manual'`): manual queues `pendingEdits` / `pendingDeletes` / `pendingInserts` and commits atomically via `api.execTx`; auto-mode seeds runnable SQL (inserts/edits run with `confirm=true`, deletes open a seeded console tab so the destructive gate applies). Data extractors (`localStorage["verix.grid.extractor"]`, default `sql-inserts`) cover SQL / delimited / structured formats. The `InspectorPanel` adds Record / Value / Aggregates views.
- **`tabs/ConsoleTab.tsx`** - SQL console using `CodeField` (a textarea with a `highlightSQL` overlay). `readOnly = conn ? conn.readOnly || !app.caps.write : false`. Run with the button or `Ctrl/Cmd+Enter`. Autocomplete pool is table names (+ `schema.table` for non-`public`), columns of tables referenced after FROM/JOIN/UPDATE/INTO (lazily fetched and cached), and `SQL_KEYWORDS`. When the server replies `needConfirm`, it shows the SQL with a danger "Yes, run it" button that re-runs with `confirm=true`. Results render in `ResultTable`.
- **`tabs/DocTab.tsx`** - Loads `api.doc` and renders `schema.table`, comment, a Columns table (PK badge, type, nullable, default), Keys, and Indexes. Read-only.
- **`tabs/UsagesTab.tsx`** - Loads `api.usages` and lists referencing table / constraint / definition, or "No foreign keys reference this table."
- **`tabs/RedisTab.tsx`** - SCAN keyspace browser (MATCH defaults to `*`, cursor paging via "more…", match input debounced 300 ms), type-aware value viewer (hash/zset -> table, list/set -> list, else text), and a read-only-aware command console with a confirm gate.
- **`tabs/MongoTab.tsx`** - Document browser with JSON FILTER / SORT / FIELDS inputs and skip-based paging (fixed `size = 50`), JSON-highlighted docs, an inline insert editor that sends a verbatim `{insert, documents}` command (so extended JSON is parsed server-side and `writeErrors` surface), and a command console in a `<details>`. Talks only to `/api/c/{id}/mongo/*`.

## 6. Component catalog

| Component | File | Role |
|-----------|------|------|
| `Explorer` | `components/Explorer.tsx` | Lazy `<details>` tree of connections -> schemas/databases -> tables/collections -> columns/keys/indexes (+ roles folder). |
| `ConnModal` | `components/ConnModal.tsx` | Create/edit connection ("Data Sources & Drivers") with URL-paste autofill and Test Connection. |
| `DDLModal` | `components/DDLModal.tsx` | Form-backed DDL dialog for the `DDLKind`s (add/modify column, rename table, new schema/table/index, create/alter user, alter schema). |
| `AuditModal` | `components/AuditModal.tsx` | Admin-only overlay of the last 200 audit rows with JSONL/CSV export. |
| `GrantsPanel` | `components/GrantsPanel.tsx` | Per-connection grant management (subject + read/write level). |
| `TableDesigner` | `components/TableDesigner.tsx` | Visual table create/modify with a column/key/FK/index/check tree and live SQL preview. |
| `ContextMenu` | `components/ContextMenu.tsx` | Right-click menu built from a `NodePayload` + caps, with engine-aware item gating. |
| `Dialog` | `components/Dialog.tsx` | The single global HUD confirm/prompt that backs `app.confirm` / `app.prompt`. |
| `Toasts` | `components/Toasts.tsx` | Bottom-right toast stack rendering `Notice`s from `notify()`. |
| `ResultTable` | `components/ResultTable.tsx` | Renders a SELECT grid or a command summary, appending `· truncated at 1000` when truncated. |
| `Autocomplete` / `CodeField` | `components/Autocomplete.tsx` | Dependency-free autocomplete for input/textarea, with an optional syntax-highlight overlay (`CodeField`). |

Notable details:

- **Explorer** lazy-loads `api.explorer(id)` on first expand and refetches on a `refreshToken` bump. It branches on `data.kind`: `redis` -> a single `keyspace` console leaf, `mongodb` -> `MongoTree`, else `SchemaList` + (admin) a `RolesNode`. The admin-only `+` button opens `NewSourceMenu` listing every `dbkinds.ts` row. Connection rows are tinted by `nameColor` (a deterministic FNV-1a hash of the name -> `hsl(... 70% 66%)`), show a read-only dot, a console button (hidden for MongoDB), an admin delete button, and a `Kebab` that opens the same context menu. The active tab auto-reveals its connection/schema.
- **ConnModal** autofills from a pasted URL via `parseConnUrl` (matched by scheme through `dbKindByScheme`). For file-based kinds (SQLite) it hides host/port/user/password/options/URL and shows a single "File path (server path under `DBM_SQLITE_DIR`)" field. Edit mode loads with `password: ''` (placeholder `•••• unchanged`); "Save as copy" calls `createConnection` with `password: ''` + `copyFrom: editId` so the server reuses stored ciphertext (the password never reaches the browser). `GrantsPanel` is embedded only when `mode === 'edit' && caps.admin && caps.scopedAccess`.
- **ContextMenu** gates items by engine to avoid invalid requests: SQL DDL items (New schema/table, Create user) only for `postgres`/`mysql` engines; the conn-level Query console is hidden for `mongodb`; schema rename/owner is hidden for `mysql`. MongoDB has no DDL, so create/drop database/collection run through `api.mongoCmd` (`{create}`, `{dropDatabase:1}`, `{drop}` with `confirm = true`). Drop actions route through a danger confirm and, on success, `closeTabsFor` the affected object and `refreshConn`.
- **TableDesigner** uses `tableModel.ts` helpers (`emptyModel`, `generateCreate`, `generateModify`, `loadModel`, `uid`). `create` audits `sql_ddl_create_table`; `modify` loads via `api.doc` + `loadModel`, diffs, and audits `sql_ddl_modify_table`; both apply via `api.applyTable`. It warns when the resulting MySQL batch is multi-statement (MySQL DDL is non-transactional).
- **Dialog** maps button `variant` to a class: `danger` -> `btn-danger`, `accent` -> `hud-btn-accent`, else `hud-btn-cta`. Dismissal resolves `null`.
- **ResultTable** prints a command summary as `{command} · {rowsAffected} rows affected · {duration}`.
- **Autocomplete** ranks prefix matches above interior, caps at 50, and supports ArrowUp/Down, Enter/Tab (accept), Esc (close), and `Ctrl/Cmd+Space` (force-open). `qIdent(name)` quotes only names that are not `/^[a-z_][a-z0-9_]*$/`. `CodeField` paints a `<pre>` overlay behind a transparent textarea and computes caret coordinates with an off-screen mirror div.

## 7. The `dbkinds.ts` registry

[`src/dbkinds.ts`](../internal/web/spa/src/dbkinds.ts) is the single source of truth on the SPA side for the database kinds the UI offers. It mirrors the engine-family mapping in `internal/dbsql/dbsql.go` (see [Database Engines](database-engines.md)).

`Engine = 'postgres' | 'mysql' | 'sqlite' | 'mongodb' | 'redis'`. Each row is a `DbKind`:

```ts
type DbKind = {
  id: string          // saved as Connection.kind
  label: string       // shown in pickers
  engine: Engine      // drives quoting, icons, accent, gating
  defaultPort: number // prefilled in ConnModal
  schemes: string[]   // matched on URL paste
  fileBased?: boolean // SQLite: file path instead of host/port
}
```

| id | label | engine | defaultPort | schemes |
|----|-------|--------|-------------|---------|
| `postgres` | PostgreSQL | postgres | 5432 | postgresql, postgres, pg |
| `cockroach` | CockroachDB | postgres | 26257 | cockroachdb, cockroach, crdb |
| `greenplum` | Greenplum | postgres | 5432 | greenplum, gpdb |
| `redshift` | Amazon Redshift | postgres | 5439 | redshift |
| `yugabyte` | YugabyteDB | postgres | 5433 | yugabytedb, yugabyte, ysql |
| `timescale` | TimescaleDB | postgres | 5432 | timescaledb, timescale |
| `aurorapg` | Aurora / RDS Postgres | postgres | 5432 | aurorapg |
| `mysql` | MySQL | mysql | 3306 | mysql |
| `mariadb` | MariaDB | mysql | 3306 | mariadb, maria |
| `sqlite` | SQLite | sqlite | 0 | sqlite, file (`fileBased: true`) |
| `mongodb` | MongoDB | mongodb | 27017 | mongodb, mongodb+srv, mongo |
| `redis` | Redis / Valkey | redis | 6379 | redis, rediss, valkey |

Array order is the picker order. Helpers: `DEFAULT_KIND = 'postgres'`, `isFileBased(id)`, `dbKind(id)`, `dbKindByScheme(scheme)` (lowercased), `defaultPort(id)` (fallback 5432), `kindLabel(id)` (fallback id), `kindEngine(id)` (fallback `'postgres'`).

A row drives the UI per engine: the `+` source menu and ConnModal picker (label + default port + file-based field), URL-paste autofill (schemes), the brand icon and accent tint (engine), identifier quoting in grid/console (engine), and context-menu item gating (engine). Adding a new SQL kind that reuses the existing engine is usually just a new row here plus a brand icon; see the engine-adding recipes in [Architecture](architecture.md) and [Database Engines](database-engines.md).

## 8. Running the dev server and building for production

### Local development (two processes)

Run the Go backend in dev mode (auto-logged-in as Dev Admin, no Keycloak), then the Vite dev server, which proxies `/api` and `/c` to the backend:

```bash
DBM_DEV_MODE=true go run ./cmd/server            # backend on :8080
cd internal/web/spa && npm install && npm run dev # Vite dev server (proxies /api + /c)
```

Open the Vite dev URL; hot module reload covers the SPA while the proxy forwards API calls to `:8080`. (The Go server's own `/app` mount serves the last built `dist/`, not the live Vite output, so use the Vite URL for active SPA work.)

### Production build

The SPA must be built before the Go binary so `go:embed` can pick it up. Use the Makefile target (it builds the SPA then the static binary):

```bash
make spa     # npm ci + vite build -> internal/web/spa/dist
make build   # make spa, then CGO_ENABLED=0 go build -o bin/verix-dbm ./cmd/server
```

`make spa` runs `npm --prefix internal/web/spa ci` then `npm --prefix internal/web/spa run build`. Building the binary without a populated `internal/web/spa/dist` fails fast at startup (`log.Fatalf`). Docker and CI build the SPA exactly once and share it (CI hands the `spa-dist` artifact to the binaries job); see [Deployment](deployment.md). For a one-off SPA-only build outside the Makefile, run `npm --prefix internal/web/spa run build`.

## 9. Related docs

- [Architecture](architecture.md) - process bootstrap, the chi router and middleware stack, the embedded-SPA handler, and the connection registry.
- [API Reference](api-reference.md) - the full JSON endpoint table the `api.ts` client targets, capability gates, and CSRF rules.
- [Database Engines](database-engines.md) - the engine families behind the shared `/pg/` routes and the engine-adding recipes that `dbkinds.ts` participates in.
- Repo root: [../README.md](../README.md), [../SECURITY.md](../SECURITY.md), [../.env.example](../.env.example), [../Makefile](../Makefile).
