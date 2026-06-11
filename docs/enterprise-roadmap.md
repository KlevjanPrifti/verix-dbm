# verix-dbm Enterprise Plan (Self-Hosted, Single-Tenant)

## Guiding principles

- **The customer operates the instance.** Every capability must plug into their IdP, their secrets manager, their SIEM, their metrics stack, not ours.
- **Credentials never leave their network.** This is the core trust promise; preserve it in every decision.
- **Ship in vertical slices.** Each phase is independently sellable, not a big-bang rewrite.

## Status

Each completed phase has a detailed design + usage doc under [docs/phases/](phases/).

| Phase | Title | Status | Doc |
| --- | --- | --- | --- |
| 1 | Per-connection RBAC | Complete | [phase-1-per-connection-rbac.md](phases/phase-1-per-connection-rbac.md) |
| 2 | Operability (logs, metrics, readiness, audit export) | Complete | [phase-2-operability.md](phases/phase-2-operability.md) |
| 3 | Secrets & key management (rotation, KMS/Vault) | Complete | [phase-3-secrets-key-management.md](phases/phase-3-secrets-key-management.md) |
| 4 | Per-customer HA (sessions, optional Postgres metadata) | Not started | - |
| 5 | Packaging, deployment & supply chain | Not started | - |
| 6 | Compliance hardening & test depth | Not started | - |

When a phase lands, add its `docs/phases/phase-N-*.md` and flip its row to Complete.

## Phase 1 - Per-connection RBAC (the #1 blocker)

**Why first:** Even a single enterprise customer has internal teams; "team A can't touch prod-db" is table stakes. The data-model changes here ripple into audit and the API, so doing it first avoids rework.

**Scope:**

- **Data model:** add a `connection_grants` concept in `internal/store` - `(subject, connection_id, level)` where `subject` is an OIDC group/role and `level` is read/write/admin. Keep the existing global roles as a fallback/superadmin.
- **Resolution:** a user's effective access to a connection = `max(global role, matching grants)`. Deny-by-default preserved.
- **Enforcement:** a single chokepoint in `internal/web/api.go` that resolves connection access before any browse/query/DDL handler runs. No per-handler ad-hoc checks.
- **Admin UI:** grant management screen in the SPA; connection list filtered to what the user can see.
- Map grants to Keycloak groups, not local users - keeps provisioning in the customer's IdP.

**Done when:** an admin can grant team A read on conn-X and team B nothing, and the grid/query/DDL paths all enforce it server-side.

## Phase 2 - Operability for someone else's ops team

**Why:** In self-hosted, the customer's SRE team runs this. Without these, they can't accept it into production. Cheap to build, clears most security questionnaires.

**Scope:**

- **Structured logging:** replace `log.Println` with leveled JSON logs (`slog`) - request IDs, user, action, latency.
- **Metrics:** Prometheus `/metrics` - request rate/latency/errors, connection-pool stats, auth outcomes, rate-limit hits.
- **Real readiness probe:** extend `/healthz` (or add `/readyz`) to check SQLite open + Keycloak reachable; keep `/healthz` as liveness.
- **Audit export + retention:** add SIEM forwarding (syslog/webhook/file) and a configurable retention/purge policy for the audit table in `internal/store`. This is the most-requested compliance item.

**Done when:** the instance emits Prometheus metrics, JSON logs, has a dependency-aware readiness check, and audit can be streamed to an external SIEM with a retention policy.

## Phase 3 - Secrets & key management on the customer's terms

**Why:** Enterprises want to own the encryption key and rotate it. Today `DBM_ENC_KEY` is a static env var and rotating it bricks every saved password.

**Scope:**

- **Key rotation:** version the encryption key; store a key-id alongside each ciphertext in `internal/crypto`/`internal/store` so old and new keys coexist and a background re-encrypt can roll forward. This makes rotation non-destructive even before external KMS lands.
- **External KMS/Vault (pluggable):** a `KeyProvider` interface with implementations for env-var (today), HashiCorp Vault, and AWS KMS. Envelope encryption: KMS holds the master key, app holds per-record data keys.
- **Audit credential access:** log when a saved password is decrypted for use.

**Done when:** a customer can point verix-dbm at their Vault/KMS, rotate keys without downtime, and see credential-access events in the audit log.

## Phase 4 - Per-customer HA (not internet-scale)

**Why:** One enterprise wants "survives a restart and runs 2+ replicas behind their LB," not "10k tenants." Much smaller lift than true SaaS scaling.

**Scope:**

- **Distributed sessions:** move the in-memory session map in `internal/auth` to a pluggable store (Redis-backed for HA, in-memory still the default for single-node).
- **Optional Postgres metadata backend:** abstract `internal/store` behind an interface; keep SQLite as the zero-dependency default, add Postgres for customers who want replicated/HA metadata. Don't force it - many customers are fine single-node.
- **Connection-pool tuning:** make `MaxConns` (currently 4 in `internal/conn`) configurable per connection.

**Done when:** two replicas can run behind a load balancer with shared sessions, and metadata can optionally live in Postgres.

## Phase 5 - Packaging, deployment & supply chain

**Why:** Self-hosted buyers deploy into k8s and run supply-chain checks before they'll install anything.

**Scope:**

- **Helm chart (+ raw k8s manifests):** StatefulSet/Deployment, ConfigMap, Secret wiring, Ingress, ServiceMonitor, PodDisruptionBudget.
- **Release automation:** tag-based releases, semver, signed container images (cosign) + SBOM. Enterprises increasingly require these.
- **Upgrade/migration path:** versioned schema migrations for the metadata store with a documented rollback.
- **Deployment docs:** k8s, Docker standalone, and air-gapped install guides.

**Done when:** `helm install verix-dbm` produces a production-ready instance with signed images and an SBOM, and upgrades are documented and migration-safe.

## Phase 6 - Compliance hardening & test depth (ongoing, parallelizable)

**Why:** Procurement asks for evidence, not promises.

**Scope:**

- **Tamper-evident audit:** hash-chain the audit log (each row includes hash of prior) so deletion/edits are detectable.
- **MFA enforcement + SCIM:** enforce MFA via Keycloak policy; add SCIM so offboarding in the IdP auto-revokes access. (SAML can wait unless a specific deal needs it.)
- **Integration & E2E tests:** Keycloak + API + store integration tests; coverage gate in CI (`.github/workflows`).
- **Security testing in CI:** SAST/dependency/container scanning beyond the current govulncheck + npm audit.

**Done when:** audit is tamper-evident, IdP-driven offboarding revokes access automatically, and CI enforces coverage + security scanning.

## What we are explicitly NOT doing (and why)

- **Multi-tenancy / org-workspace model** - unnecessary in single-tenant; the customer's instance is their tenant. Revisit only if/when you add a hosted SaaS tier for SMB.
- **Per-tenant encryption / region sharding** - same reason; self-hosting gives data residency for free.
- **More DB engines** - sales breadth, not enterprise-readiness; sequence it against actual demand, independent of this plan.

## Suggested sequencing

Phase 1 -> 2 -> 3 is the critical path to your first enterprise deal (access control + operability + key custody). Phase 4 unlocks customers who mandate HA. Phases 5 and 6 can run partly in parallel with the others since they're packaging/compliance rather than core feature work.
