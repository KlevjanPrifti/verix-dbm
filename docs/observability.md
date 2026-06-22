# Observability

verix-dbm exposes a small, SRE-focused operational surface from a single file ([internal/web/observ.go](../internal/web/observ.go)): structured request logs, Prometheus metrics, liveness and readiness probes, and audit events mirrored to the log for SIEM forwarding. This page documents each, with exact env vars, route paths, metric names, and the gotchas you will hit in production.

## Structured request logging

The process logger is built once at startup in `newLogger` ([cmd/server/main.go](../cmd/server/main.go)) and writes to **stderr** using the Go standard library `log/slog`. The logger is held on the server and used for request logs, audit mirroring, and a handful of lifecycle messages.

### Configuration

| Env var | Config field | Default | Effect |
|---|---|---|---|
| `DBM_LOG_LEVEL` | `LogLevel` | `info` | Minimum slog level: `debug`, `info`, `warn`, or `error`. Any unrecognized value falls through to `info` (it is not an error). |
| `DBM_LOG_FORMAT` | `LogFormat` | `json` | Output handler: the literal lowercase `text` selects the slog text handler; anything else (including unset or garbage) uses the JSON handler. Comparison is `strings.ToLower(cfg.LogFormat) == "text"`. |

JSON is the default because it ships cleanly into a log aggregator or SIEM. Text is friendlier for local development and is what the demo compose file uses (`DBM_LOG_FORMAT: text`). Neither value is validated in `config.Load()`; the consumer decides.

### The per-request log line

Request logs are emitted by the `observe` middleware (`(*Server).observe`), wired globally in [internal/web/server.go](../internal/web/server.go) via `r.Use(s.observe)`. It runs after `RequestID` and `Recoverer` (and after `RealIP`, which is only enabled when `DBM_TRUST_PROXY=true`), and before `securityHeaders`. Each completed request produces one line with the message `http_request`.

The log severity is derived from the HTTP **status code**, not from the configured `DBM_LOG_LEVEL`:

| Status range | slog level |
|---|---|
| `>= 500` | `error` |
| `400` to `499` | `warn` |
| everything else (1xx/2xx/3xx) | `info` |

Fields (slog attributes), in order:

| Field | Value | Notes |
|---|---|---|
| `method` | HTTP method | |
| `route` | matched chi route pattern | From `chi.RouteContext(...).RoutePattern()`. Falls back to `other` when empty (unmatched / 404), so the raw path never becomes a label and cardinality stays bounded. |
| `path` | `r.URL.Path` | The raw request path. |
| `status` | int HTTP status | Coerced to `200` when the handler wrote nothing (status `0`). |
| `bytes` | response bytes written | |
| `duration_ms` | request latency in milliseconds | |
| `ip` | client IP | From `clientIP(r)`: the host portion of `RemoteAddr` (port stripped). It does not itself read forwarded headers. |
| `request_id` | request id | Set by the chi `RequestID` middleware. |
| `user` | user email | Appended **only** when an authenticated user is present in the context and their email is non-empty; absent for unauthenticated requests. |

#### Client IP and proxies

`clientIP` returns the direct peer address from `RemoteAddr`. Trusting `X-Forwarded-For` / `X-Real-IP` is gated entirely by `DBM_TRUST_PROXY=true`, which enables the chi `RealIP` middleware. Behind a reverse proxy with `DBM_TRUST_PROXY` unset, the logged `ip` (and the rate-limit key) is the proxy's address, not the real client. Only enable `DBM_TRUST_PROXY` behind a proxy that overwrites those headers, otherwise clients can spoof their address. See [Configuration](configuration.md) for details.

### Paths excluded from logging (and metrics)

`isInfraPath(p)` matches exactly these three paths and short-circuits the `observe` middleware before any logging or metrics:

```
/healthz
/readyz
/metrics
```

These endpoints are scraped and probed frequently; logging them per-request would drown the signal. The trade-off: request rate and latency for the probes and the metrics scrape itself are **not** visible in the `verixdbm_http_*` metrics.

### Logging gotchas

