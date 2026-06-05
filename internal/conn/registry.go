// Package conn manages live connection pools to registered targets. Pools are
// opened lazily on first use and closed after an idle period to keep RAM low.
package conn

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"verix-dbm/internal/crypto"
	"verix-dbm/internal/store"
)

const idleTTL = 5 * time.Minute

type pgEntry struct {
	pool     *pgxpool.Pool
	lastUsed time.Time
}

type redisEntry struct {
	client   *redis.Client
	lastUsed time.Time
}

type Registry struct {
	box   *crypto.Box
	mu    sync.Mutex
	pg    map[int64]*pgEntry
	redis map[int64]*redisEntry
}

func NewRegistry(box *crypto.Box) *Registry {
	r := &Registry{box: box, pg: map[int64]*pgEntry{}, redis: map[int64]*redisEntry{}}
	go r.janitor()
	return r
}

func (r *Registry) janitor() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		r.mu.Lock()
		now := time.Now()
		for id, e := range r.pg {
			if now.Sub(e.lastUsed) > idleTTL {
				e.pool.Close()
				delete(r.pg, id)
			}
		}
		for id, e := range r.redis {
			if now.Sub(e.lastUsed) > idleTTL {
				_ = e.client.Close()
				delete(r.redis, id)
			}
		}
		r.mu.Unlock()
	}
}

// PG returns a pooled Postgres connection for the given profile.
func (r *Registry) PG(ctx context.Context, c store.Connection) (*pgxpool.Pool, error) {
	r.mu.Lock()
	if e, ok := r.pg[c.ID]; ok {
		e.lastUsed = time.Now()
		r.mu.Unlock()
		return e.pool, nil
	}
	r.mu.Unlock()

	pw, err := r.password(c)
	if err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(c.DSN(pw))
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 4
	cfg.MaxConnIdleTime = idleTTL
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	r.mu.Lock()
	r.pg[c.ID] = &pgEntry{pool: pool, lastUsed: time.Now()}
	r.mu.Unlock()
	return pool, nil
}

// Redis returns a client for the given profile.
func (r *Registry) Redis(ctx context.Context, c store.Connection) (*redis.Client, error) {
	r.mu.Lock()
	if e, ok := r.redis[c.ID]; ok {
		e.lastUsed = time.Now()
		r.mu.Unlock()
		return e.client, nil
	}
	r.mu.Unlock()

	pw, err := r.password(c)
	if err != nil {
		return nil, err
	}
	dbNum := 0
	if c.DBName != "" {
		if n, err := strconv.Atoi(c.DBName); err == nil {
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
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(cctx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	r.mu.Lock()
	r.redis[c.ID] = &redisEntry{client: client, lastUsed: time.Now()}
	r.mu.Unlock()
	return client, nil
}

// Forget drops any cached pool/client for a connection (e.g. after delete).
func (r *Registry) Forget(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.pg[id]; ok {
		e.pool.Close()
		delete(r.pg, id)
	}
	if e, ok := r.redis[id]; ok {
		_ = e.client.Close()
		delete(r.redis, id)
	}
}

func (r *Registry) password(c store.Connection) (string, error) {
	if c.PasswordEnc == "" {
		return "", nil
	}
	return r.box.Decrypt(c.PasswordEnc)
}
