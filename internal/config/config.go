// Package config loads runtime configuration from the environment.
package config

import (
	"fmt"
	"log"
	"os"
)

type Config struct {
	Addr       string // HTTP bind address, e.g. ":8080"
	SQLitePath string // path to the metadata DB file
	EncKey     string // hex/base64 32-byte key for credential encryption ("" => ephemeral)
	BaseURL    string // public base URL (for OIDC redirect default)

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

	// TrustProxy enables honoring X-Forwarded-For / X-Real-IP for the client IP.
	// Only safe behind a reverse proxy that overwrites those headers; off by
	// default so untrusted clients can't spoof their address.
	TrustProxy bool

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
		Addr:             env("DBM_ADDR", ":8080"),
		SQLitePath:       env("DBM_SQLITE_PATH", "./data/verix-dbm.db"),
		EncKey:           os.Getenv("DBM_ENC_KEY"),
		BaseURL:          env("DBM_BASE_URL", "http://localhost:8080"),
		OIDCIssuer:       os.Getenv("OIDC_ISSUER"),
		OIDCClientID:     os.Getenv("OIDC_CLIENT_ID"),
		OIDCClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
		OIDCRedirectURL:  os.Getenv("OIDC_REDIRECT_URL"),
		OIDCAdminRole:    env("OIDC_ADMIN_ROLE", "dbm-admin"),
		OIDCWriteRole:    env("OIDC_WRITE_ROLE", "dbm-write"),
		OIDCReadRole:     env("OIDC_READ_ROLE", "dbm-read"),
		OpenRead:         os.Getenv("DBM_OPEN_READ") == "true",
		TrustProxy:       os.Getenv("DBM_TRUST_PROXY") == "true",
		DevMode:          os.Getenv("DBM_DEV_MODE") == "true",
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
	if c.OpenRead {
		log.Println("config: DBM_OPEN_READ=true every authenticated realm user is granted READ access to all connections.")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
