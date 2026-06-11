# Phase 2: Operability

**Status:** Complete
**Flags:** `DBM_LOG_LEVEL`, `DBM_LOG_FORMAT`, `DBM_METRICS_TOKEN`, `DBM_AUDIT_RETENTION_DAYS`
**Surfaces touched:** `cmd/server`, `internal/config`, `internal/store`, `internal/web`, SPA

## Summary

In a self-hosted deployment the customer's SRE team runs the instance, so the app
has to plug into their logging, metrics, and monitoring stacks. This phase adds
the four things an ops team needs before they will accept it into production:
structured logs, Prometheus metrics, a real readiness probe, and an audit trail
they can ship to a SIEM and retain on a policy.

None of it changes application behaviour; it makes the running instance
observable.

## Configuration

| Env var | Default | Effect |
| --- | --- | --- |
| `DBM_LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error`. |
| `DBM_LOG_FORMAT` | `json` | `json` for log shippers, `text` for local dev. |
| `DBM_METRICS_TOKEN` | (empty) | When set, `/metrics` requires it as a Bearer token. |
| `DBM_AUDIT_RETENTION_DAYS` | `0` | Purge audit rows older than N days. `0` keeps them forever. |

## Structured logging

The process logger is `log/slog`, configured in
[cmd/server/main.go](../../cmd/server/main.go) and set as the default. JSON output
by default so a log shipper (Fluent Bit, Vector, the container runtime) can
forward it; text output for local development.

A single combined middleware,
[`observe`](../../internal/web/observ.go), emits one structured line per request
with `method`, matched `route`, `path`, `status`, `bytes`, `duration_ms`, client
`ip`, a `request_id` (from chi's `RequestID` middleware), and `user` when
authenticated. Level escalates with status: 5xx logs at error, 4xx at warn, the
rest at info. The operational endpoints (`/healthz`, `/readyz`, `/metrics`) are
skipped so frequent probes and scrapes do not drown the signal.

Lifecycle and audit events are logged too (startup, shutdown, retention purges,
and every audit event, see below).

## Metrics

A Prometheus endpoint is served at `/metrics`
([internal/web/observ.go](../../internal/web/observ.go)). The registry includes the
standard Go runtime and process collectors (memory, goroutines, GC, file
descriptors, CPU) plus app-level series:

| Metric | Type | Labels |
| --- | --- | --- |
| `verixdbm_http_requests_total` | counter | `method`, `route`, `status` |
| `verixdbm_http_request_duration_seconds` | histogram | `method`, `route` |
| `verixdbm_auth_logins_total` | counter | `result` (`success` / `failure`) |

The `route` label is the **matched chi route pattern** (for example
`/api/c/{id}/grid`), not the raw path, so cardinality stays bounded no matter how
many connections or ids are in play. The same `observe` middleware records both
the counter and the histogram.

Auth outcomes are fed from the audit pipeline (see below), so login success and
failure are counted wherever they originate.

**Access:** `/metrics` is served unauthenticated by default, which is normal when
the port is only reachable on a private network or scraped by an in-cluster
Prometheus. Set `DBM_METRICS_TOKEN` to require a Bearer token (compared in
constant time).

## Readiness and liveness

- `/healthz` stays a static **liveness** check: the process is up.
- `/readyz` is a **readiness** check that pings the metadata store
  ([`Store.Ping`](../../internal/store/store.go)) with a 2s timeout and returns
  `503` with a reason if it is unreachable, `200` otherwise. OIDC reachability is
  validated once at startup (the provider's discovery document is fetched in
  `auth.New`), so it is not re-probed on every readiness check.

Both are suitable for Kubernetes `livenessProbe` / `readinessProbe`.

## Audit: forwarding, export, retention

Three additions make the existing audit log enterprise-usable:

1. **SIEM forwarding via structured logs.** The store gained an `OnAudit` sink;
   `main` wires it to [`Server.ObserveAudit`](../../internal/web/observ.go), which
   mirrors every audit event to the structured log as an `audit` record (action,
   user, conn_id, detail, success) and feeds the auth-outcome metric. For a
   self-hosted app this is the idiomatic forwarding path: the customer's existing
   log pipeline carries audit events to their SIEM with no extra integration.
2. **Bulk export.** `GET /api/audit/export?format=jsonl|csv` (admin only) streams
   the **entire** audit log for forensics or one-off SIEM ingestion, via
   `Store.IterAudit` so it is not buffered in memory. The admin Audit modal has
   Export JSONL / Export CSV buttons.
3. **Retention.** When `DBM_AUDIT_RETENTION_DAYS > 0`, a background goroutine in
   `main` (`retainAudit`) purges audit rows older than the window at startup and
   daily thereafter, via `Store.PurgeAuditOlderThan`. `0` (default) keeps
   everything.

Credential redaction in audit details (`PASSWORD '...'`, Redis `AUTH` /
`requirepass`) from before this phase still applies, so exports and forwarded
events never carry secrets.

## Dependency

This phase adds `github.com/prometheus/client_golang` (pure Go, builds into the
static `CGO_ENABLED=0` binary). It is the standard choice for correct histogram
semantics and the free Go/process collectors that ops teams expect.

## Testing

- [internal/store/audit_test.go](../../internal/store/audit_test.go): `Ping`, the
  `OnAudit` sink firing, `PurgeAuditOlderThan` removing only old rows, and
  `IterAudit` ordering.
- [internal/web/observ_test.go](../../internal/web/observ_test.go): infra-path
  detection, `ObserveAudit` feeding the auth metric, and the `/metrics` Bearer
  token gate (401 without, 200 with, open when unset).

Verified locally with `make build`, `go vet ./...`, `go test ./...`, and an
end-to-end smoke test: `/healthz` 200, `/readyz` ok, `/metrics` 401 without token
and 200 with (Go collectors + `verixdbm_*` series present), audit export in both
formats, and structured request/audit logs with request IDs.

## Limitations and follow-ups

- No OpenTelemetry tracing yet; request IDs are logged but not propagated as W3C
  trace context. A tracing exporter is a natural follow-up.
- Audit forwarding relies on the customer's log pipeline. A direct webhook/syslog
  sink (POST per event) could be added if a deployment cannot ship stdout logs.
- `/readyz` checks the store only; it does not verify each registered database
  target (those are checked lazily on use and surfaced per-connection).
