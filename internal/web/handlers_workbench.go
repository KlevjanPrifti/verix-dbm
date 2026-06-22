package web

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"verix-dbm/internal/dbsql"
	"verix-dbm/internal/store"
)

// Shared connection helpers. The legacy HTMX workbench that lived here has been
// removed; these helpers are still used by the JSON API (pingPG/pingMySQL/
// pingRedis) and the CSV export handler (sqlEngineFor).

// sqlEngineFor resolves the URL's connection and its dbsql.Engine (Postgres or
// MySQL, per the connection's kind), writing an error response and returning
// ok=false on failure.
func (s *Server) sqlEngineFor(w http.ResponseWriter, r *http.Request) (store.Connection, dbsql.Engine, bool) {
	c, err := s.connFor(r)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return store.Connection{}, nil, false
	}
	eng, err := s.reg.Engine(r.Context(), c)
	if err != nil {
		http.Error(w, "connect: "+err.Error(), http.StatusBadGateway)
		return c, nil, false
	}
	return c, eng, true
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

// pingMySQL opens a one-shot pool to verify MySQL/MariaDB connectivity for a
// candidate connection (used by the "Test connection" action).
func pingMySQL(ctx context.Context, c store.Connection, pw string) error {
	db, err := sql.Open("mysql", c.DSNMySQL(pw))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return db.PingContext(cctx)
}

// pingSQLite verifies a SQLite target file is reachable (and inside the
// DBM_SQLITE_DIR allowlist) for a candidate connection. modernc.org/sqlite
// creates the file if it does not exist yet, which is the expected way to start
// a fresh database; the allowlist still fences where that can happen.
func pingSQLite(ctx context.Context, c store.Connection, allowDir string) error {
	path, err := store.ResolveSQLitePath(allowDir, c.DBName)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", store.SQLiteDSN(path))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return db.PingContext(cctx)
}

// pingMongo verifies MongoDB connectivity for a candidate connection.
func pingMongo(ctx context.Context, c store.Connection, pw string) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, err := mongo.Connect(cctx, options.Client().ApplyURI(c.DSNMongo(pw)).
		SetServerSelectionTimeout(5*time.Second))
	if err != nil {
		return err
	}
	defer client.Disconnect(context.Background())
	return client.Ping(cctx, nil)
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
