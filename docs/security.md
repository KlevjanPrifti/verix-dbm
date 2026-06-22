---
title: Security
nav_order: 5
---

# Security model (deep dive)

This is the engineering deep-dive into how verix-dbm enforces authentication, authorization, credential protection, and request hardening. The operator-facing policy summary lives in [SECURITY.md](https://github.com/KlevjanPrifti/verix-dbm/blob/main/SECURITY.md); read that first for the short version, then come here for the exact mechanisms, source locations, and fail-closed behaviors.

Source files referenced throughout: `internal/auth/auth.go`, `internal/auth/sessions.go`, `internal/web/access.go`, `internal/crypto/crypto.go`, `internal/web/security.go`, `internal/web/ratelimit.go`, `internal/web/egress.go`, `internal/web/server.go`, `internal/web/api.go`, `cmd/server/main.go`, `internal/config/config.go`.

## 1. Threat model in brief

verix-dbm is a database workbench operators put in front of databases that should not be directly reachable. The design assumptions:

- **No exposed DB port.** The target databases are not published to the host or the internet. verix-dbm reaches them over an internal/private network; only the app's own HTTP port (`:8080` by default, `DBM_ADDR`) is exposed, and in production it sits behind a TLS-terminating reverse proxy. The demo and Dokploy compose files demonstrate this: only the `dbm` service publishes a port, the bundled Postgres/Redis do not. See [Deployment](deployment.md).
- **SSO, not shared passwords.** Humans authenticate to Keycloak (OIDC), not to a shared database account. There is no local username/password store in verix-dbm. Database credentials are saved once by an admin, encrypted at rest, and never sent back to a browser.
- **The least-privilege DB role is the real boundary.** Application-layer gates (read-only toggles, destructive-statement confirmation, server-side-exec screens) are defense in depth and UX guardrails. They are best-effort, not authorization. The authoritative control over what a connection can do is the privilege of the database role configured on that connection. Grant each saved connection the least privilege it needs.

Fail-closed is the recurring theme: the process refuses to start without OIDC and an encryption key in production, the SQLite engine is disabled unless explicitly allowlisted, the SSRF guard blocks by default, and a `crypto/rand` failure panics rather than minting a weak token.

## 2. Authentication: Keycloak OIDC

Implemented in `internal/auth/auth.go` using `github.com/coreos/go-oidc/v3/oidc` plus `golang.org/x/oauth2`. The authenticator is constructed only when `!cfg.DevMode`; the OIDC provider is discovered at startup via `oidc.NewProvider(ctx, cfg.OIDCIssuer)`, so a misconfigured or unreachable issuer fails the boot rather than the first login.

The flow is **authorization code with PKCE**, hardened with a server-side single-use state and a per-login nonce.

| Element | Detail |
|---|---|
| Scopes | `openid`, `profile`, `email` |
| PKCE | `oauth2.GenerateVerifier()` + `oauth2.S256ChallengeOption` (S256). The verifier is stored server-side and replayed on `Exchange` via `oauth2.VerifierOption`. |
| State | 192-bit hex token, stored server-side under key `state:<state>` (10-minute TTL) AND in cookie `dbm_state`. The cookie alone is not trusted. |
| Nonce | Per-login 192-bit token, passed as `oidc.Nonce(nonce)`; the ID token's `nonce` claim is verified with `subtle.ConstantTimeCompare`. |

### Login (`GET /auth/login`)

In dev mode this is a 302 to `/`. Otherwise it mints `state`, `nonce`, and a PKCE verifier, stores them in a server-side entry keyed `state:<state>` (10 min TTL), sets the `dbm_state` cookie (`HttpOnly`, `Secure` per `secure(cfg)`, `SameSite=Lax`, `MaxAge=600`), and redirects to the IdP `AuthCodeURL` carrying the state, nonce, and S256 challenge.

### Callback (`GET /auth/callback`)

Validation runs in this order; each failure writes an `auth_login_failed` audit event with a specific detail:

1. The query `state` must constant-time match the `dbm_state` cookie (400 `invalid state`).
2. The authoritative check: the server-side entry `state:<state>` must exist and match (400 `unknown or expired state`). The cookie is corroborating, not authoritative.
3. The entry is consumed (`Delete("state:"+state)`) and the cookie cleared, so a callback URL cannot be replayed.
4. `oauth.Exchange` with the stored PKCE verifier (502 `token exchange failed`).
5. `id_token` extracted (502 `no id_token`) and verified (401 `id_token verify failed`).
6. Nonce match (401 `invalid nonce`).
7. Claims parsed (502 `claims parse failed`).

Roles and groups are read from the ID token claims (`realm_access.roles`, `groups`). The **access token** is then signature-verified separately (`atVerifier`, `SkipClientIDCheck: true`, so audience is skipped but signature/issuer/expiry are still checked) and, if it verifies, its `realm_access.roles` and `groups` are unioned in (`mergeRoles`). This is because Keycloak puts realm roles in the access token by default and only in the ID token when a mapper opts in. If the access token is not a verifiable JWT, the code falls back to ID-token roles only. On success the user's capabilities are applied (`applyCaps`), an `auth_login` event is audited, a session is created, the session cookie is set, and the browser is 302'd to `/`.

### Logout (`POST /auth/logout`)

Logout is **POST only and CSRF-protected** (`CheckCSRF`; 403 `bad csrf` otherwise). It is deliberately not wired as a GET so a cross-site page cannot force a logout. It deletes the local session, clears the `dbm_session` cookie, and (when the IdP advertises an `end_session_endpoint`) performs an RP-initiated logout: a 302 to the IdP with `id_token_hint`, `client_id`, and `post_logout_redirect_uri=cfg.BaseURL`.

### Dev-mode bypass and why it must be explicit

`DBM_DEV_MODE=true` skips OIDC entirely. `auth.Middleware` auto-logs every request in as a local admin (`User{Subject:"dev", Name:"Dev Admin", Email:"dev@localhost", Roles:["dev"], Admin/Write/Read:true}`), sets a session cookie and CSRF token, and never contacts Keycloak.

This is gated hard for safety. In `config.Load()`, the dev-mode short-circuit runs before the production checks, but those production checks otherwise **refuse to boot** without OIDC: if `OIDC_ISSUER` or `OIDC_CLIENT_ID` is empty (and dev mode is off) the process exits with an error. There is no silent fail-open: a missing OIDC config does not quietly disable auth, it stops the server. Dev mode must therefore be set with the exact literal string `true` (boolean env vars are strict; `1`, `yes`, `TRUE` all read as `false`). Dev mode also implies `AllowLocalTargets` (the SSRF guard is skipped) and short-circuits the encryption-key requirement, booting with an ephemeral random key. Never enable it on an internet-reachable deployment.

## 3. Sessions

Implemented in `internal/auth/sessions.go`.

- Cookie name: `dbm_session`. Session TTL: 12 hours (`sessionTTL`).
- Cookie attributes (`setCookie`): `Path=/`, `HttpOnly: true`, `Secure: secure(cfg)`, `SameSite=Lax`, `MaxAge=43200`.
- `secure(cfg)` returns true only when `cfg.BaseURL` starts with `https` (first five characters). The Secure flag is therefore set only when `DBM_BASE_URL` is an HTTPS URL. Run behind TLS in production so session and state cookies are marked Secure.
- The session token (`token()`) is 24 random bytes rendered as 48 hex characters (192-bit). A `crypto/rand` failure **panics** (fail closed): the app never mints a weak or zero token. The same generator backs CSRF tokens and OIDC state/nonce.

### Backends (`DBM_SESSION_BACKEND`)

| Value | Store | Notes |
|---|---|---|
| `memory` (default, or unset) | `memSessionStore` | In-memory map, mutex-guarded. Lost on restart, single-node. An hourly `reap()` ticker deletes expired entries. |
| `redis` | `redisSessionStore` | Dials `DBM_SESSION_REDIS_URL` via `redis.ParseURL` and **pings at startup with a 5s timeout**, so a misconfigured URL fails the boot, not the first login. Key prefix `dbm:sess:`; get/put/delete use 2s context timeouts; the Redis TTL matches the session TTL. |
| anything else | error: `DBM_SESSION_BACKEND must be 'memory' or 'redis', got <x>` |

The Redis backend persists a `sessionWire` JSON shape (`user`, `expires`, `csrf`, `idToken`, `oauthState`, `nonce`, `pkceVerifier`) because the in-memory `session` struct fields are unexported. Both backends check expiry on read.

### HA implications

For multi-replica deployments, pair the Redis session backend with a Postgres metadata store (`DBM_STORE_DRIVER=postgres`). With sessions in a shared keyspace and metadata in a shared database, any replica can serve any session, and N identical replicas survive restarts and node loss with no session loss. The default memory backend loses all sessions on restart and cannot be shared across replicas. See [Configuration](configuration.md) and [Data model](data-model.md).

## 4. Authorization

### Deny-by-default RBAC

Capabilities are three boolean flags on the `User` with strict implication: **admin includes write includes read**. They are derived from Keycloak realm roles in `applyCaps`:

| Realm role (config default) | Env var | Grants |
|---|---|---|
| `dbm-admin` | `OIDC_ADMIN_ROLE` | admin + write + read |
| `dbm-write` | `OIDC_WRITE_ROLE` | write + read |
| `dbm-read` | `OIDC_READ_ROLE` | read |

Access is **deny-by-default**. `auth.Middleware` rejects an authenticated user who has no read capability with HTTP 403 (plain text that names the three configured roles). A valid session is not sufficient; the user must hold a mapped role. Unauthenticated callers are 302'd to `/auth/login` instead. The 403 is a deliberate dead-end with no redirect loop.

### `DBM_OPEN_READ`

`DBM_OPEN_READ=true` is a pre-hardening compatibility switch. In `applyCaps` it unconditionally sets `u.Read = true` for any authenticated realm user regardless of roles. It never grants write or admin. It logs a warning at boot in production. Prefer explicit role mapping; reach for this only to ease migration.

### Scoped per-connection grants (`DBM_SCOPED_ACCESS`)

By default a user's global role applies to every saved connection. When `DBM_SCOPED_ACCESS=true`, non-admins are instead authorized per connection through grants, managed by admins in the connection edit dialog ([GrantsPanel.tsx](https://github.com/KlevjanPrifti/verix-dbm/blob/main/internal/web/spa/src/components/GrantsPanel.tsx)).

A grant binds a **subject** (a Keycloak group path or a realm-role name) to a **level** (`read` or `write`) on one connection. A user's subjects are the union of their realm roles and groups (`User.Subjects()`). The store resolves the highest grant any of the user's subjects holds on a connection (write outranks read) via `GrantForSubjects`; `ResolveConnAccess(u, grant, scoped)` (`internal/web/access.go`, pure and unit-tested) then computes effective access:

- Not scoped, OR the user is an admin: `{Read: u.Read, Write: u.Write}`. **Admins always bypass scoping and see everything.**
- Scoped, no grant: `{}` (no access at all).
- Scoped, read grant: `{Read: u.Read}`.
- Scoped, write grant: `{Read: u.Read, Write: u.Write}`.

The key invariant: **a grant scopes where a user acts, never what they may do above their global capability.** Effective capability is `min(grant, global)`. A user with only the global `read` role who is given a `write` grant still only reads, because `out.Write = u.Write`, which is false. Connection CRUD remains a global-admin power; there is no per-connection admin grant and no sub-connection (per-db/schema) scope.

`connFor(r)` (`internal/web/server.go`) is the single read-access chokepoint for the whole API: it loads the connection and requires `access(...).Read`. An inaccessible connection returns `errNoAccess`, which handlers map to **404, identical to not-found**, so scoped users cannot enumerate connections they cannot see. Write/admin endpoints layer `apiRequireWrite` / `apiRequireAdmin` on top (CSRF, then the capability check, then the read-only check). Note that grant management endpoints work regardless of `DBM_SCOPED_ACCESS` (so access can be set up first); grants only become *effective* once scoping is on.

## 5. Credential encryption

Saved connection passwords are encrypted at rest and never returned to a browser. Implemented in `internal/crypto/crypto.go`.

- Algorithm: **AES-256-GCM** (`aes.NewCipher` + `cipher.NewGCM`). Keys must be exactly 32 bytes.
- Ciphertext format: `<keyID>$<base64(nonce||ciphertext)>` (standard base64; the GCM nonce is prepended and the auth tag appended by `Seal`). `keyID` must be non-empty and must not contain `$`.

### Versioned keyring

A `Box` keyring holds a `primaryID` (used for all new writes), a map of `id -> AEAD`, and an `order` list (primary first) for trying legacy unprefixed ciphertext. Key sources:

- `DBM_ENC_KEY` (single key): a 32-byte AES key as 64 hex chars (or base64; `decodeKey` tries hex then base64, must yield 32 bytes). It is assigned id `1`, so writes become `1$...`. Empty in non-production mode mints an **ephemeral** random key (logs a warning; saved credentials become undecryptable after restart).
- `DBM_ENC_KEYS` (rotation form `id:key,id:key,...`): the **first entry is primary** for new writes, the rest are retained for decryption during rotation. `DBM_ENC_KEYS` supersedes `DBM_ENC_KEY` when both are set.

`Encrypt` always uses the primary key. `Decrypt` opens prefixed ciphertext with the named key (error `no key %q ... (rotated out?)` if missing) and falls back to trying every retained key in order for legacy unprefixed values; GCM authentication makes a wrong-key attempt fail cleanly.

In production the app **refuses to start** without `DBM_ENC_KEY` or `DBM_ENC_KEYS`, rather than minting an ephemeral key that would diverge across HA replicas and be unreadable after restart.

### Zero-downtime rotation and the Re-encrypt flow

1. Add the new key as primary and keep the old one retained: `DBM_ENC_KEYS="v2:new,v1:old"`. Restart (or roll the replicas). New writes use `v2`; old `v1$...` ciphertext still decrypts.
2. Trigger **Re-encrypt** to rewrite all stored credentials under the new primary. Either click Re-encrypt in the admin UI or call `POST /api/admin/reencrypt` (admin-only, CSRF-protected, `apiReencrypt` in `internal/web/api_audit.go`). It iterates connections and calls `box.Reencrypt(c.PasswordEnc)`: ciphertext already under the primary is skipped (no-op write avoided); otherwise it is decrypted and re-encrypted under the primary, written back via `UpdatePasswordEnc`, and the cached pool dropped with `reg.Forget(id)`. It audits action `reencrypt` and returns `{ primaryKey, checked, rewritten, failed }`.
3. Once Re-encrypt reports everything rewritten, drop the old key: `DBM_ENC_KEYS="v2:new"` and restart.

### Provider / KMS seam

Key material is sourced through a `Provider` interface (`Keys(ctx) (primaryID, []KeySpec, error)`). `StaticProvider` is the env-backed default. The seam lets an external KMS/Vault implement `Provider` later, but no external KMS is wired today. Validation (`NewFromProvider`) rejects empty or `$`-containing ids, non-32-byte material, duplicate ids, and a primary id not present among the keys.

### `cred_access` audit event

Decrypting a stored password to open a pool emits a dedicated audit event: `Action: "cred_access"`, `ConnID: c.ID`, `Detail: c.Name`, `Success: true`. It is wired in `cmd/server/main.go` via `reg.OnCredentialAccess(...)` and fired by the registry only on a **successful** decrypt; a decrypt failure does not fire it. This gives an audit trail of every time a saved credential is actually used to dial a database.

## 6. CSRF protection

CSRF is enforced on every state-changing request (`internal/auth/auth.go`, `CheckCSRF`). Each session carries its own CSRF token, minted by `token()` at session creation and independent of the session id. The token is surfaced to the SPA on `GET /api/me` as `csrf`.

`CheckCSRF(r)` logic:

1. Require the `dbm_session` cookie (else false).
2. Look up the expected token (`csrfFor(cookie)`; false if empty).
3. Read the supplied token from the `X-CSRF-Token` header; if absent, fall back to the `csrf` form field.
4. Pass only if `subtle.ConstantTimeCompare(got, want) == 1` and `got != ""`.

The SPA sends `X-CSRF-Token` on every non-GET request; HTML form posts send a hidden `csrf` field. This applies to all mutating API endpoints (create/update/delete connection, grants, DDL, console writes, re-encrypt) and to **logout, which is POST-only**. One deliberate quirk: `GET /c/{id}/export` is a GET that nonetheless checks CSRF, because it triggers a file download.

## 7. SSRF egress guard

The "test connection" probe and connection create/update let an admin point the server at an arbitrary `host:port`, a classic SSRF lever. `guardEgressHost(ctx, host, allowLocal)` (`internal/web/egress.go`) runs before dialing a target.

- `allowLocal = cfg.AllowLocalTargets || cfg.DevMode`. `AllowLocalTargets` comes from `DBM_ALLOW_LOCAL_TARGETS=true`. **When `allowLocal` is true the guard is skipped entirely**, and dev mode implies it.
- An empty host is rejected (`empty host`).
- A literal IP is checked directly; a hostname is resolved with `net.Resolver.LookupIPAddr` and **rejected if ANY resolved IP is blocked**, so a name cannot smuggle a metadata IP alongside a public one.

`blockedEgressIP(ip)` blocks:

- loopback (`127.0.0.0/8`, `::1`),
- link-local unicast (`169.254.0.0/16`, which includes the cloud-metadata endpoint `169.254.169.254`, and `fe80::/10`),
- link-local multicast, interface-local multicast, and any multicast,
- the unspecified address (`0.0.0.0`, `::`).

**RFC1918 private ranges are allowed on purpose**, because real databases routinely live on private networks. IPv4-mapped IPv6 (e.g. `::ffff:127.0.0.1`) is caught because `net.IP`'s methods unmap. A blocked target returns an error naming the resolved address and the `DBM_ALLOW_LOCAL_TARGETS=true` override. This is defense in depth; still run the app with restricted container egress. In the API, `apiTestConnection` surfaces a guard failure as HTTP 200 `{ok:false,error}` (so the SPA renders it inline), while create/update return 400. SQLite connections are exempt from the guard (no network host; they are fenced by `DBM_SQLITE_DIR` instead).

## 8. Security headers and CSP

`securityHeaders(cfg)` (`internal/web/security.go`) is global middleware, set on every response:

| Header | Value |
|---|---|
| `X-Frame-Options` | `DENY` |
| `X-Content-Type-Options` | `nosniff` |
| `Referrer-Policy` | `no-referrer` |
| `Content-Security-Policy` | see below |
| `Strict-Transport-Security` | `max-age=63072000; includeSubDomains` (only when `DBM_BASE_URL` starts with `https`) |

The exact CSP (directives joined with `; `):

```
default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self' <IdP origin>; frame-ancestors 'none'
```

Notes:

- `script-src` is `'self'` only, with **no `'unsafe-eval'`**: the Vite-built React SPA ships no `eval`/`new Function`.
- `form-action` is `'self'` plus the IdP origin (`scheme://host` of `OIDC_ISSUER`, via `originOf`) appended when parseable. Browsers apply `form-action` to the whole redirect chain, so the issuer origin must be allowed or the RP-initiated logout redirect to the IdP's `end_session_endpoint` would be blocked and the user stranded.
- `frame-ancestors 'none'` denies framing outright (alongside `X-Frame-Options: DENY`).
- The only relaxations are `style-src 'unsafe-inline'` plus Google Fonts (`fonts.googleapis.com`, `fonts.gstatic.com`) and `img-src data:`.

**HSTS is only emitted over HTTPS** (the `BaseURL` prefix check), so a development HTTP deployment does not get pinned. Serve production over TLS so HSTS is in effect.

## 9. In-process rate limiting

`internal/web/ratelimit.go` provides a fixed-window limiter keyed by a string, with a 1-minute pruning ticker. There is a `maxRateKeys = 50_000` cap: when the table is full it sweeps expired entries inline and, if still full, **refuses new keys** (a fail-closed backstop against `X-Forwarded-For` key-rotation flooding). Two buckets are wired in `internal/web/server.go`:

| Bucket | Limit | Window | Key | Covers |
|---|---|---|---|---|
| `authLimit` | 20 | 1 minute | client IP (`clientIP`) | `GET /auth/login`, `GET /auth/callback` |
| `authedLimit` | 600 | 1 minute | `sessionKey` (`u:<email>` if authed, else `ip:<ip>`) | the whole authed surface (`/`, `/c/{id}/export`, `/api/*`, `/app*`) |

Over-limit requests get HTTP 429 `too many requests`. `clientIP` strips the port from `RemoteAddr` and does not itself trust `X-Forwarded-For`; forwarded-header handling only applies when `DBM_TRUST_PROXY=true` enables chi's `RealIP` middleware (off by default so clients cannot spoof their address). The operational endpoints `/healthz`, `/readyz`, and `/metrics` are not rate-limited (registered outside both groups), and logout is registered outside the auth-limit group. This is a floor, not a substitute for an edge WAF.

## 10. Audit logging, password redaction, and the destructive-statement backstop

### Audit logging and redaction

Every mutating action is recorded to the audit log in the metadata store (`AddAudit`), which is admin-viewable (`GET /api/audit`) and exportable as JSONL or CSV (`GET /api/audit/export`). Audit writes are best-effort and never block the request. Each event is also mirrored to the structured log via the `OnAudit` sink, so a SIEM can ingest it without coupling to the store. Auth outcomes additionally feed the `verixdbm_auth_logins_total{result}` metric (`auth_login` -> success, `auth_login_failed` -> failure). See [Observability](observability.md).

Before persistence, `auditDetail` (`internal/web/security.go`) redacts secrets from the statement, then truncates to 500 characters:

| Pattern | Matches | Replacement |
|---|---|---|
| `reSQLPassword` | `PASSWORD '...'` / `IDENTIFIED BY '...'` (handles doubled-quote `''` escaping) | `'***'` |
| `reSQLPasswordDollar` | dollar-quoted `PASSWORD $$...$$` / `$tag$...$tag$` (RE2 has no backrefs, so the closing tag is loose; over-redacts, the safe direction) | `'***'` |
| `reJSONPassword` | Mongo/JSON `"pwd"` / `"password": "..."` | `"***"` |
| `reRedisRequirepass` | `requirepass <val>` | `***` |
| `reRedisAuth` | line-anchored `^auth <val>` | `***` |

So `CREATE ROLE ... PASSWORD 'secret'` and Redis `AUTH` / `CONFIG SET requirepass` never store cleartext in the audit log (or its backups). Stored connection passwords are never sent to the browser. CSV export additionally neutralizes spreadsheet formula injection (cells starting with `=`, `+`, `-`, `@`, tab, or CR are prefixed with `'`).

### Per-engine destructive-statement gates (defense in depth)

These are application-layer backstops, not authorization; the authoritative control is still the least-privileged DB role on the connection.

- **Statement timeout (30s) and a 1000-row result cap** are enforced inside every engine on the query/browse paths. Preserve them when touching query handlers.
- **Destructive-statement confirmation** trips on `DROP`/`TRUNCATE` and unguarded `DELETE`/`UPDATE` (no `WHERE`). For SQL this is `dbsql.NeedsConfirm` (it strips comments and examines every statement); Redis (`redisdb.NeedsConfirm`) and Mongo (`mongodb.NeedsConfirm`) have their own dangerous-command lists. It is a UX gate: a write user can always confirm, and the naive split errs toward over-prompting.
- **Server-side execution / file-access screen** (`serverSideBlocked`, `internal/web/security.go`): admins are exempt; everyone else (including read-only filters) is blocked from primitives like Postgres `COPY ... PROGRAM` / `pg_read_file`, MySQL `LOAD DATA INFILE` / `INTO OUTFILE`, and SQLite `ATTACH` / `load_extension`. Mongo blocks `$where` / `$function` / `$accumulator` in find filters for non-admins, and routes dangerous DB commands through the admin-plus-confirm gate.
- **Admin-gating** on the most destructive operations: dropping tables, columns, indexes, schemas, and all role drop/alter operations require admin, not merely write.

## 11. Cross-links

- [Configuration](configuration.md): full environment variable reference and defaults, including every security-relevant flag.
- [Data model](data-model.md): the metadata store (connections, grants, audit log) and what is and is not persisted.
- [Deployment](deployment.md): TLS termination, network isolation, HA topology, and the demo/Dokploy compose files.
- Repo root: [SECURITY.md](https://github.com/KlevjanPrifti/verix-dbm/blob/main/SECURITY.md) (operator policy summary), [README.md](https://github.com/KlevjanPrifti/verix-dbm/blob/main/README.md), [.env.example](https://github.com/KlevjanPrifti/verix-dbm/blob/main/.env.example).