- Because request severity follows the HTTP status, running at `DBM_LOG_LEVEL=warn` drops all normal 2xx access logs (only 4xx becomes warn, 5xx becomes error). Keep `info` if you want an access log.
- The three infra paths produce neither access logs nor HTTP metrics, so probe/scrape traffic is invisible there.
- Audit mirror lines (see below) are emitted at `info`, so `warn`/`error` levels suppress them too.

## Prometheus metrics

The `/metrics` endpoint serves Prometheus exposition. Metrics live on a **private** registry built in `newMetrics()` (`prometheus.NewRegistry()`), not the global default registry. Anything registered on `prometheus.DefaultRegisterer` elsewhere would not appear here.

### Registered collectors

Two standard collectors are registered, so the Go runtime and process families are exported for free:

- `collectors.NewGoCollector()` -> the `go_*` families (memory, goroutines, GC).
- `collectors.NewProcessCollector(...)` -> the `process_*` families (file descriptors, CPU, resident memory).

### App-level metrics (exact names)

All app metrics use the namespace `verixdbm`, so every name is prefixed `verixdbm_`. Verified against [internal/web/observ.go](../internal/web/observ.go):

| Metric name | Type | Labels | Description |
|---|---|---|---|
| `verixdbm_http_requests_total` | counter | `method`, `route`, `status` | Total HTTP requests by method, matched route, and status code. |
| `verixdbm_http_request_duration_seconds` | histogram | `method`, `route` | HTTP request latency by method and matched route. Buckets are `prometheus.DefBuckets` (`.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10`). |
| `verixdbm_auth_logins_total` | counter | `result` | Login outcomes by result. `result` is `success` or `failure`. |

Label details:

- `status` is the decimal string of the integer status code.
- `route` is the matched chi route pattern, or `other` for unmatched requests, mirroring the log field and bounding cardinality.
- `verixdbm_auth_logins_total` is incremented only through audit mirroring (`ObserveAudit`), not in the request middleware. A successful login (`auth_login`) increments `result="success"`; a failed one (`auth_login_failed`) increments `result="failure"`.

### Optional Bearer token gate

The `/metrics` endpoint is protected only by an optional Bearer token; the main OIDC / RBAC / CSRF / session stack does **not** apply to it (it is registered before the auth-protected groups and is an infra path).

| Env var | Config field | Default | Effect |
|---|---|---|---|
| `DBM_METRICS_TOKEN` | `MetricsToken` | empty (unset) | When set, `/metrics` requires `Authorization: Bearer <token>`. When empty, `/metrics` is open. |

Behavior of `metricsHandler`:

- **Token unset (`""`)**: `/metrics` is open, no authentication. This is fail-open by design, suitable when the metrics port is reachable only on a private network.
- **Token set**: the handler strips the `Bearer ` prefix from the `Authorization` header and compares it to the configured token with a timing-safe (`crypto/subtle.ConstantTimeCompare`) comparison. A mismatch sets `WWW-Authenticate: Bearer` and returns `401 unauthorized`.

If the metrics port is publicly reachable, set `DBM_METRICS_TOKEN`.

### Sample Prometheus scrape config

Unauthenticated (token unset, private network):

```yaml
scrape_configs:
  - job_name: verix-dbm
    metrics_path: /metrics
    static_configs:
      - targets: ['verix-dbm:8080']
```

With a Bearer token (`DBM_METRICS_TOKEN` set):

```yaml
scrape_configs:
  - job_name: verix-dbm
    metrics_path: /metrics
    scheme: https
    authorization:
      type: Bearer
      credentials: 'YOUR_DBM_METRICS_TOKEN'
    static_configs:
      - targets: ['dbm.example.com']
```

## Health and readiness probes

Both probes are registered in [internal/web/server.go](../internal/web/server.go), are unauthenticated by design (outside the auth groups), and are infra paths (no request logging, no HTTP metrics).

### `GET /healthz` (liveness)

Static liveness check. The inline handler always returns `200` with the plain-text body `ok`. It signals only that the process is up and serving HTTP; it performs no dependency checks. Use it for a load balancer or orchestrator liveness probe.

