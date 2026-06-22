# Development and contributing

This page is the contributor's guide to verix-dbm: how the repository is laid out, how to run the app locally, the Make targets and tests, what CI does on pull requests and releases, the coding conventions enforced in this codebase, and how to add a new database engine.

## Repository layout

verix-dbm is a single static Go binary with a React workbench baked in. One process serves the JSON API and the embedded SPA. The backend lives under `internal/`, the frontend under `internal/web/spa`, and the entrypoint under `cmd/`.

| Path | Responsibility |
|------|----------------|
| [cmd/server](../cmd/server) | Process entrypoint: config load, wiring, `http.Server`, graceful shutdown. |
| [internal/config](../internal/config) | Env config (`Load()` -> `*Config`); fail-closed validation. |
| [internal/crypto](../internal/crypto) | AES-256-GCM credential encryption, versioned keyring, rotation, `Provider` seam. |
| [internal/store](../internal/store) | Metadata store (saved connections, per-connection grants, audit log) over SQLite or Postgres. |
| [internal/conn](../internal/conn) | Pooled connection registry: lazy, idle-closing pools keyed by connection ID. |
| [internal/dbsql](../internal/dbsql) | Engine-neutral SQL interface (`Dialect` + `Engine`) shared by the SQL engines. |
| [internal/postgres](../internal/postgres) | pgx v5 introspection / query / DDL / code generators (`dbsql.Engine`). |
| [internal/mysql](../internal/mysql) | go-sql-driver MySQL/MariaDB introspection / query / DDL (`dbsql.Engine`). |
| [internal/sqlite](../internal/sqlite) | modernc.org/sqlite introspection / query / DDL over a file (`dbsql.Engine`). |
| [internal/redisdb](../internal/redisdb) | go-redis SCAN browse, value viewers, read-only command console (non-SQL). |
| [internal/mongodb](../internal/mongodb) | mongo-driver databases / collections / find / command console (non-SQL). |
| [internal/auth](../internal/auth) | OIDC login, sessions (memory/redis), RBAC, per-connection grants, CSRF. |
| [internal/web](../internal/web) | chi router, JSON API (`api_*.go`), security headers, SSRF egress guard, rate limit, observability. |
| [internal/web/spa](../internal/web/spa) | React + TypeScript + Vite workbench, embedded via `go:embed`. |

`internal/web` stays a single flat package: handlers are methods on the unexported `*Server`, grouped by file (`api_*.go` for the JSON surface, `handlers_*.go` for shared helpers). See [Architecture](architecture.md) for how these wire together at startup and per request.

## Local development workflow

There are two ways to work, depending on whether you are iterating on the SPA.

### DEV mode (backend + Vite dev server)

For fast frontend iteration, run the Go backend in DEV mode and the Vite dev server side by side. The Vite server proxies API calls to the backend, so you do not have to rebuild and re-embed the SPA on every change.

```bash
# Terminal 1: backend on :8080, auto-logged-in as a local admin (no Keycloak)
DBM_DEV_MODE=true go run ./cmd/server

# Terminal 2: Vite dev server with hot reload
cd internal/web/spa && npm install && npm run dev
```

Key facts:

