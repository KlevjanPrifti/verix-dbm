package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"verix-dbm/internal/store"
)

// Shared connection helpers. The legacy HTMX workbench that lived here has been
// removed; these helpers are still used by the JSON API (pingPG/pingRedis) and
// the CSV export handler (pgPoolFor).

// pgPoolFor resolves the URL's connection and its Postgres pool, writing an
// error response and returning ok=false on failure.
func (s *Server) pgPoolFor(w http.ResponseWriter, r *http.Request) (store.Connection, *pgxpool.Pool, bool) {
	c, err := s.connFor(r)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return store.Connection{}, nil, false
	}
	pool, err := s.reg.PG(r.Context(), c)
	if err != nil {
		http.Error(w, "connect: "+err.Error(), http.StatusBadGateway)
		return c, nil, false
	}
	return c, pool, true
}

// pingPG opens a one-shot pool to verify Postgres connectivity for a candidate
// connection (used by the "Test connection" action).
func pingPG(ctx context.Context, c store.Connection, pw string) error {
	cfg, err := pgxpool.ParseConfig(c.DSN(pw))
	if err != nil {
		return err
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	return pool.Ping(ctx)
}

// pingRedis verifies Redis/Valkey connectivity for a candidate connection.
func pingRedis(ctx context.Context, c store.Connection, pw string) error {
	dbNum := 0
	if c.DBName != "" {
		if n, e := strconv.Atoi(c.DBName); e == nil {
			dbNum = n
		}
	}
	user := c.Username
	if user == "" {
		user = "default"
	}
	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", c.Host, c.Port),
		Username: user,
		Password: pw,
		DB:       dbNum,
	})
	defer client.Close()
	return client.Ping(ctx).Err()
}
