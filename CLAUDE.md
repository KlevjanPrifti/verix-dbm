# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Conventions

- Do not use "— " (em dash followed by a space) in code, comments, docs, or commit messages. Prefer a regular hyphen, colon, or a reworded sentence.
- After every change, end the reply with a short, copy-ready commit message the user can paste. Write it like a human would: one concise line (roughly 50 chars, imperative mood, e.g. "fix sticky tab bar on mobile"), lowercase, no trailing period, no Claude/co-author attribution. Put it in its own fenced code block so it is easy to copy. Add a short body only if the change really needs explaining. This is just text for the user to copy; do not actually run `git commit` unless asked.

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

The root `/` serves the React SPA; the SPA lives in `internal/web/spa` and is the primary UI. It is a DataGrip-style workbench (React 18 + TypeScript 5 + Vite 5, Lucide icons, custom HUD theme in [styles/hud.css](internal/web/spa/src/styles/hud.css)): an Explorer tree on the left and a tabbed workspace whose tabs stay mounted so state survives switching. Tab kinds live in [components/tabs](internal/web/spa/src/components/tabs): **grid** (paginated browse with WHERE/ORDER BY, sort, inline TX-mode toggle, queued edits), **console** (SQL with read/write modes and the destructive-statement gate), **redis** (keyspace browser + read-only command console), **doc** (columns/keys/indexes/comments), and **usages** (inbound foreign keys). Other components: TableDesigner (form-backed DDL), ConnModal, DDLModal, AuditModal, GrantsPanel, ContextMenu, Dialog, Toasts. The shell is mobile-aware: the Explorer collapses to an off-canvas drawer and the tab bar pins its drawer toggle.

**Engine packages are pluggable.** Each database engine is its own package behind a shared pooled connection registry:
- [internal/conn](internal/conn) - `Registry` of lazy, idle-closing connection pools keyed by connection ID. **Pools are shared (`MaxConns = 4`) and connections are reused, so never leak session state** (e.g. `SET default_transaction_read_only`) across acquires.
- [internal/postgres](internal/postgres) - pgx v5 introspection, query execution, DDL, and code generators. `Query(..., readOnly)` sets `default_transaction_read_only` per call; DDL runs via `ExecScript` as one atomic transaction.
- [internal/redisdb](internal/redisdb) - go-redis SCAN browse, value viewers, command console with a read-only allowlist.

Adding an engine = new package + its API/UI tabs, without touching auth, crypto, or the workbench shell.

**Auth & security model** ([internal/auth](internal/auth), [SECURITY.md](SECURITY.md)):
- Keycloak OIDC login; realm roles map to **admin / write / read** with **deny-by-default** (no role -> 403).
- `DBM_DEV_MODE=true` bypasses OIDC and auto-logs-in a local admin. It must be set explicitly: in production the app **refuses to start** without OIDC rather than fail open.
- **Per-connection grants** (opt-in via `DBM_SCOPED_ACCESS=true`): a non-admin reaches a connection only if an admin has granted one of their Keycloak groups/realm roles `read`/`write` on it (managed in the connection edit dialog, [GrantsPanel.tsx](internal/web/spa/src/components/GrantsPanel.tsx)). A grant scopes *where* a user acts, never above their global capability; admins always see everything. `DBM_OPEN_READ=true` instead grants READ to any authenticated realm user (pre-hardening behaviour).
- Saved connection passwords are **AES-256-GCM encrypted at rest** ([internal/crypto](internal/crypto)) and never sent to the browser. Keys are **versioned and rotatable**: ciphertext is `<keyID>$<base64(nonce||ciphertext)>`, a keyring keeps a primary (for new writes) plus retained keys (to decrypt during rotation). Rotate with no downtime by setting `DBM_ENC_KEYS="v2:new,v1:old"`, then triggering **Re-encrypt** (admin UI or `POST /api/admin/reencrypt`), then dropping the old key. A `Provider` seam allows an external KMS later but none is wired today. Decrypting a stored password to open a pool fires a `cred_access` audit event.
- **CSRF** on every mutating request (form posts and the SPA's `X-CSRF-Token` header), including logout (POST only).
- Security headers (CSP `frame-ancestors 'none'`, HSTS over TLS) on every response ([internal/web/security.go](internal/web/security.go)).
- In-process rate limiting on auth and authed endpoints ([internal/web/ratelimit.go](internal/web/ratelimit.go)).
- All mutating actions are written to an audit log (admin-viewable, CSV-exportable); role passwords are redacted. Optional retention purge via `DBM_AUDIT_RETENTION_DAYS`.

**Metadata store** ([internal/store](internal/store)): holds saved connections, per-connection grants, and the audit log only - never the data in the connected databases. The backend is **pluggable**: SQLite (default, single-node) or Postgres (`DBM_STORE_DRIVER=postgres` + `DBM_STORE_DSN`) so several replicas can share one metadata DB. The same SQL is written once; placeholders are rebound to `$N` and a few schema differences (id columns, the reserved word `"user"`) are handled per driver.

**Per-customer HA** (run 2+ replicas behind a load balancer): sessions are pluggable too ([internal/auth/sessions.go](internal/auth/sessions.go)) - `memory` (default, lost on restart) or `redis` (`DBM_SESSION_BACKEND=redis` + `DBM_SESSION_REDIS_URL`, JSON sessions in a shared keyspace so any replica serves any session). Pair Redis sessions with a Postgres store and N identical replicas survive restarts and node loss with no session loss. The per-target Postgres pool size is `DBM_PG_POOL_MAX_CONNS` (default 4).

**Observability** ([internal/web/observ.go](internal/web/observ.go)): structured request logging (`DBM_LOG_LEVEL`/`DBM_LOG_FORMAT`), Prometheus metrics at `/metrics` (optionally Bearer-gated by `DBM_METRICS_TOKEN`), `/healthz` + `/readyz` probes, and audit events mirrored to the structured log for SIEM forwarding. Infra paths are excluded from per-request logs.

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
internal/auth       OIDC + sessions (memory|redis) + RBAC + per-conn grants + CSRF
internal/web        chi router, JSON API (api.go), legacy html/template pages, security, ratelimit, observ
internal/web/spa    React + TypeScript + Vite workbench (embedded via go:embed)
```

Key Go deps: chi v5 (router), pgx v5 (Postgres), go-redis v9, coreos/go-oidc v3 + x/oauth2 (auth), modernc.org/sqlite (pure-Go SQLite), prometheus/client_golang (metrics).

Engines today are **PostgreSQL** (pgx) and **Redis/Valkey** (go-redis); no others. Postgres can also serve as the *metadata* store backend for HA, separate from the databases users connect to.

Configuration is via env vars - see [.env.example](.env.example). Required in production: `DBM_ENC_KEY` (64 hex chars) or `DBM_ENC_KEYS`, the `OIDC_*` set, and `DBM_BASE_URL`. Optional subsystems are gated by env: HA (`DBM_STORE_DRIVER`/`DBM_STORE_DSN`, `DBM_SESSION_BACKEND`/`DBM_SESSION_REDIS_URL`, `DBM_PG_POOL_MAX_CONNS`), access scoping (`DBM_SCOPED_ACCESS`, `DBM_OPEN_READ`), and observability (`DBM_LOG_*`, `DBM_METRICS_TOKEN`, `DBM_AUDIT_RETENTION_DAYS`).
