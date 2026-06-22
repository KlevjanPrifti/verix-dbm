# Configuration reference

verix-dbm is configured entirely through environment variables. There is no config file: every knob is an env var read once at startup by `Load()` in `internal/config/config.go`. On a misconfiguration the process **fails closed**: `Load()` returns an error and `cmd/server/main.go` logs it to stderr and exits with status 1 before the server ever binds a port. This page documents every variable, its default, whether it is required, and the exact start-up behavior.

## How values are parsed

A few parsing rules apply uniformly (see `env`, `intEnv`, and the boolean reads in `internal/config/config.go`):

- **Strings with defaults** (`env(key, def)`): an unset var **or an empty string** falls back to the default. For example `DBM_ADDR=` (set but blank) yields `:8080`, not an empty bind address.
- **Raw strings** (`os.Getenv`): a small set of vars are read raw with no default and empty allowed: `DBM_SQLITE_DIR`, `DBM_ENC_KEY`, `DBM_ENC_KEYS`, `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URL`, `DBM_METRICS_TOKEN`, `DBM_SESSION_REDIS_URL`, `DBM_STORE_DSN`.
- **Integers** (`intEnv(key, def)`): unset, empty, non-numeric, **or negative** values silently fall back to the default. A bad integer is not an error.
- **Booleans**: enabled only by the exact lowercase literal `true`. Any other value (`1`, `yes`, `TRUE`, `on`) reads as `false`.

## Required in production

> **Production = `DBM_DEV_MODE` is not `true`.** In production the server refuses to start unless these are set:
>
> - **`OIDC_ISSUER` and `OIDC_CLIENT_ID`** - authentication. If either is empty, `Load()` returns:
>   `authentication is not configured: set OIDC_ISSUER and OIDC_CLIENT_ID, or set DBM_DEV_MODE=true for local development. Refusing to start with auth disabled`
> - **`DBM_ENC_KEY` or `DBM_ENC_KEYS`** - a persistent credential encryption key. If both are empty, `Load()` returns:
>   `encryption key is not configured: set DBM_ENC_KEY (64 hex chars) or DBM_ENC_KEYS, or set DBM_DEV_MODE=true for local development. Refusing to start without a persistent credential key`
>
> `DBM_BASE_URL` is also required in practice for a correct deployment (it derives the OIDC redirect and gates Secure cookies and HSTS), but note that `Load()` does **not** validate it: it defaults to `http://localhost:8080`.

### Start-up refusal behavior, in order

`Load()` runs these steps in this exact order:

1. **Derive the OIDC redirect default.** If `OIDC_REDIRECT_URL` is empty, it is set to `DBM_BASE_URL + "/auth/callback"`. This always runs, before the dev-mode check, so the derived value depends on `DBM_BASE_URL`.
2. **Dev-mode short-circuit.** If `DBM_DEV_MODE=true`, log the warning `config: DBM_DEV_MODE=true DEV mode, auto-login as a local admin. NEVER enable this in production.` and return immediately. **All production checks below are skipped**, so a dev process boots with no real auth and (if no key is set) an ephemeral encryption key.
3. **OIDC required.** If `OIDC_ISSUER` or `OIDC_CLIENT_ID` is empty, return the authentication error above. Only these two are checked here: `OIDC_CLIENT_SECRET` and `OIDC_REDIRECT_URL` are not validated by `Load()`.
4. **Encryption key required.** If both `DBM_ENC_KEY` and `DBM_ENC_KEYS` are empty, return the encryption error above. A missing key would mint a random ephemeral key, making saved credentials undecryptable after restart and unreadable across HA replicas.
5. **Informational warnings (non-fatal).** If `DBM_OPEN_READ=true`, log `config: DBM_OPEN_READ=true every authenticated realm user is granted READ access to all connections.`. If `DBM_SCOPED_ACCESS=true`, log `config: DBM_SCOPED_ACCESS=true non-admin users reach a connection only via a per-connection grant.`.

Conditional dependencies are **not** enforced by `Load()`; they are checked downstream:

