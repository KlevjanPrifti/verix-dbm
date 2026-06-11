# Phase 1: Per-Connection RBAC

**Status:** Complete
**Flag:** `DBM_SCOPED_ACCESS` (default `false`)
**Surfaces touched:** `internal/store`, `internal/auth`, `internal/config`, `internal/web`, SPA

## Summary

By default every verix-dbm role is global: a `dbm-read` user can read every
registered connection, a `dbm-write` user can write to all of them. Phase 1 adds
an opt-in mode where non-admin users reach a connection only through an explicit
**grant**. A grant maps a Keycloak group or realm role to `read` or `write` on a
single connection.

The feature ships behind `DBM_SCOPED_ACCESS`. With it off (the default), behaviour
is identical to before this phase, so upgrading is a no-op. With it on, access
narrows to what has been granted.

## Configuration

| Env var | Default | Effect |
| --- | --- | --- |
| `DBM_SCOPED_ACCESS` | `false` | `true` switches non-admin users to per-connection grants. |

Interactions with existing flags:

- **Global admins** (`OIDC_ADMIN_ROLE`) always bypass scoping: they see and manage
  every connection regardless of grants.
- **`DBM_OPEN_READ`** still grants the global read capability to any authenticated
  realm user. With scoping on, that capability is the ceiling, but a non-admin
  still needs a grant to reach a given connection.

To populate the `groups` claim used by grants, configure a Keycloak **Group
Membership** mapper on the client (token claim name `groups`). Realm-role names
also work as grant subjects with no extra mapper.

## Access model

A grant scopes **where** a user acts. It never raises **what** a user may do above
their global capability. The two combine as a cap, not a sum.

Effective access is computed by the pure function `ResolveConnAccess` in
[internal/web/access.go](../../internal/web/access.go):

| Mode | Global capability | Grant on connection | Result |
| --- | --- | --- | --- |
| Not scoped | any | (ignored) | Global capability applies to every connection (legacy). |
| Scoped | admin | (ignored) | Full access (admin bypasses scoping). |
| Scoped | read or write | none | No access. |
| Scoped | read | read or write | Read only. |
| Scoped | write | read | Read only (grant scopes down). |
| Scoped | write | write | Read + write. |

Key property: a `read`-role user with a `write` grant still only reads. Write
requires **both** the global write capability **and** a write-level grant.

Connection management (create / update / delete) and database-role operations
remain **global-admin only**. There is no per-connection "admin" grant in this
phase.

## Data model

New table `connection_grants` in [internal/store/store.go](../../internal/store/store.go):

```sql
CREATE TABLE connection_grants (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  conn_id    INTEGER NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
  subject    TEXT NOT NULL,   -- Keycloak group path or realm-role name
  level      TEXT NOT NULL,   -- 'read' | 'write'
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(conn_id, subject)
);
```

- `ON DELETE CASCADE` removes a connection's grants when the connection is deleted
  (foreign keys are enabled in the SQLite DSN).
- `UNIQUE(conn_id, subject)` makes `SetGrant` an upsert: re-granting a subject
  replaces its level rather than adding a row.

Store methods added: `ListGrants`, `SetGrant`, `DeleteGrant`, `GrantForSubjects`
(returns the highest-level grant matching any of a user's subjects, write
outranking read), and `ListConnectionsForSubjects` (the connections a subject set
can see).

## Enforcement

Every per-connection handler resolves its target through one helper, so
enforcement lives in one place rather than being sprinkled across handlers.

- **Read** is gated inside `connFor` in
  [internal/web/server.go](../../internal/web/server.go). Because `apiPGPool` and
  `apiRequireWrite` both call `connFor`, this single chokepoint covers browse,
  query, grid, explorer, redis, generate/doc, and DDL-prefill paths. An
  inaccessible connection returns the **same error as a missing one**, so scoped
  mode never discloses which connections exist to a user who cannot see them.
- **Write** is checked per connection in `apiRequireWrite`, plus the query,
  exec-tx, redis-command, and DDL-form paths in
  [internal/web/api.go](../../internal/web/api.go). The read-only signal that drives
  Postgres read-only transactions and the Redis command allowlist is now derived
  from per-connection access, not the global role.
- **Connection list** (`apiListConnections`) returns only visible connections in
  scoped mode (`ListConnectionsForSubjects`), so the sidebar shows a user only
  what they can reach.

`Server.access(...)` is the per-request resolver; it queries grants only when
scoping is on and the user is not a global admin, then defers to
`ResolveConnAccess`.

## API

All grant endpoints require the global **admin** role.

| Method | Path | Body | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/connections/{id}/grants` | - | List grants on a connection. |
| `PUT` | `/api/connections/{id}/grants` | `{"subject":"...","level":"read"|"write"}` | Upsert a grant. |
| `DELETE` | `/api/connections/{id}/grants/{gid}` | - | Remove a grant (scoped to its connection). |

Mutations require the `X-CSRF-Token` header (the standard SPA CSRF gate). Invalid
levels are rejected with `400`. `/api/me` now includes `"scopedAccess": bool` so
the SPA can show or hide the grants UI.

## Admin UI

When `DBM_SCOPED_ACCESS` is on and the viewer is an admin, the connection edit
dialog ([ConnModal.tsx](../../internal/web/spa/src/components/ConnModal.tsx)) shows an
**Access grants** panel
([GrantsPanel.tsx](../../internal/web/spa/src/components/GrantsPanel.tsx)): list
existing grants, add a subject with a read/write level, or remove one. Subjects
are free text (a group path like `/team-a` or a realm role like `dbm-write`); the
panel does not query Keycloak's directory.

## Audit

Grant changes are written to the audit log:

- `grant_set` with detail `subject=level`
- `grant_delete` with the grant id

These appear in the admin Audit view alongside the existing connection and query
events.

## Rollout and rollback

- **Forward:** the migration only adds a new table; `connections` and `audit` are
  untouched. Deploy with the flag unset for zero behaviour change, then flip
  `DBM_SCOPED_ACCESS=true` once grants are in place.
- **Backward:** an older binary ignores the `connection_grants` table; no
  destructive change is required to roll back. Turning the flag off reverts to
  global-role behaviour without data loss (grants remain stored, just unused).

Recommended enablement order: set grants on each connection first (manageable even
while the flag is off), confirm the intended teams appear, then enable the flag.

## Testing

- [internal/web/access_test.go](../../internal/web/access_test.go): a 10-case truth
  table over `ResolveConnAccess` covering every (scoped, global capability, grant)
  combination.
- [internal/store/grants_test.go](../../internal/store/grants_test.go): grant upsert,
  highest-level selection across subjects, connection-list filtering,
  cascade-on-delete, and connection-scoped delete.

Verified locally: `make build`, `go vet ./...`, `go test ./...`, plus an
end-to-end smoke test of the grants API (set / list / invalid-level reject /
delete).

## Limitations and follow-ups

- No per-database or per-schema scope below the connection level.
- No per-connection `admin` grant; connection CRUD stays global-admin.
- The handler-level enforcement path for a scoped **non-admin** user is covered by
  the unit and store tests but not by a live end-to-end test, since dev mode
  auto-logs-in as admin. A full E2E would need an OIDC test harness (folded into
  the Phase 6 integration-test work).
