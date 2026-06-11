# Security model & operational guidance

verix-dbm is a web front-end that runs SQL and Redis commands against databases
you register. Treat it like an admin tool: its blast radius is exactly the
privileges of the database roles you give it. This document describes the trust
model, the controls in place, and the deployment choices that keep it safe.

## Authentication & authorization

- **Auth is required and fails closed.** Login is Keycloak OIDC. If OIDC is not
  fully configured the process **refuses to start**  it will not silently fall
  back to an open, auto-admin mode. Local development without Keycloak requires
  an explicit `DBM_DEV_MODE=true` (which logs in everyone as admin  never set it
  on a reachable deployment).
- **Access is deny-by-default.** A valid session is not enough: a user must hold
  one of the realm roles `OIDC_READ_ROLE` (default `dbm-read`),
  `OIDC_WRITE_ROLE` (`dbm-write`), or `OIDC_ADMIN_ROLE` (`dbm-admin`). With no
  role they get HTTP 403. `admin ⊇ write ⊇ read`.
  - Set `DBM_OPEN_READ=true` to restore the old "any authenticated realm user
    may read everything" behaviour. Only do this when the realm is dedicated to
    this app.
- **Per-connection access (optional).** By default roles are global: a `read`
  user can read **every** registered connection, a `write` user can write to all
  of them. Set `DBM_SCOPED_ACCESS=true` to switch non-admin users to
  **per-connection grants**: an admin grants a Keycloak group path or realm-role
  name `read` or `write` on a specific connection (in the connection's edit
  dialog), and a non-admin user reaches a connection only through such a grant.
  Grants scope **where** a user acts; they never raise **what** they may do above
  their global capability (a `read`-role user with a `write` grant still only
  reads). Global admins bypass scoping and see/manage everything. With scoping
  off (the default), the original global-role behaviour is unchanged. When
  scoping is off, don't register a connection some authenticated users shouldn't
  reach.
- **CSRF** protects every state-changing request (`X-CSRF-Token` header or a
  `csrf` form field), including logout. Session cookies are `HttpOnly`,
  `SameSite=Lax`, and `Secure` when `DBM_BASE_URL` is `https`.

## The database role IS the security boundary

A read-only transaction stops *data writes*, but it does **not** stop everything
a privileged Postgres role can do. `COPY … TO/FROM PROGRAM`, `pg_read_file()`,
`lo_import/lo_export`, and friends reach the database host's OS and depend only
on the connected role's privileges  not on this app.

**Therefore: register each connection with a least-privileged database role.**
- For Postgres, do **not** connect as `postgres`/superuser. Create a role with
  only the privileges the users of that connection need.
- For Redis, prefer an ACL user restricted to the commands you actually use.

As defense in depth, verix-dbm additionally:
- Blocks `COPY … PROGRAM`, `pg_read_file`/`pg_ls_dir`/`pg_stat_file`,
  `lo_import`/`lo_export`, and related server-side primitives for **non-admin**
  users (in the console, the grid filter, and exports). This is a conservative
  keyword screen, not a parser  it is a backstop, not a substitute for a
  least-privileged role.
- Gates Redis data-flush, scripting (`EVAL`/`FUNCTION`/`SCRIPT`), `MODULE`,
  `CONFIG`, replication (`SLAVEOF`/`REPLICAOF`), `MIGRATE`, and admin/persistence
  commands behind **admin + explicit confirmation**; read-only users are limited
  to a read allowlist.

## Credentials & secrets

- Saved connection passwords are **AES-256-GCM encrypted at rest** in SQLite
  (`DBM_ENC_KEY`, 32 bytes). The ciphertext is never sent to the browser;
  "Save as copy" duplicates it server-side. Set a stable `DBM_ENC_KEY` in
  production  without it an ephemeral key is used and saved passwords become
  unreadable after a restart.