- `DBM_STORE_DRIVER=postgres` needs `DBM_STORE_DSN` (validated when the store opens, `internal/store/store.go`).
- `DBM_SESSION_BACKEND=redis` needs `DBM_SESSION_REDIS_URL` (the Redis session backend pings at startup, so a misconfigured URL fails the process at boot rather than at first login, `internal/auth/sessions.go`).
- The SQLite **engine** needs `DBM_SQLITE_DIR`; unset disables SQLite entirely (fail closed), it is not an error.

## Full reference

Required column is for production (`DBM_DEV_MODE != true`). Dev mode skips all of these checks.

### HTTP

| Variable | Default | Required | Description |
| --- | --- | --- | --- |
| `DBM_ADDR` | `:8080` | no | HTTP bind address passed to `http.Server.Addr`. |
| `DBM_BASE_URL` | `http://localhost:8080` | recommended (not enforced by `Load()`) | Public base URL. Derives the `OIDC_REDIRECT_URL` default, sets the `post_logout_redirect_uri`, and gates the Secure cookie flag and HSTS: the session/state cookies get `Secure` and the `Strict-Transport-Security` header is sent **only when this value starts with `https`**. |
| `DBM_TRUST_PROXY` | `false` | no | When `true`, enables chi `middleware.RealIP` so `X-Forwarded-For` / `X-Real-IP` are honored for the client IP used in logging and rate limiting. Off by default so clients cannot spoof their address. Enable only behind a proxy that overwrites those headers. |

### Network egress (SSRF guard)

| Variable | Default | Required | Description |
| --- | --- | --- | --- |
| `DBM_ALLOW_LOCAL_TARGETS` | `false` | no | When `false` (default), the egress guard (`internal/web/egress.go`) rejects connection targets that resolve to loopback, link-local, cloud-metadata (`169.254.169.254`), unspecified, or multicast addresses, before dialing on connection create/update/test. RFC1918 private ranges are allowed (real databases live there). `true` disables the guard. Dev mode implies it. |

### Observability

| Variable | Default | Required | Description |
| --- | --- | --- | --- |
| `DBM_LOG_LEVEL` | `info` | no | Minimum slog level: `debug`, `info`, `warn`, or `error`. Unrecognized values fall through to `info`. Note: per-request log severity is derived from HTTP status, so at `warn` you lose all 2xx access logs. |
| `DBM_LOG_FORMAT` | `json` | no | `text` selects the slog text handler; anything else (including unset) uses JSON. JSON suits SIEM shipping; text is friendlier in dev. |
| `DBM_METRICS_TOKEN` | `""` (empty, open) | no | When set, `/metrics` requires `Authorization: Bearer <token>` (constant-time compared). Empty leaves `/metrics` open (fail-open by design); set it if the metrics endpoint is publicly reachable. |
| `DBM_AUDIT_RETENTION_DAYS` | `0` (keep forever) | no | Purge audit rows older than N days, on a 24h ticker (and once at startup). `0` keeps forever. Negative or unparseable falls back to `0`. |

### Credential encryption

| Variable | Default | Required | Description |
| --- | --- | --- | --- |
| `DBM_ENC_KEY` | `""` (empty) | yes, unless `DBM_ENC_KEYS` is set (or dev mode) | A 32-byte AES-256-GCM key as 64 hex chars (`openssl rand -hex 32`); base64 is also accepted. Encrypts saved connection passwords at rest. Empty in dev mode mints an ephemeral random key (logs a warning; unreadable after restart). |
| `DBM_ENC_KEYS` | `""` (empty) | yes, unless `DBM_ENC_KEY` is set (or dev mode) | Multi-key rotation form `"id:key,id:key,..."`. The **first entry is the primary** used for new writes; the rest are retained for decryption. **Supersedes `DBM_ENC_KEY` when set.** Rotate with no downtime: set `"v2:new,v1:old"`, restart, run Re-encrypt (`POST /api/admin/reencrypt`), then drop the old key. |

Production boot is refused only when **both** are empty.

### SQLite engine (target databases, not the metadata store)

| Variable | Default | Required | Description |
| --- | --- | --- | --- |
| `DBM_SQLITE_DIR` | `""` (empty, engine disabled) | no | Allowlist directory that SQLite **target** files must resolve under (`..` traversal and escaping symlinks are rejected, including symlinks on intermediate directories). **Empty disables the SQLite engine entirely (fail closed); SQLite is opt-in.** Example: `/data/sqlite`. Distinct from `DBM_SQLITE_PATH` (the metadata DB file). |