### `GET /readyz` (readiness)

Readiness check that reflects the ability to actually serve. `(*Server).readyz` pings the metadata store (`s.st.Ping(ctx)`) with a `2s` timeout derived from the request context.

| Outcome | HTTP status | Body |
|---|---|---|
| store reachable | `200` | `{"status":"ok"}` |
| store ping error | `503 Service Unavailable` | `{"status":"unavailable"}` |

On failure, the raw store error is **not** returned to the caller (it could disclose DSN fragments or the metadata host on this unauthenticated endpoint). The detail is logged server-side at `warn` with the message `readyz_store_unavailable` and an `error` attribute.

OIDC reachability is validated once at startup and is intentionally **not** re-probed by `/readyz`. The probe therefore covers the metadata store ([internal/store/store.go](../internal/store/store.go) `Ping`), not the user-registered target databases or the identity provider.

### Container note

The runtime image is distroless with no shell, so a container-level `HEALTHCHECK` is not possible. Probe `/healthz` and `/readyz` externally (for example from Traefik or your orchestrator). See [Deployment](deployment.md).

## Audit events mirrored to the structured log (SIEM)

verix-dbm writes every mutating action to its audit log in the metadata store. To make those events available to a SIEM without a second integration, the server mirrors each audit row to the structured log as it is written.

The mechanism: `cmd/server/main.go` registers `st.OnAudit(srv.ObserveAudit)`. The store's audit sink is invoked synchronously after each audit row is inserted, so both authentication events and handler events flow through `(*Server).ObserveAudit`.

`ObserveAudit` emits one `info`-level line with the message `audit` and these attributes:

| Attribute | Source |
|---|---|
| `action` | the audit action string (for example `sql_query`, `create_connection`, `cred_access`, `auth_login`) |
| `user` | the acting user |
| `conn_id` | the connection id (`0` for non-connection events) |
| `detail` | the redacted detail string |
| `success` | boolean outcome |

Notes:

- The audit row's `ID` and `TS` are not included in this attribute set; slog adds its own timestamp.
- These lines are `info`, so they are suppressed when `DBM_LOG_LEVEL` is `warn` or `error`. Keep `info` to forward audit events.
- The same handler also feeds `verixdbm_auth_logins_total` for the `auth_login` / `auth_login_failed` actions; all other actions are logged but touch no metric.

For the full audit record shape (the `Audit` type, the complete list of action strings, and where rows are stored) see [Data model](data-model.md). Detail redaction (SQL passwords, JSON `password`/`pwd`, Redis `requirepass`/`AUTH`, then truncation to 500 chars) happens at the call sites before the row is written, so the mirrored log line and any export are already redacted; see [Security](security.md) and [../SECURITY.md](../SECURITY.md).

## Environment variable summary

| Env var | Config field | Default | Purpose |
|---|---|---|---|
| `DBM_LOG_LEVEL` | `LogLevel` | `info` | slog minimum level: `debug` / `info` / `warn` / `error` (unknown falls back to `info`). |
| `DBM_LOG_FORMAT` | `LogFormat` | `json` | `text` selects the text handler; anything else uses JSON. |
| `DBM_METRICS_TOKEN` | `MetricsToken` | empty | When set, requires a Bearer token on `/metrics`; when empty, `/metrics` is open. |
| `DBM_TRUST_PROXY` | `TrustProxy` | `false` | When `true`, enables chi `RealIP` so the logged `ip` and the rate-limit key derive from `X-Forwarded-For` / `X-Real-IP`. |

## See also

- [Configuration](configuration.md) - the full env var reference and defaults.
- [Deployment](deployment.md) - container, compose, and CI/release topology, plus how the probes fit behind a proxy.
- [Security](security.md) and [../SECURITY.md](../SECURITY.md) - auth, RBAC, audit detail redaction, and the security header stack.
- [Data model](data-model.md) - the audit record shape and action strings.
- Repo root: [../README.md](../README.md), [../.env.example](../.env.example).