- **Keys are versioned and rotatable without downtime.** `DBM_ENC_KEYS`
  (`id:key,id:key,...`, first = primary) lets a new key coexist with the old one;
  an admin then runs **Re-encrypt** (`POST /api/admin/reencrypt`) to roll stored
  credentials onto the new key, after which the old key can be dropped. The
  keyring also exposes a `Provider` seam so an external KMS / Vault can supply key
  material. Every decryption of a stored credential for use is audited
  (`cred_access`). See
  [docs/phases/phase-3-secrets-key-management.md](docs/phases/phase-3-secrets-key-management.md).
- The **audit log redacts** `PASSWORD '…'` / `IDENTIFIED BY '…'` (SQL) and
  Redis `AUTH` / `CONFIG SET requirepass` values before persisting, so role
  passwords you set through the UI don't land in SQLite in cleartext. The same
  redaction applies to events mirrored to the structured log and to exports.
- **Audit forwarding, export, and retention.** Every audit event is mirrored to
  the structured (JSON) log, so a log shipper forwards it to your SIEM with no
  extra integration. Admins can pull the full log via
  `GET /api/audit/export?format=jsonl|csv`. `DBM_AUDIT_RETENTION_DAYS` purges rows
  older than the window (0 = keep forever). See
  [docs/phases/phase-2-operability.md](docs/phases/phase-2-operability.md).

## Transport & browser hardening

- **Always terminate TLS in front of this app** (e.g. Traefik). Cookies are only
  marked `Secure` when `DBM_BASE_URL` is `https`, and HSTS is emitted there.
- Postgres connections default to `sslmode=prefer` (attempt TLS). For remote /
  production targets, set `sslmode=verify-full` (with a CA) in the connection's
  Options so credentials and data are encrypted and the server is authenticated.
- Responses carry `X-Frame-Options: DENY` (+ CSP `frame-ancestors 'none'`),
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, and a CSP.
  The CSP still allows `script-src 'unsafe-eval'` (the legacy Alpine.js workbench
  needs it) and inline styles; tightening it further is tracked alongside
  self-hosting fonts and retiring the legacy pages.
- **Operational endpoints are unauthenticated by design:** `/healthz` and
  `/readyz` (liveness/readiness) expose no sensitive data, and `/metrics` exposes
  only aggregate counters with bounded-cardinality labels (no SQL, no
  credentials). Set `DBM_METRICS_TOKEN` to require a Bearer token on `/metrics`,
  or restrict these paths at your ingress, when the port is internet-reachable.

## Network exposure (SSRF)  admins are trusted

An admin can register a connection pointing at any host:port, and the server
will connect to it. That's inherent to a database manager, but it means an admin
can make the server reach internal/cloud-metadata endpoints. Connection CRUD is
admin-only by design  grant `dbm-admin` only to operators you trust on the
network, and run the container with egress restricted to the databases it needs.

## Known limitations / roadmap

- **Sessions are in-memory:** they're lost on restart and don't shard across
  replicas. Run a single instance, or move sessions to the shared Valkey (on the
  roadmap) before scaling out.
- **Rate limiting** covers the auth endpoints (per-IP) and the authenticated
  surface (a generous per-user backstop). It is a floor, not a substitute for an
  edge WAF on a hostile-internet deployment.
- **Per-connection grants are opt-in** (`DBM_SCOPED_ACCESS`, see above) and key
  off Keycloak groups / realm roles. There is not yet a per-database or
  per-schema scope below the connection level, nor a per-connection `admin` grant
  (connection CRUD stays a global-admin power).

## Reporting

Found something? Email to_be_defined_email with details and a PoC if you
have one. Please don't open a public issue for an unpatched vulnerability.

## Maintainer checklist

- [ ] `DBM_DEV_MODE` is **unset/false** in every non-local environment.
- [ ] `DBM_ENC_KEY` is set to a stable 32-byte key (`openssl rand -hex 32`).
- [ ] Every connection uses a **least-privileged** DB role (no superuser).
- [ ] TLS terminates in front; `DBM_BASE_URL` is `https`.
- [ ] Remote Postgres connections use `sslmode=verify-full`.
- [ ] `dbm-admin` is granted only to trusted operators.
- [ ] `govulncheck ./...` is clean (CI runs it; see `make vuln`).
