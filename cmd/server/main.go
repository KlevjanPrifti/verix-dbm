// verix-dbm a low-footprint web DB manager for PostgreSQL and Redis/Valkey.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
		log.Fatalf("config: %v", err)
	}

	if dir := filepath.Dir(cfg.SQLitePath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("mkdir data dir: %v", err)
		}
	}

	box, err := crypto.New(cfg.EncKey)
	if err != nil {
		log.Fatalf("crypto: %v", err)
	}

	st, err := store.Open(cfg.SQLitePath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	reg := conn.NewRegistry(box)

	ctx := context.Background()
	a, err := auth.New(ctx, cfg)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	// Record login success/failure to the audit log.
	a.SetAudit(func(ctx context.Context, action, detail string, success bool) {
		st.AddAudit(ctx, store.Audit{User: detail, Action: action, Detail: detail, Success: success})
	})

	srv := web.NewServer(cfg, st, reg, a, box)
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("verix-dbm listening on %s (dev=%v)", cfg.Addr, cfg.DevMode)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("shutting down…")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
}
