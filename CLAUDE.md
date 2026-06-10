# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Conventions

- Do not use "— " (em dash followed by a space) in code, comments, docs, or commit messages. Prefer a regular hyphen, colon, or a reworded sentence.

## Commands

```bash
make build        # build SPA (npm ci + vite build) then the static Go binary -> bin/verix-dbm
make spa          # build only the embedded React SPA
make run          # go run ./cmd/server (needs SPA already built for /app, or use DEV mode)
make vet          # go vet ./...
make test         # go test ./...
make vuln         # govulncheck dependency scan
make tidy         # go mod tidy
```

Run a single Go test:
```bash
go test ./internal/web -run TestName -v
```

Local development (no Keycloak required):
```bash
DBM_DEV_MODE=true go run ./cmd/server        # backend on :8080, auto-logged-in as Dev Admin
cd internal/web/spa && npm install && npm run dev   # Vite dev server, proxies /api + /c to :8080
```

The Go binary is fully static (`CGO_ENABLED=0`, pure-Go SQLite via `modernc.org/sqlite`). The SPA must be built before `go build`/`go run` for it to be embedded and served at `/app` (`go:embed` in `internal/web/spa.go`).

## Architecture

Single static Go binary with a React workbench baked in. One process serves the JSON API, a legacy server-rendered UI, and the embedded SPA.

**Two HTTP surfaces, one router.** [internal/web/server.go](internal/web/server.go) wires everything. The same auth/RBAC/CSRF/rate-limit middleware stack protects both:
- Legacy `html/template` pages (handlers in `handlers_*.go`, templates in `internal/web/templates`) - kept reachable but no longer the landing page.
- JSON API mounted at `/api` ([internal/web/api.go](internal/web/api.go)) - this is what the React SPA calls. The two surfaces largely mirror each other (e.g. `s.pgQuery` vs `s.apiQuery`).

The root `/` serves the React SPA; the SPA lives in `internal/web/spa` and is the primary UI.

**Engine packages are pluggable.** Each database engine is its own package behind a shared pooled connection registry:
- [internal/conn](internal/conn) - `Registry` of lazy, idle-closing connection pools keyed by connection ID. **Pools are shared (`MaxConns = 4`) and connections are reused, so never leak session state** (e.g. `SET default_transaction_read_only`) across acquires.
- [internal/postgres](internal/postgres) - pgx v5 introspection, query execution, DDL, and code generators. `Query(..., readOnly)` sets `default_transaction_read_only` per call; DDL runs via `ExecScript` as one atomic transaction.
- [internal/redisdb](internal/redisdb) - go-redis SCAN browse, value viewers, command console with a read-only allowlist.

Adding an engine = new package + its API/UI tabs, without touching auth, crypto, or the workbench shell.

**Auth & security model** ([internal/auth](internal/auth), [SECURITY.md](SECURITY.md)):
- Keycloak OIDC login; realm roles map to **admin / write / read** with **deny-by-default** (no role -> 403).
- `DBM_DEV_MODE=true` bypasses OIDC and auto-logs-in a local admin. It must be set explicitly: in production the app **refuses to start** without OIDC rather than fail open.
- Saved connection passwords are **AES-256-GCM encrypted at rest** in SQLite ([internal/crypto](internal/crypto)) and never sent to the browser.
- **CSRF** on every mutating request (form posts and the SPA's `X-CSRF-Token` header), including logout (POST only).
- Security headers (CSP `frame-ancestors 'none'`, HSTS over TLS) on every response ([internal/web/security.go](internal/web/security.go)).
- In-process rate limiting on auth and authed endpoints ([internal/web/ratelimit.go](internal/web/ratelimit.go)).
- All mutating actions are written to an audit log (admin-viewable); role passwords are redacted.

**Metadata store** ([internal/store](internal/store)): SQLite holds saved connections and the audit log only - never the data in the connected databases.

**Guardrails on query/console paths**: 30s statement timeout, 1000-row result cap, and a confirmation gate for destructive statements (`DROP`/`TRUNCATE`, unguarded `DELETE`/`UPDATE`). Preserve these when touching query handlers.

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
internal/web        chi router, JSON API (api.go) + legacy html/template pages
internal/web/spa    React + TypeScript + Vite workbench (embedded via go:embed)
```

Configuration is via env vars - see [.env.example](.env.example). Required in production: `DBM_ENC_KEY` (64 hex chars), the `OIDC_*` set, and `DBM_BASE_URL`.
