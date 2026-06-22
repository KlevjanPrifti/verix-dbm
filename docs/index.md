---
title: Home
nav_order: 1
---

# verix-dbm documentation

This folder is the home for verix-dbm's reference documentation. Start here to find the page you need, then follow the cross-links between pages.

## What verix-dbm is

verix-dbm is a governed front door to your databases: a single static Go binary with an IDE-style React workbench baked in (embedded via `go:embed`), so one process serves both the JSON API and the UI. It puts SSO (Keycloak OIDC), role-based access control (deny-by-default admin / write / read, with optional per-connection grants), an audit log of every mutating action (admin-viewable, CSV/JSON export, secrets redacted), and AES-256-GCM encryption of saved credentials in front of PostgreSQL, MySQL/MariaDB, SQLite, Redis/Valkey, and MongoDB. It is fail-closed by design: in production it refuses to start without OIDC and an encryption key, and it never exposes stored passwords to the browser.

## Documentation map

| Page | What it covers |
|------|----------------|
| [Getting started](getting-started.md) | First run: build the SPA + binary, dev mode (`DBM_DEV_MODE=true`), the Vite dev server, and connecting your first database. |
| [Configuration](configuration.md) | Full environment-variable reference (names, defaults, types) and the fail-closed validation order in `config.Load()` (`internal/config/config.go`). |
| [Architecture](architecture.md) | Process bootstrap, the chi router + middleware stack, embedded SPA serving, the `/api` route table, and the connection registry (`cmd/server/main.go`, `internal/web/server.go`, `internal/conn/registry.go`). |
| [Security](security.md) | OIDC authorization-code + PKCE flow, RBAC and per-connection grants, CSRF, AES-GCM keyring + rotation, security headers/CSP, rate limiting, and the SSRF egress guard. |
| [Database engines](database-engines.md) | The engine families, the `dbsql.Engine` interface, shared guardrails (30s timeout, 1000-row cap, destructive-statement gate, read-only enforcement), and per-engine specifics. |
| [API reference](api-reference.md) | Every JSON endpoint under `/api` (and `GET /c/{id}/export`): method, path, capability gate, CSRF requirement, and behavior (`internal/web/api_*.go`). |
| [Frontend](frontend.md) | The React + TypeScript + Vite SPA under `internal/web/spa`: app shell, app context, API client, tab kinds, the Explorer tree, and components. |
| [Deployment](deployment.md) | The multi-stage `Dockerfile`, [Makefile](https://github.com/KlevjanPrifti/verix-dbm/blob/main/Makefile) targets, the demo and Dokploy compose files, and the CI/release workflow. |
| [Observability](observability.md) | Structured request logging, Prometheus metrics at `/metrics`, `/healthz` + `/readyz` probes, and audit mirroring to the structured log for SIEM. |
| [Data model](data-model.md) | The metadata store (`internal/store`): schema for `connections`, `connection_grants`, and `audit`, plus the SQLite/Postgres backends and DSN builders. |
| [Development](development.md) | Building, testing, and extending verix-dbm: the `make` targets, running a single test, and the recipes for adding a new engine. |

## Start here

- New to verix-dbm? Read [Getting started](getting-started.md) first, then [Architecture](architecture.md) for the big picture.
- Operators and deployers: read [Deployment](deployment.md) for topology and images, then [Configuration](configuration.md) for the exact env vars (required-in-production: `DBM_ENC_KEY` or `DBM_ENC_KEYS`, the `OIDC_*` set, and `DBM_BASE_URL`). Pair these with [Security](security.md) and [Observability](observability.md) when hardening.
- Contributors: read [Development](development.md) and [Architecture](architecture.md), and consult [Database engines](database-engines.md) before adding an engine and [Data model](data-model.md) before touching the metadata store.

## At a glance

- One process, one binary: the JSON API is mounted at `/api` (`internal/web/api.go`); the SPA is served at `/app` and `/` redirects (HTTP 302) to `/app`.
- Deny-by-default RBAC: an authenticated user with no `read` capability gets HTTP 403; production refuses to start without OIDC unless `DBM_DEV_MODE=true` is set explicitly.
- Pluggable backends: the metadata store is SQLite (default) or Postgres (`DBM_STORE_DRIVER=postgres`), and sessions are in-memory (default) or Redis (`DBM_SESSION_BACKEND=redis`), so several replicas can run behind a load balancer.
- Guardrails on query paths: 30s statement timeout, 1000-row result cap, and a confirmation gate for destructive statements (`DROP`/`TRUNCATE`, unguarded `DELETE`/`UPDATE`).

## Repo root references

- [README.md](https://github.com/KlevjanPrifti/verix-dbm/blob/main/README.md): project overview and quickstart.
- [SECURITY.md](https://github.com/KlevjanPrifti/verix-dbm/blob/main/SECURITY.md): the security policy and how to report a vulnerability (the security model is [here](security.md)).
- [.env.example](https://github.com/KlevjanPrifti/verix-dbm/blob/main/.env.example): a commented template of every configuration variable.
- [Makefile](https://github.com/KlevjanPrifti/verix-dbm/blob/main/Makefile): build, test, vet, and vulnerability-scan targets.