### Metadata store

The metadata store holds saved connections, per-connection grants, and the audit log only, never data from the connected databases.

| Variable | Default | Required | Description |
| --- | --- | --- | --- |
| `DBM_STORE_DRIVER` | `sqlite` | no | Metadata backend: `sqlite` (default, single-node) or `postgres` (shared/replicated for HA). |
| `DBM_SQLITE_PATH` | `./data/verix-dbm.db` | no | Path to the metadata DB file, used when `DBM_STORE_DRIVER=sqlite`. The container image defaults this to `/data/verix-dbm.db`. The parent directory is created at startup when the driver is not postgres. |
| `DBM_STORE_DSN` | `""` (empty) | conditionally (when `DBM_STORE_DRIVER=postgres`) | Postgres connection string for the metadata DB, e.g. `postgres://user:pass@host:5432/dbmmeta?sslmode=verify-full`. Not validated by `Load()`; enforced when the store opens. |

### High availability

| Variable | Default | Required | Description |
| --- | --- | --- | --- |
| `DBM_SESSION_BACKEND` | `memory` | no | Session store: `memory` (default, single-node, lost on restart) or `redis` (shared, so any replica serves any session). Any other value is an error at startup. |
| `DBM_SESSION_REDIS_URL` | `""` (empty) | conditionally (when `DBM_SESSION_BACKEND=redis`) | `redis://[:pass@]host:port/db`. The Redis backend pings at startup, so a bad URL fails the process at boot. Not validated by `Load()` itself. |
| `DBM_PG_POOL_MAX_CONNS` | `4` | no | Max pooled connections per registered SQL **target**. Despite the name, this caps Postgres, MySQL, **and** SQLite pools alike. It does not size the metadata store pool. Negative or unparseable falls back to `4`. |

### Dev / auth

| Variable | Default | Required | Description |
| --- | --- | --- | --- |
| `DBM_DEV_MODE` | `false` | n/a (the bypass switch) | `true` auto-logs every request in as a local admin (`Dev Admin`, all capabilities), does not require Keycloak, implies `DBM_ALLOW_LOCAL_TARGETS`, and short-circuits all production validation (OIDC and encryption). Must be set explicitly. Never enable on an internet-reachable deployment. |

### OIDC / Keycloak

| Variable | Default | Required | Description |
| --- | --- | --- | --- |
| `OIDC_ISSUER` | `""` (empty) | **yes** (checked by `Load()`) | Keycloak issuer URL, e.g. `https://keycloak.example.com/realms/yourrealm`. Empty in production refuses to start. Its origin is also appended to the CSP `form-action` so RP-initiated logout is not blocked. |
| `OIDC_CLIENT_ID` | `""` (empty) | **yes** (checked by `Load()`) | OIDC client id. Empty in production refuses to start. |
| `OIDC_CLIENT_SECRET` | `""` (empty) | needed for OIDC, not validated by `Load()` | OIDC client secret. |
| `OIDC_REDIRECT_URL` | derived: `DBM_BASE_URL + "/auth/callback"` | no | OIDC redirect URI. Computed when empty, before the dev-mode short-circuit. Must match the Keycloak client's configured redirect URI. |
| `OIDC_ADMIN_ROLE` | `dbm-admin` | no | Realm role mapped to the **admin** capability (everything, including DROP / TRUNCATE / FLUSH and connection CRUD). |
| `OIDC_WRITE_ROLE` | `dbm-write` | no | Realm role mapped to **write** (mutate data; implies read). |
| `OIDC_READ_ROLE` | `dbm-read` | no | Realm role mapped to **read** (browse / query only). |

Access is **deny-by-default**: an authenticated user with none of these roles gets HTTP 403, unless `DBM_OPEN_READ` is set.

### Access scoping

| Variable | Default | Required | Description |
| --- | --- | --- | --- |
| `DBM_OPEN_READ` | `false` | no | `true` grants READ to **any** authenticated realm user regardless of role (pre-hardening behavior). Logs a warning at boot in production. Does not grant write or admin. |
| `DBM_SCOPED_ACCESS` | `false` | no | `true` switches non-admin RBAC from global roles to **per-connection grants**: a non-admin reaches a connection only via a grant on one of their groups/roles, and a grant never exceeds their global capability. Admins always see everything. Logs a warning at boot in production. |

