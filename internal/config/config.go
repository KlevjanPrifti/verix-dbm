// Package config loads runtime configuration from the environment.
package config

import (
	"log"
	"os"
)

type Config struct {
	Addr       string // HTTP bind address, e.g. ":8080"
	SQLitePath string // path to the metadata DB file
	EncKey     string // hex/base64 32-byte key for credential encryption ("" => ephemeral)
	BaseURL    string // public base URL (for OIDC redirect default)

	// OIDC / Keycloak. When Issuer+ClientID are empty the app runs in DEV mode
	// (auto-login as a local admin) so it boots without Keycloak.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	OIDCAdminRole    string // realm role that grants the admin capability
	OIDCWriteRole    string // realm role that grants write capability

	DevMode bool
}

func Load() *Config {
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
	}
	if c.OIDCRedirectURL == "" {
		c.OIDCRedirectURL = c.BaseURL + "/auth/callback"
	}
	c.DevMode = c.OIDCIssuer == "" || c.OIDCClientID == ""
	if c.DevMode {
		log.Println("config: OIDC not configured — running in DEV mode (auto-login as local admin)")
	}
	return c
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
