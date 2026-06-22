// Package conn manages live connection pools to registered targets. Pools are
// opened lazily on first use and closed after an idle period to keep RAM low.
package conn

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql" // database/sql driver "mysql" (MySQL/MariaDB)
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	_ "modernc.org/sqlite" // database/sql driver "sqlite" (embedded file engine)

	"verix-dbm/internal/crypto"
	"verix-dbm/internal/dbsql"
	"verix-dbm/internal/mysql"
	"verix-dbm/internal/postgres"
	"verix-dbm/internal/sqlite"
	"verix-dbm/internal/store"
)

const idleTTL = 5 * time.Minute

type pgEntry struct {
	pool     *pgxpool.Pool
	lastUsed time.Time
}

type mysqlEntry struct {
	db       *sql.DB
	lastUsed time.Time
}

type sqliteEntry struct {
	db       *sql.DB
	lastUsed time.Time
}

type redisEntry struct {
	client   *redis.Client
	lastUsed time.Time
}

type mongoEntry struct {
	client   *mongo.Client
	lastUsed time.Time
}

type Registry struct {
	box        *crypto.Box
	pgMaxConns int32
	sqliteDir  string // DBM_SQLITE_DIR: allowlist root for SQLite target files ("" => disabled)
	mu         sync.Mutex
	pg         map[int64]*pgEntry
	mysql      map[int64]*mysqlEntry
	sqlite     map[int64]*sqliteEntry
	redis      map[int64]*redisEntry
	mongo      map[int64]*mongoEntry
	// onCred, if set, is called whenever a stored credential is decrypted to open
	// a pool (i.e. the secret is actually used). Lets the app audit credential
	// access without coupling this package to the store.
	onCred func(c store.Connection)
}

// NewRegistry builds the pool registry. pgMaxConns caps the pooled connections
// opened to each Postgres/MySQL/SQLite target (<= 0 falls back to 4). sqliteDir
// is the DBM_SQLITE_DIR allowlist root for SQLite files ("" disables SQLite).
func NewRegistry(box *crypto.Box, pgMaxConns int, sqliteDir string) *Registry {
	if pgMaxConns <= 0 {
		pgMaxConns = 4
	}
	r := &Registry{
		box: box, pgMaxConns: int32(pgMaxConns), sqliteDir: sqliteDir,
		pg: map[int64]*pgEntry{}, mysql: map[int64]*mysqlEntry{},
		sqlite: map[int64]*sqliteEntry{}, redis: map[int64]*redisEntry{},
		mongo: map[int64]*mongoEntry{},
	}
	go r.janitor()
	return r
}