## Worked examples

### Minimal production

OIDC, an encryption key, and the public URL. Everything else keeps its default (SQLite metadata store, in-memory sessions, deny-by-default RBAC, SSRF guard on).

```bash
# Public URL (https enables Secure cookies + HSTS)
DBM_BASE_URL=https://dbm.example.com

# Authentication (required in production)
OIDC_ISSUER=https://keycloak.example.com/realms/yourrealm
OIDC_CLIENT_ID=verix-dbm
OIDC_CLIENT_SECRET=__from_keycloak__
# OIDC_REDIRECT_URL defaults to ${DBM_BASE_URL}/auth/callback

# Credential encryption (required in production): openssl rand -hex 32
DBM_ENC_KEY=00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff

# Metadata store on disk (default)
DBM_SQLITE_PATH=/data/verix-dbm.db

# Behind a TLS-terminating reverse proxy
DBM_TRUST_PROXY=true
```

### High availability (Postgres store + Redis sessions)

Run two or more identical replicas behind a load balancer. Sessions live in Redis (any replica serves any session) and metadata in a shared Postgres (replicas survive restarts and node loss). Use the same `DBM_ENC_KEY` / `DBM_ENC_KEYS` on every replica so each can decrypt the others' credential writes.

```bash
DBM_BASE_URL=https://dbm.example.com
DBM_TRUST_PROXY=true

OIDC_ISSUER=https://keycloak.example.com/realms/yourrealm
OIDC_CLIENT_ID=verix-dbm
OIDC_CLIENT_SECRET=__from_keycloak__

# Same key on every replica
DBM_ENC_KEY=00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff

# Shared metadata store
DBM_STORE_DRIVER=postgres
DBM_STORE_DSN=postgres://dbm:__pw__@pg.internal:5432/dbmmeta?sslmode=verify-full

# Shared sessions
DBM_SESSION_BACKEND=redis
DBM_SESSION_REDIS_URL=redis://:__pw__@redis.internal:6379/0

# Per-target SQL pool size (Postgres, MySQL, and SQLite targets)
DBM_PG_POOL_MAX_CONNS=8

# Optional: lock down the metrics endpoint and ship JSON logs
DBM_METRICS_TOKEN=__random_token__
DBM_LOG_FORMAT=json
```

### Local development

Dev mode bypasses Keycloak and auto-logs in as a local admin, and short-circuits the production checks, so no OIDC or encryption key is required. Add `DBM_SQLITE_DIR` only if you want to use SQLite targets.

```bash
DBM_DEV_MODE=true
DBM_LOG_FORMAT=text
# Optional: enable the SQLite engine against a sandbox directory
DBM_SQLITE_DIR=/tmp/verix-sqlite
```

Then:

```bash
DBM_DEV_MODE=true go run ./cmd/server               # backend on :8080, auto-logged-in as Dev Admin
cd internal/web/spa && npm install && npm run dev    # Vite dev server, proxies /api + /c to :8080
```

See [../.env.example](../.env.example) for a copy-ready template of all variables.

## Related docs

- [Security model](security.md) and [../SECURITY.md](../SECURITY.md): RBAC and deny-by-default roles, per-connection grants (`DBM_SCOPED_ACCESS` / `DBM_OPEN_READ`), credential encryption and zero-downtime key rotation (`DBM_ENC_KEY` / `DBM_ENC_KEYS`), CSRF, security headers, and the SSRF egress guard (`DBM_ALLOW_LOCAL_TARGETS`).
- [Deployment](deployment.md): Docker image, compose topologies, and the high-availability setup (`DBM_STORE_DRIVER` / `DBM_STORE_DSN`, `DBM_SESSION_BACKEND` / `DBM_SESSION_REDIS_URL`, `DBM_PG_POOL_MAX_CONNS`).
- [Observability](observability.md): structured logging (`DBM_LOG_LEVEL` / `DBM_LOG_FORMAT`), Prometheus metrics and the `/metrics` Bearer gate (`DBM_METRICS_TOKEN`), `/healthz` + `/readyz` probes, and audit retention (`DBM_AUDIT_RETENTION_DAYS`).
