// Package config loads runtime configuration from the environment.
package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
)

type Config struct {
	Addr       string // HTTP bind address, e.g. ":8080"
	SQLitePath string // path to the metadata DB file
	// SQLiteDir is the directory SQLite *target* database files must live under
	// (DBM_SQLITE_DIR). Opening a SQLite connection reads a file on the server's
	// filesystem, so this allowlist fences which files are reachable. Empty
	// disables the SQLite engine entirely (fail closed): SQLite is opt-in.
	SQLiteDir string
	EncKey    string // hex/base64 32-byte key for credential encryption ("" => ephemeral)
	// EncKeys is the multi-key form for rotation: "id:key,id:key,..." with the
	// first entry the primary (new writes) and the rest retained for decryption.
	// When set it supersedes EncKey. See DBM_ENC_KEYS.
	EncKeys string
	BaseURL string // public base URL (for OIDC redirect default)

	// OIDC / Keycloak realm roles → capabilities.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	OIDCAdminRole    string // realm role that grants the admin capability
	OIDCWriteRole    string // realm role that grants write capability
	OIDCReadRole     string // realm role that grants read capability

	// OpenRead grants read access to ANY authenticated user, regardless of role
	// (the pre-1.0 behaviour). Off by default: access is deny-by-default and a
	// user needs an explicit read/write/admin role. Set DBM_OPEN_READ=true only
	// when the realm is dedicated to this app and every realm user should read.
	OpenRead bool

	// ScopedAccess switches RBAC from global roles to per-connection grants for
	// non-admin users. Off by default (a user's role applies to every connection,
	// the original behaviour). When on, a non-admin user reaches a connection only
	// if one of their groups/roles has a grant on it, and a grant never exceeds
	// their global capability. Global admins still see and manage everything.
	ScopedAccess bool

	// TrustProxy enables honoring X-Forwarded-For / X-Real-IP for the client IP.
	// Only safe behind a reverse proxy that overwrites those headers; off by
	// default so untrusted clients can't spoof their address.
	TrustProxy bool

	// AllowLocalTargets disables the server-side egress guard that blocks database
	// connections from resolving to loopback, link-local, or cloud-metadata
	// (169.254.169.254) addresses. Off by default to prevent SSRF; turn it on only
	// when a target legitimately lives on localhost (e.g. a sidecar). Dev mode
	// implies it so local databases work out of the box.
	AllowLocalTargets bool

	// High availability.
	// SessionBackend selects where sessions live: "memory" (default, single-node)
	// or "redis" (shared, so multiple replicas can serve any session).
	SessionBackend  string
	SessionRedisURL string // redis://[:pass@]host:port/db when SessionBackend=redis
	// StoreDriver selects the metadata backend: "sqlite" (default, zero-dependency)
	// or "postgres" (shared/replicated metadata for HA). StoreDSN is the Postgres
	// connection string when StoreDriver=postgres.
	StoreDriver string
	StoreDSN    string
	// PGPoolMaxConns caps the pooled connections opened to each registered
	// Postgres target (default 4). Raise it for busier deployments.
	PGPoolMaxConns int

	// Observability.
	LogLevel  string // debug | info | warn | error (default info)
	LogFormat string // json | text (default json; text is friendlier in dev)
	// MetricsToken, when set, requires scrapers of /metrics to send it as a
	// Bearer token. Empty leaves /metrics open (typical behind a private network).
	MetricsToken string
	// AuditRetentionDays purges audit rows older than this many days. 0 keeps
	// them forever (the default).
	AuditRetentionDays int

	// DevMode auto-logs every request in as a local admin (no Keycloak). It must
	// be requested explicitly via DBM_DEV_MODE=true a missing OIDC config no
	// longer silently disables auth (that would fail open in production).
	DevMode bool
}

