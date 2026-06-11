# Phase 3: Secrets & Key Management

**Status:** Complete
**Flags:** `DBM_ENC_KEYS` (multi-key rotation); `DBM_ENC_KEY` unchanged
**Surfaces touched:** `internal/crypto`, `internal/config`, `internal/store`, `internal/conn`, `internal/web`, `cmd/server`, SPA

## Summary

Saved connection passwords are AES-256-GCM encrypted at rest. Before this phase
the encryption key was a single static env var: there was no way to rotate it,
and changing it made every stored credential unreadable. This phase makes the key
**versioned and rotatable without downtime**, exposes a **re-encrypt** operation
to roll data forward, adds a **pluggable key-provider** seam for external KMS /
Vault, and **audits every use of a stored credential**.

It is fully backward compatible: existing single-key deployments keep working
with no config change, and ciphertext written by older builds still decrypts.

## How key versioning works

Ciphertext is now stored as `<keyID>$<base64(nonce||ciphertext)>`. The keyID names
which key sealed it. The keyring ([internal/crypto/crypto.go](../../internal/crypto/crypto.go))
holds:

- a **primary** key, used to seal all new writes, and
- any number of **retained** keys, kept only to decrypt data not yet rewritten.

Decryption uses the key named in the prefix. Ciphertext written before this phase
has **no prefix**; the keyring decrypts it by trying each key in turn. AES-GCM is
authenticated, so a wrong-key attempt fails cleanly rather than returning garbage,
which makes try-decrypt safe. This is why upgrading needs no migration.

## Configuration

| Env var | Form | Meaning |
| --- | --- | --- |
| `DBM_ENC_KEY` | single `64-hex` or base64 of 32 bytes | The one key (id `1`). Unchanged behaviour. |
| `DBM_ENC_KEYS` | `id:key,id:key,...` | Multi-key ring. First entry is the **primary**; the rest are retained for decryption. Supersedes `DBM_ENC_KEY` when set. |

Key ids are arbitrary labels (no `$`); the examples use `v1`, `v2`. An empty key
config still generates an ephemeral key with a startup warning (dev only).

## Rotation: the zero-downtime flow

1. Starting state: `DBM_ENC_KEY=<old>`. Stored data is encrypted with `<old>`.
2. **Introduce the new key as primary, keep the old for reads:**
   `DBM_ENC_KEYS="v2:<new>,v1:<old>"` and restart. New and updated credentials are
   now sealed under `v2`; existing data still decrypts under `v1` (or the legacy
   try-path).
3. **Roll data forward:** an admin clicks **Re-encrypt** in the UI (top nav) or
   calls `POST /api/admin/reencrypt`. Every stored credential is decrypted and
   re-sealed under the primary. The endpoint is idempotent: rows already on the
   primary are skipped.
4. **Retire the old key:** once re-encryption reports `rewritten=0` on a fresh
   run, drop the old key: `DBM_ENC_KEYS="v2:<new>"`.

At no point is the service down or are credentials lost. If step 3 is skipped, the
old key simply stays needed; nothing breaks.

## Re-encrypt operation

`POST /api/admin/reencrypt` (admin + CSRF), handled in
[internal/web/api.go](../../internal/web/api.go):

- Iterates all connections, calls `Box.Reencrypt` on each stored ciphertext.
- Writes back only changed rows via `Store.UpdatePasswordEnc` (touches the
  ciphertext column only), and drops any cached pool so the next use re-decrypts.
- Returns `{primaryKey, checked, rewritten, failed}` and writes a `reencrypt`
  audit event with those counts.
- Exposed in the SPA as a **Re-encrypt** admin action with a confirm dialog and a
  result toast.

## Pluggable key provider

The keyring is built from a `Provider`:

```go
type Provider interface {
    Keys(ctx context.Context) (primaryID string, specs []KeySpec, err error)
}
```

The built-in `StaticProvider` serves the env-configured keys. An external
provider (HashiCorp Vault, AWS KMS, Azure Key Vault) implements the same
interface to fetch or unwrap keys at startup, and `crypto.NewFromProvider` builds
the keyring from it, with no change to the rest of the app. Wiring a concrete KMS
provider is a follow-up; the seam is in place so it does not require touching the
encrypt/decrypt paths.

## Credential-access auditing

The connection registry gained an `OnCredentialAccess` hook
([internal/conn/registry.go](../../internal/conn/registry.go)), invoked whenever a
stored password is actually decrypted to open a pool (a cache miss, not on cached
reuse). `main` wires it to write a `cred_access` audit event, which (via Phase 2)
is mirrored to the structured log. Credential-less connections do not fire it.

## Testing

- [internal/crypto/crypto_test.go](../../internal/crypto/crypto_test.go): single-key
  round trip, legacy unprefixed decryption, rotation (old data decrypts under a
  new primary), `Reencrypt` moving data to the primary and being a no-op
  afterwards, unknown-key-id failure, and multi-key parse errors.
- [internal/conn/registry_test.go](../../internal/conn/registry_test.go): the
  credential-access hook fires on decrypt and not for credential-less connections.
- [internal/store/grants_test.go](../../internal/store/grants_test.go):
  `UpdatePasswordEnc` rewrites only the ciphertext.

Verified with `make build`, `go vet ./...`, `go test ./...`, and an end-to-end
rotation smoke test across three restarts: single key -> rotated ring
(`rewritten=1` on first re-encrypt, `0` on second) -> old key dropped
(`rewritten=0`, `failed=0`, proving data is fully readable under the new key
alone).

## Limitations and follow-ups

- No concrete external KMS/Vault provider yet, only the `Provider` seam and the
  static implementation. AWS KMS / Vault providers are the natural next step
  (envelope encryption: KMS holds the master key, the app holds per-record data
  keys).
- Rotation is operator-driven (set keys, click re-encrypt). There is no automatic
  scheduled rotation.
- `cred_access` audits credential decryption, not per-query credential use (each
  query is already audited separately with the acting user).