- `DBM_DEV_MODE=true` bypasses OIDC and auto-logs every request in as a local admin (`User{Name:"Dev Admin", Email:"dev@localhost", Admin/Write/Read:true}`). It also relaxes the SSRF egress guard the same way `DBM_ALLOW_LOCAL_TARGETS=true` does (the guard's `allowLocal` is `cfg.AllowLocalTargets || cfg.DevMode` in [internal/web/api_connections.go](../internal/web/api_connections.go)), so you can connect to `localhost` databases. `Load()` returns early for dev mode, skipping the production OIDC and encryption-key checks, and an empty key makes the crypto layer mint an ephemeral random key, so saved credentials are not readable after a restart. Never set `DBM_DEV_MODE` on an internet-reachable deployment.
- The Vite config ([internal/web/spa/vite.config.ts](../internal/web/spa/vite.config.ts)) proxies `/api -> http://localhost:8080` and `/c -> http://localhost:8080` (note `/c`, not `/api/c`: the table export download lives at `GET /c/{id}/export`).
- In production the app refuses to start without OIDC and a persistent encryption key. See [Configuration](configuration.md) for the full env reference and the fail-closed rules.

### Building the embedded binary

The Go binary embeds the built SPA via `//go:embed all:spa/dist` in [internal/web/spa.go](../internal/web/spa.go). The SPA must be built before `go build` / `go run`, or the binary will refuse to boot (`log.Fatalf` if `spa/dist/index.html` is missing). The simplest path is the `build` target:

```bash
make build          # npm ci + vite build, then the static Go binary -> bin/verix-dbm
./bin/verix-dbm     # serves the JSON API and the SPA at /app
```

`make build` produces a fully static binary (`CGO_ENABLED=0`, pure-Go SQLite via `modernc.org/sqlite`). The root `/` redirects (302) to `/app`, where the embedded SPA is served. Assets under `assets/` get a one-year immutable cache header (Vite content-hashed filenames); `index.html` is served `no-cache`.

If you only need to refresh the embedded frontend without a full backend build, run `make spa`.

## Make targets

All targets live in the [Makefile](../Makefile).

| Target | Command | Notes |
|--------|---------|-------|
| `make build` | depends on `spa`, then `CGO_ENABLED=0 go build -o bin/verix-dbm ./cmd/server` | Builds the SPA first so it embeds, then the static binary. Output: `bin/verix-dbm`. No `-trimpath`/`-ldflags` (unlike the Dockerfile and CI release build). |
| `make spa` | `npm --prefix internal/web/spa ci` then `npm --prefix internal/web/spa run build` | Builds only the embedded React SPA into `internal/web/spa/dist`. `npm run build` runs `tsc -b && vite build` (typecheck + bundle). |
| `make run` | `go run ./cmd/server` | Needs the SPA already built for `/app`, or run with `DBM_DEV_MODE=true` plus the Vite dev server. |
| `make vet` | `go vet ./...` | Static analysis. |
| `make test` | `go test ./...` | Runs the full Go test suite. |
| `make vuln` | `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` | Dependency vulnerability scan. Uses `@latest` (the same invocation CI runs). |
| `make tidy` | `go mod tidy` | Reconcile `go.mod` / `go.sum`. |

### Running a single Go test

To run one test (or a regex of tests) in a package, pass `-run` to `go test`:

```bash
go test ./internal/web -run TestName -v
```

`-run` takes a regex, so `-run TestApi` matches every test whose name starts with `TestApi`. Drop `-v` for quieter output. The same form works for any package, for example `go test ./internal/crypto -run TestRotate -v`.

## Testing overview

Tests are standard Go `_test.go` files colocated with the package they exercise; run them all with `make test` (`go test ./...`). The pure, side-effect-free seams are the most heavily unit-tested because they are deterministic and need no live database:

- `internal/dbsql` - the engine-neutral dialect and the destructive-statement gate (`dbsql.NeedsConfirm`), the server-side-exec screen, identifier/literal quoting, and `FormSQL` DDL builders.
- `internal/crypto` - AES-GCM encrypt/decrypt round-trips, the versioned keyring, key parsing (hex vs base64), and `Reencrypt` rotation behavior.
- `internal/store` - placeholder rebinding (`?` -> `$N` for Postgres), grant resolution (`GrantForSubjects` highest-wins), DSN builders, and the SQLite path allowlist (`ResolveSQLitePath` containment and symlink checks).
- `internal/web` / `internal/auth` - the access-resolution logic (`ResolveConnAccess`, designed to be pure and unit-testable), CSRF, rate limiting, audit redaction (`auditDetail`), CSV formula-injection neutralizing (`csvSafe`), and the SSRF egress guard (`blockedEgressIP` / `guardEgressHost`).

The frontend is typechecked rather than unit-tested: `npm run build` runs `tsc -b` first, so a type error fails the SPA build (and therefore CI). When you change query, console, or export paths, preserve the shared guardrails: the 30s statement timeout, the 1000-row result cap, and the confirmation gate for destructive statements. See [Database engines](database-engines.md) for exactly how each engine enforces these.

## CI pipeline

CI lives in [.github/workflows/ci.yml](../.github/workflows/ci.yml) (`name: ci`). It is one workflow covering both pull-request checks and releases, with three jobs that chain: `ci` -> `tag` -> `binaries`.

### Triggers

- `push` to `main` and `push` of any `v*` tag.
- `pull_request` (CI checks only).
- `workflow_dispatch` (manual) with an optional `version` input ("Exact version tag, e.g. v1.2.0; leave blank to auto-bump the patch").

### Job `ci` (every trigger)

Runs on `ubuntu-latest`, sets up Go `1.26` (`check-latest: true`) and Node `20` (npm cache keyed on `internal/web/spa/package-lock.json`), then:

1. `npm --prefix internal/web/spa ci` - install SPA deps.
2. `npm --prefix internal/web/spa run build` - typecheck + build the SPA.
3. `npm --prefix internal/web/spa audit --audit-level=high` - non-blocking (`continue-on-error: true`).
4. Upload the built `internal/web/spa/dist` as artifact `spa-dist` (retention 1 day) so the release job never rebuilds it.
5. `go vet ./...`
6. `go test ./...`
7. `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`

These same checks (SPA build, `go vet`, `go test`, govulncheck) are what gate every pull request. The `tag` and `binaries` jobs `needs: ci`, so a failing CI blocks any release.

### Release flow

Releases happen only on non-PR triggers; pull requests run `ci` alone.

Job `tag` (`needs: ci`, skipped on `pull_request`, escalates to `permissions: contents: write`) resolves the version and, when needed, creates and pushes the tag:

- A directly pushed `v*` tag is used as-is (`release=true`, nothing is created).
- A `workflow_dispatch` with an explicit `version` uses it verbatim.
- Otherwise the version auto-bumps from the latest `v*` tag, controlled by directives in the head commit message (case-insensitive):
  - `#major` -> bump major, zero minor and patch.
  - `#minor` -> bump minor, zero patch.
  - `#norelease` (or `#skip-release` / `#skip release`) -> skip the release for that push (`release=false`).
  - no directive -> bump the patch.
  - If no `v*` tag exists yet, the first release is `v0.1.0`.
- The computed version is validated against the semver regex `^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`, and the job errors if that tag already exists on origin. The tag is pushed by `github-actions[bot]` as an annotated tag.

Job `binaries` (`needs: tag`, runs only when `needs.tag.outputs.release == 'true'`, `permissions: contents: write`):

- Checks out at the resolved tag (full history for git-cliff) and restores the `spa-dist` artifact into `internal/web/spa/dist` (the SPA is built once in `ci`, never here).
- Cross-compiles with `CGO_ENABLED=0` for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64` using `go build -trimpath -ldflags="-s -w"`. Each target is packaged as `verix-dbm_${VERSION}_${os}_${arch}.tar.gz`, and a `checksums.txt` (`sha256sum *.tar.gz`) is written.
- Generates categorized release notes (Features / Bug fixes / Documentation / Chores) with `git-cliff` from the tag's conventional-commit history (config `cliff.toml`).
- Creates a GitHub Release (`softprops/action-gh-release`) for the tag, uploading all tarballs plus `checksums.txt`, with the changelog as the body. Any version containing a `-suffix` is flagged `prerelease`.

The binaries are pure-Go static, so all four targets cross-compile natively on the Linux runner. No Docker image is published by this workflow: only the tag, the GitHub Release, and the tarballs.

### Required workflow permissions and manual tagging

The top-level workflow grants `permissions: contents: read`; only the `tag` and `binaries` jobs escalate to `contents: write`. For the bot to push tags, the repository must also allow it under **Settings -> Actions -> General -> Workflow permissions -> "Read and write"**; otherwise the tag push fails with 403 regardless of the workflow file. To cut a release by hand, either push a `v*` tag directly, or run the workflow via **workflow_dispatch** and type an exact `version` (blank auto-bumps the patch).

## Coding conventions

These conventions come from [../CLAUDE.md](../CLAUDE.md) and the structure of the codebase. Please follow them in code, comments, docs, and commit messages.

### No em dash

Never use an em dash (the long dash character, U+2014) anywhere: code, comments, docs, commit messages, or chat. Use a regular hyphen, a colon, or reword the sentence instead.

### Commit message style

Write commit subjects like a human would: one concise line of roughly 50 characters, imperative mood, lowercase, no trailing period, and no Claude or co-author attribution. Add a short body only if the change genuinely needs explaining. For example:

```
fix sticky tab bar on mobile
```

```
add greenplum kind to dbkinds registry

reuses the postgres engine; only the picker row and
default port differ.
```

Conventional-commit prefixes (`feat:`, `fix:`, `docs:`, `chore:`) are useful because release notes are categorized from commit history via git-cliff, but the human, lowercase, imperative style above is the house rule.

### One flat `internal/web` package

`internal/web` is intentionally a single flat package. Handlers are methods on the unexported `*Server`, grouped by file: `api_*.go` for the JSON surface, `handlers_*.go` for shared helpers (ping probes in `handlers_workbench.go`, CSV/JSON export in `handlers_export.go`). A new engine adds one `api_<engine>.go`, not a subpackage. Do not introduce subpackages under `internal/web`.

### Engine packaging rules

Each database engine is its own package behind the shared pooled connection registry. SQL-family engines implement the engine-neutral `dbsql.Engine` interface (with a compile-time `var _ dbsql.Engine = (*Engine)(nil)` assertion) and reuse the grid/console/doc/usages workbench for free. Non-SQL verticals (Redis, Mongo) carry their own data model and UI tab and are reached through dedicated registry getters, not through `Engine()`. Adding an engine means a new package plus its API/UI wiring, without touching auth, crypto, or the workbench shell.

## Adding a new database engine

There are two recipes depending on whether the engine speaks SQL:

- A **SQL-family engine** (fits `dbsql.Engine`; mirror [internal/mysql](../internal/mysql) or [internal/sqlite](../internal/sqlite)) reuses the grid/console/doc/usages tabs. You add the engine package, a `Family<X>` + kind mapping in [internal/dbsql/dbsql.go](../internal/dbsql/dbsql.go), a pool entry plus a dispatch arm in `Engine()` in [internal/conn/registry.go](../internal/conn/registry.go), an `apiTestConnection` switch arm and a `ping<X>`, and a row in the SPA `dbkinds.ts`. No new tab is needed.
- A **non-SQL vertical** (its own data model; mirror [internal/redisdb](../internal/redisdb) or [internal/mongodb](../internal/mongodb)) does NOT touch `Engine()`. You add the engine package, the family/kind mapping, a client getter plus idle-close in the registry, an `api_<engine>.go` with handlers and an `apiExplorer` branch, and the full SPA tab wiring (a new `TabView` variant, tab component, Explorer branch, and `Tabs.tsx` route).

The full step-by-step recipe for both paths, including the exact files to edit and the gotchas (shared pool sizing, read-only enforcement per engine, the `pg/` URL prefix being engine-neutral), is in [Database engines](database-engines.md).

## See also

- [Architecture](architecture.md) - process bootstrap, router and middleware stack, the embedded SPA, and the connection registry lifecycle.
- [Database engines](database-engines.md) - the engine interfaces, per-engine specifics, shared guardrails, and the add-an-engine recipe.
- [Configuration](configuration.md) - the full env var reference and fail-closed validation rules.
- [Security](security.md) and [../SECURITY.md](../SECURITY.md) - auth, RBAC, CSRF, crypto, headers, and the SSRF guard.
- Repo root: [../README.md](../README.md), [../Makefile](../Makefile), [../.env.example](../.env.example).
