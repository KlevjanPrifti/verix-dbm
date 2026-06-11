# Phase 4: Per-Customer HA

**Status:** Complete
**Flags:** `DBM_SESSION_BACKEND`, `DBM_SESSION_REDIS_URL`, `DBM_STORE_DRIVER`, `DBM_STORE_DSN`, `DBM_PG_POOL_MAX_CONNS`
**Surfaces touched:** `internal/auth`, `internal/store`, `internal/conn`, `internal/config`, `cmd/server`

## Summary

A self-hosted enterprise customer wants to run more than one replica behind their
load balancer: survive a restart, tolerate a node failure, roll out upgrades with
no downtime. Two things blocked that, both now addressed:

- **Sessions were in-process** (an in-memory map), so a second replica could not
  serve a session created on the first, and a restart logged everyone out.
- **Metadata was single-node SQLite** with a single writer, a single point of
  failure for the connection registry and audit log.

This phase makes both **pluggable**: in-memory + SQLite remain the zero-dependency
defaults for single-node deployments, and Redis sessions + a Postgres metadata
backend can be switched on for HA. It also makes the per-target Postgres pool size
configurable. No new dependencies: Redis (go-redis) and Postgres (pgx) were
already in the tree.

## Distributed sessions

Sessions now go through a `sessionStore` interface
([internal/auth/sessions.go](../../internal/auth/sessions.go)) with two
implementations:

- **memory** (default): the original in-process map, self-reaping hourly. Single
  node; sessions are lost on restart.
- **redis**: sessions are JSON-serialized into a shared Redis/Valkey keyspace
  (`dbm:sess:<token>`) with a TTL equal to the session lifetime. Any replica
  reads any session, and sessions survive a restart. Redis is dialed and pinged
  at startup, so a misconfiguration fails the process rather than first login.

| Env var | Default | Meaning |
| --- | --- | --- |
| `DBM_SESSION_BACKEND` | `memory` | `memory` or `redis`. |
| `DBM_SESSION_REDIS_URL` | (empty) | `redis://[:pass@]host:port/db` when backend is `redis`. |

The OAuth login-state stash and CSRF tokens ride the same store, so the whole
auth flow works across replicas.

## Postgres metadata backend

`internal/store` now drives either SQLite or Postgres from one codebase
([internal/store/store.go](../../internal/store/store.go)):

- Queries are written once with `?` placeholders and rebound to `$N` for Postgres.
- `CreateConnection` uses `RETURNING id`, which both engines support, avoiding the
  `LastInsertId` divergence.
- The only schema differences are id columns (`INTEGER PRIMARY KEY AUTOINCREMENT`
  vs `BIGSERIAL`) and `conn_id` width; both are switched in `migrate`.
- The audit `user` column is quoted (`"user"`) since it is reserved in Postgres;
  SQLite accepts the quoted identifier too.
- SQLite keeps its single-writer pragma; Postgres allows concurrent writers, so
  replicas share one metadata database.

| Env var | Default | Meaning |
| --- | --- | --- |
| `DBM_STORE_DRIVER` | `sqlite` | `sqlite` or `postgres`. |
| `DBM_STORE_DSN` | (empty) | pgx/libpq DSN when driver is `postgres`. |

SQLite remains the default and is fine for many deployments; Postgres is opt-in
for those that need replicated/HA metadata.

## Configurable connection pool

`DBM_PG_POOL_MAX_CONNS` (default 4) sets the pooled connection cap the registry
opens to each registered Postgres **target** (not the metadata store), passed in
via `conn.NewRegistry` ([internal/conn/registry.go](../../internal/conn/registry.go)).
Raise it for busier deployments.

## The HA shape

With `DBM_SESSION_BACKEND=redis` + `DBM_STORE_DRIVER=postgres`, run N identical
replicas behind a load balancer:

- any replica serves any user session (shared Redis),
- all replicas see the same connections, grants, and audit log (shared Postgres),
- a replica can be restarted or replaced with no session loss and no downtime.

## Testing

- [internal/auth/sessions_test.go](../../internal/auth/sessions_test.go): the
  in-memory store (put/get/delete/expiry), backend selection, and a Redis
  round-trip (gated on `DBM_TEST_REDIS_URL`).
- [internal/store/postgres_integration_test.go](../../internal/store/postgres_integration_test.go):
  the full store surface against a real Postgres (gated on `DBM_TEST_PG_DSN`):
  connection CRUD with `RETURNING id`, grants + upsert + `ON DELETE CASCADE`,
  audit insert/list/iter/purge exercising the reserved-word `"user"` quoting and
  `RowsAffected`.

Gated tests skip cleanly when the env vars are unset, so CI without those services
stays green. Verified against live Redis and Postgres containers, including an
**end-to-end two-replica run**: both replicas (shared Redis + shared Postgres)
served the identical session token, and a connection created on replica A was
immediately visible on replica B.

Verified with `make build`, `go vet ./...`, and the full `go test ./...` (with and
without the integration env vars).

## Limitations and follow-ups

- Postgres migrations are create-only (`IF NOT EXISTS`), matching the existing
  SQLite approach. A versioned migration tool is a Phase 5 packaging concern.
- No automatic SQLite-to-Postgres data migration; switching backends starts fresh
  (export/import is a manual step if a customer migrates an existing instance).
- Redis is assumed reachable and trusted on the deployment network; sessions are
  stored as JSON (no application-layer encryption of the session blob).