// OnCredentialAccess registers a callback invoked each time a saved password is
// decrypted to open a connection.
func (r *Registry) OnCredentialAccess(fn func(c store.Connection)) { r.onCred = fn }

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
		for id, e := range r.mysql {
			if now.Sub(e.lastUsed) > idleTTL {
				_ = e.db.Close()
				delete(r.mysql, id)
			}
		}
		for id, e := range r.sqlite {
			if now.Sub(e.lastUsed) > idleTTL {
				_ = e.db.Close()
				delete(r.sqlite, id)
			}
		}
		for id, e := range r.redis {
			if now.Sub(e.lastUsed) > idleTTL {
				_ = e.client.Close()
				delete(r.redis, id)
			}
		}
		for id, e := range r.mongo {
			if now.Sub(e.lastUsed) > idleTTL {
				dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = e.client.Disconnect(dctx)
				cancel()
				delete(r.mongo, id)
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
	cfg.MaxConns = r.pgMaxConns
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

// MySQL returns a pooled MySQL/MariaDB connection for the given profile. *sql.DB
// is itself a pool, so MaxOpenConns/ConnMaxIdleTime cap it like the pgx pool.
func (r *Registry) MySQL(ctx context.Context, c store.Connection) (*sql.DB, error) {
	r.mu.Lock()
	if e, ok := r.mysql[c.ID]; ok {
		e.lastUsed = time.Now()
		r.mu.Unlock()
		return e.db, nil
	}
	r.mu.Unlock()

	pw, err := r.password(c)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", c.DSNMySQL(pw))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(int(r.pgMaxConns))
	db.SetConnMaxIdleTime(idleTTL)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(cctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	r.mu.Lock()
	r.mysql[c.ID] = &mysqlEntry{db: db, lastUsed: time.Now()}
	r.mu.Unlock()
	return db, nil
}

// SQLite returns a pooled connection to a SQLite target file. The path (stored
// in the connection's DBName) is validated against the DBM_SQLITE_DIR allowlist
// before the file is opened, so an out-of-allowlist or traversal path is refused
// here, the single open choke point. *sql.DB is itself a pool.
func (r *Registry) SQLite(ctx context.Context, c store.Connection) (*sql.DB, error) {
	r.mu.Lock()
	if e, ok := r.sqlite[c.ID]; ok {
		e.lastUsed = time.Now()
		r.mu.Unlock()
		return e.db, nil
	}
	r.mu.Unlock()

	path, err := store.ResolveSQLitePath(r.sqliteDir, c.DBName)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", store.SQLiteDSN(path))
	if err != nil {
		return nil, err
	}
	// SQLite serialises writers; a small pool avoids "database is locked" churn
	// while still allowing concurrent readers in WAL mode.
	db.SetMaxOpenConns(int(r.pgMaxConns))
	db.SetConnMaxIdleTime(idleTTL)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(cctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	r.mu.Lock()
	r.sqlite[c.ID] = &sqliteEntry{db: db, lastUsed: time.Now()}
	r.mu.Unlock()
	return db, nil
}

// Engine returns the dbsql.Engine for a SQL connection, dispatching on the
// connection's engine family. It is the single seam the web layer uses for every
// SQL operation; Redis connections are handled separately (Registry.Redis).
func (r *Registry) Engine(ctx context.Context, c store.Connection) (dbsql.Engine, error) {
	switch c.Engine() {
	case dbsql.FamilyMySQL:
		db, err := r.MySQL(ctx, c)
		if err != nil {
			return nil, err
		}
		return mysql.New(db), nil
	case dbsql.FamilySQLite:
		db, err := r.SQLite(ctx, c)
		if err != nil {
			return nil, err
		}
		return sqlite.New(db), nil
	}
	pool, err := r.PG(ctx, c)
	if err != nil {
		return nil, err
	}
	return postgres.New(pool), nil
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

// Mongo returns a connected client for the given profile. The driver pools
// connections internally; the registry caches one client per connection and
// closes it after the idle TTL.
func (r *Registry) Mongo(ctx context.Context, c store.Connection) (*mongo.Client, error) {
	r.mu.Lock()
	if e, ok := r.mongo[c.ID]; ok {
		e.lastUsed = time.Now()
		r.mu.Unlock()
		return e.client, nil
	}
	r.mu.Unlock()

	pw, err := r.password(c)
	if err != nil {
		return nil, err
	}
	opts := options.Client().ApplyURI(c.DSNMongo(pw)).
		SetServerSelectionTimeout(5 * time.Second)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, err := mongo.Connect(cctx, opts)
	if err != nil {
		return nil, err
	}
	if err := client.Ping(cctx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	r.mu.Lock()
	r.mongo[c.ID] = &mongoEntry{client: client, lastUsed: time.Now()}
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
	if e, ok := r.mysql[id]; ok {
		_ = e.db.Close()
		delete(r.mysql, id)
	}
	if e, ok := r.sqlite[id]; ok {
		_ = e.db.Close()
		delete(r.sqlite, id)
	}
	if e, ok := r.redis[id]; ok {
		_ = e.client.Close()
		delete(r.redis, id)
	}
	if e, ok := r.mongo[id]; ok {
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = e.client.Disconnect(dctx)
		cancel()
		delete(r.mongo, id)
	}
}

func (r *Registry) password(c store.Connection) (string, error) {
	if c.PasswordEnc == "" {
		return "", nil
	}
	pw, err := r.box.Decrypt(c.PasswordEnc)
	if err == nil && r.onCred != nil {
		r.onCred(c)
	}
	return pw, err
}