// Load reads the environment and validates it. It returns an error (rather than
// quietly degrading) when authentication would otherwise be disabled, so the
// process fails closed instead of booting wide open on a misconfiguration.
func Load() (*Config, error) {
	c := &Config{
		Addr:               env("DBM_ADDR", ":8080"),
		SQLitePath:         env("DBM_SQLITE_PATH", "./data/verix-dbm.db"),
		SQLiteDir:          os.Getenv("DBM_SQLITE_DIR"),
		EncKey:             os.Getenv("DBM_ENC_KEY"),
		EncKeys:            os.Getenv("DBM_ENC_KEYS"),
		BaseURL:            env("DBM_BASE_URL", "http://localhost:8080"),
		OIDCIssuer:         os.Getenv("OIDC_ISSUER"),
		OIDCClientID:       os.Getenv("OIDC_CLIENT_ID"),
		OIDCClientSecret:   os.Getenv("OIDC_CLIENT_SECRET"),
		OIDCRedirectURL:    os.Getenv("OIDC_REDIRECT_URL"),
		OIDCAdminRole:      env("OIDC_ADMIN_ROLE", "dbm-admin"),
		OIDCWriteRole:      env("OIDC_WRITE_ROLE", "dbm-write"),
		OIDCReadRole:       env("OIDC_READ_ROLE", "dbm-read"),
		OpenRead:           os.Getenv("DBM_OPEN_READ") == "true",
		ScopedAccess:       os.Getenv("DBM_SCOPED_ACCESS") == "true",
		TrustProxy:         os.Getenv("DBM_TRUST_PROXY") == "true",
		AllowLocalTargets:  os.Getenv("DBM_ALLOW_LOCAL_TARGETS") == "true",
		DevMode:            os.Getenv("DBM_DEV_MODE") == "true",
		LogLevel:           env("DBM_LOG_LEVEL", "info"),
		LogFormat:          env("DBM_LOG_FORMAT", "json"),
		MetricsToken:       os.Getenv("DBM_METRICS_TOKEN"),
		AuditRetentionDays: intEnv("DBM_AUDIT_RETENTION_DAYS", 0),
		SessionBackend:     env("DBM_SESSION_BACKEND", "memory"),
		SessionRedisURL:    os.Getenv("DBM_SESSION_REDIS_URL"),
		StoreDriver:        env("DBM_STORE_DRIVER", "sqlite"),
		StoreDSN:           os.Getenv("DBM_STORE_DSN"),
		PGPoolMaxConns:     intEnv("DBM_PG_POOL_MAX_CONNS", 4),
	}
	if c.OIDCRedirectURL == "" {
		c.OIDCRedirectURL = c.BaseURL + "/auth/callback"
	}

	if c.DevMode {
		log.Println("config: DBM_DEV_MODE=true DEV mode, auto-login as a local admin. NEVER enable this in production.")
		return c, nil
	}
	// Production: OIDC must be fully configured or we refuse to start. Booting
	// without it used to fall back to DEV auto-admin, i.e. fail open.
	if c.OIDCIssuer == "" || c.OIDCClientID == "" {
		return nil, fmt.Errorf("authentication is not configured: set OIDC_ISSUER and OIDC_CLIENT_ID, " +
			"or set DBM_DEV_MODE=true for local development. Refusing to start with auth disabled")
	}
	// A persistent encryption key is mandatory in production. Without it the crypto
	// layer would mint a random ephemeral key, so every saved credential becomes
	// undecryptable after a restart and HA replicas can't read each other's writes.
	// Fail closed rather than boot on a throwaway key.
	if c.EncKey == "" && c.EncKeys == "" {
		return nil, fmt.Errorf("encryption key is not configured: set DBM_ENC_KEY (64 hex chars) or DBM_ENC_KEYS, " +
			"or set DBM_DEV_MODE=true for local development. Refusing to start without a persistent credential key")
	}
	if c.OpenRead {
		log.Println("config: DBM_OPEN_READ=true every authenticated realm user is granted READ access to all connections.")
	}
	if c.ScopedAccess {
		log.Println("config: DBM_SCOPED_ACCESS=true non-admin users reach a connection only via a per-connection grant.")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// intEnv reads a non-negative integer env var, falling back to def when unset or
// unparseable.
func intEnv(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}
