// verix-dbm a low-footprint web DB manager for PostgreSQL and Redis/Valkey.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/config"
	"verix-dbm/internal/conn"
	"verix-dbm/internal/crypto"
	"verix-dbm/internal/store"
	"verix-dbm/internal/web"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Config failed before the logger is configured; stderr is the only sink.
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)

	if cfg.StoreDriver != "postgres" {
		if dir := filepath.Dir(cfg.SQLitePath); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				fatal(logger, "mkdir data dir", err)
			}
		}
	}

	box, err := crypto.ParseKeyring(cfg.EncKey, cfg.EncKeys)
	if err != nil {
		fatal(logger, "crypto", err)
	}

	st, err := openStore(cfg)
	if err != nil {
		fatal(logger, "store", err)
	}
	defer st.Close()

	reg := conn.NewRegistry(box, cfg.PGPoolMaxConns)

	ctx := context.Background()
	a, err := auth.New(ctx, cfg)
	if err != nil {
		fatal(logger, "auth", err)
	}
	// Record login success/failure to the audit log.
	a.SetAudit(func(ctx context.Context, action, detail string, success bool) {
		st.AddAudit(ctx, store.Audit{User: detail, Action: action, Detail: detail, Success: success})
	})

	srv := web.NewServer(cfg, st, reg, a, box, logger)
	// Mirror every audit event to the structured log (for SIEM forwarding) and
	// feed the auth-outcome metric.
	st.OnAudit(srv.ObserveAudit)
	// Audit every use of a stored credential (decrypt-to-open-a-pool).
	reg.OnCredentialAccess(func(c store.Connection) {
		st.AddAudit(context.Background(), store.Audit{ConnID: c.ID, Action: "cred_access", Detail: c.Name, Success: true})
	})

	// Background audit retention: purge rows older than the configured window.
	if cfg.AuditRetentionDays > 0 {
		go retainAudit(st, logger, cfg.AuditRetentionDays)
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("listening", "addr", cfg.Addr, "dev", cfg.DevMode, "scoped_access", cfg.ScopedAccess)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal(logger, "listen", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	logger.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
}

// newLogger builds the process logger from config: JSON by default (structured
// for log shippers), or text for friendlier local development.
func newLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.ToLower(cfg.LogFormat) == "text" {
		h = slog.NewTextHandler(os.Stderr, opts)
	} else {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

// retainAudit purges audit rows older than retentionDays once at startup and
// daily thereafter.
func retainAudit(st *store.Store, logger *slog.Logger, retentionDays int) {
	purge := func() {
		cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		n, err := st.PurgeAuditOlderThan(ctx, cutoff)
		if err != nil {
			logger.Error("audit retention purge failed", "err", err)
			return
		}
		if n > 0 {
			logger.Info("audit retention purge", "removed", n, "older_than_days", retentionDays)
		}
	}
	purge()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for range t.C {
		purge()
	}
}

// openStore opens the configured metadata backend: SQLite by default, or
// Postgres (shared/replicated, for HA) when DBM_STORE_DRIVER=postgres.
func openStore(cfg *config.Config) (*store.Store, error) {
	if cfg.StoreDriver == "postgres" {
		return store.OpenPostgres(cfg.StoreDSN)
	}
	return store.Open(cfg.SQLitePath)
}

func fatal(logger *slog.Logger, msg string, err error) {
	logger.Error(msg, "err", err)
	os.Exit(1)
}
