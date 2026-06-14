// Package store persists connection profiles and an audit log. The default
// backend is SQLite (pure-Go driver, so the binary stays static and cgo-free);
// a Postgres backend can be selected for shared/replicated metadata in an HA
// deployment. Both speak the same SQL here: queries are written with "?"
// placeholders and rebound to "$N" for Postgres, the few engine-specific bits
// (id columns, the reserved word "user") are handled explicitly.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql" // target DSN builder for MySQL/MariaDB
	_ "github.com/jackc/pgx/v5/stdlib"       // database/sql driver "pgx"
	_ "modernc.org/sqlite"                   // database/sql driver "sqlite"

	"verix-dbm/internal/dbsql"
)

// Connection is a saved target (Postgres, MySQL/MariaDB, or Redis). Password is
// stored encrypted (see internal/crypto); the plaintext never touches SQLite.
type Connection struct {
	ID          int64
	Name        string
	Kind        string // a dbkinds id: "postgres" | "mysql" | "mariadb" | "redis" | ...
	Host        string
	Port        int
	DBName      string // pg database, or redis logical db number as string
	Username    string
	PasswordEnc string
	Options     string // e.g. "sslmode=disable" (pg) or key prefix hint (redis)
	ReadOnly    bool
	CreatedBy   string
	CreatedAt   time.Time
}

// Grant scopes a subject's access to a single connection. Subject is a Keycloak
// group path or realm-role name; Level is "read" or "write". Grants only take
// effect when DBM_SCOPED_ACCESS is on (otherwise global roles apply to every
// connection, the default behaviour). A grant never raises a user above their
// global capability: it scopes which connections they reach, not what they can
// do. Connection management (create/update/delete) stays a global-admin power.
type Grant struct {
	ID        int64
	ConnID    int64
	Subject   string
	Level     string // GrantRead | GrantWrite
	CreatedBy string
	CreatedAt time.Time
}

const (
	GrantRead  = "read"
	GrantWrite = "write"
)

// ValidGrantLevel reports whether level is a recognised grant level.
func ValidGrantLevel(level string) bool {
	return level == GrantRead || level == GrantWrite
}

type Audit struct {
	ID      int64
	TS      time.Time
	User    string
	ConnID  int64
	Action  string
	Detail  string
	Success bool
}

type Store struct {
	db     *sql.DB
	driver string // "sqlite" | "postgres"
	// sink, if set, is called after every successful audit insert so the caller
	// can mirror the event elsewhere (structured logs / metrics / SIEM). Best
	// effort and synchronous, so it must be cheap and non-blocking.
	sink func(Audit)
}

// Open opens the SQLite metadata store at path (the default backend).
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: serialize writes; plenty for metadata
	return finishOpen(&Store{db: db, driver: "sqlite"})
}

// OpenPostgres opens a Postgres metadata store (HA: shared, replicated). The dsn
// is a libpq/pgx connection string. Unlike SQLite it allows concurrent writers,
// so several app replicas can share one metadata database.
func OpenPostgres(dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	return finishOpen(&Store{db: db, driver: "postgres"})
}

func finishOpen(s *Store) (*Store, error) {
	if err := s.migrate(); err != nil {
		s.db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// rebind converts "?" placeholders to Postgres "$N". SQLite keeps "?", so the
// rest of the package can be written once with "?".
func (s *Store) rebind(q string) string {
	if s.driver != "postgres" {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteByte(q[i])
		}
	}
	return b.String()
}

func (s *Store) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.rebind(q), args...)
}

func (s *Store) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.rebind(q), args...)
}

func (s *Store) queryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.rebind(q), args...)
}

// Ping verifies the metadata DB is reachable (backs the readiness probe).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// OnAudit registers a sink invoked after each audit row is written. Used to
// mirror audit events to structured logs / metrics without coupling the store to
// either.
func (s *Store) OnAudit(fn func(Audit)) { s.sink = fn }

func (s *Store) migrate() error {
	// id columns are the only real DDL difference; "user" is quoted (reserved in
	// Postgres) and accepted as an identifier by SQLite too.
	idCol := "INTEGER PRIMARY KEY AUTOINCREMENT"
	connRef := "INTEGER"
	if s.driver == "postgres" {
		idCol = "BIGSERIAL PRIMARY KEY"
		connRef = "BIGINT"
	}
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS connections (
  id           ` + idCol + `,
  name         TEXT NOT NULL,
  kind         TEXT NOT NULL,
  host         TEXT NOT NULL,
  port         INTEGER NOT NULL,
  dbname       TEXT NOT NULL DEFAULT '',
  username     TEXT NOT NULL DEFAULT '',
  password_enc TEXT NOT NULL DEFAULT '',
  options      TEXT NOT NULL DEFAULT '',
  readonly     INTEGER NOT NULL DEFAULT 0,
  created_by   TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
  id        ` + idCol + `,
  ts        TEXT NOT NULL,
  "user"    TEXT NOT NULL DEFAULT '',
  conn_id   ` + connRef + ` NOT NULL DEFAULT 0,
  action    TEXT NOT NULL,
  detail    TEXT NOT NULL DEFAULT '',
  success   INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS connection_grants (
  id         ` + idCol + `,
  conn_id    ` + connRef + ` NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
  subject    TEXT NOT NULL,
  level      TEXT NOT NULL,
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(conn_id, subject)
);`)
	return err
}

func (s *Store) ListConnections(ctx context.Context) ([]Connection, error) {
	rows, err := s.query(ctx, `SELECT id,name,kind,host,port,dbname,username,password_enc,options,readonly,created_by,created_at FROM connections ORDER BY kind,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetConnection(ctx context.Context, id int64) (Connection, error) {
	row := s.queryRow(ctx, `SELECT id,name,kind,host,port,dbname,username,password_enc,options,readonly,created_by,created_at FROM connections WHERE id=?`, id)
	return scanConn(row)
}

func (s *Store) CreateConnection(ctx context.Context, c Connection) (int64, error) {
	// RETURNING id works on both SQLite (>= 3.35) and Postgres, avoiding the
	// LastInsertId divergence between drivers.
	var id int64
	err := s.queryRow(ctx,
		`INSERT INTO connections (name,kind,host,port,dbname,username,password_enc,options,readonly,created_by,created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?) RETURNING id`,
		c.Name, c.Kind, c.Host, c.Port, c.DBName, c.Username, c.PasswordEnc, c.Options, boolToInt(c.ReadOnly), c.CreatedBy, time.Now().UTC().Format(time.RFC3339)).Scan(&id)
	return id, err
}

// UpdateConnection saves edits to an existing connection. The password is only
// rewritten when updatePw is true (an empty field in the edit form means "keep").
func (s *Store) UpdateConnection(ctx context.Context, c Connection, updatePw bool) error {
	if updatePw {
		_, err := s.exec(ctx,
			`UPDATE connections SET name=?,kind=?,host=?,port=?,dbname=?,username=?,password_enc=?,options=?,readonly=? WHERE id=?`,
			c.Name, c.Kind, c.Host, c.Port, c.DBName, c.Username, c.PasswordEnc, c.Options, boolToInt(c.ReadOnly), c.ID)
		return err
	}
	_, err := s.exec(ctx,
		`UPDATE connections SET name=?,kind=?,host=?,port=?,dbname=?,username=?,options=?,readonly=? WHERE id=?`,
		c.Name, c.Kind, c.Host, c.Port, c.DBName, c.Username, c.Options, boolToInt(c.ReadOnly), c.ID)
	return err
}

// UpdatePasswordEnc rewrites only the stored ciphertext for a connection. Used
// by key rotation re-encryption, which must not touch any other field.
func (s *Store) UpdatePasswordEnc(ctx context.Context, id int64, enc string) error {
	_, err := s.exec(ctx, `UPDATE connections SET password_enc=? WHERE id=?`, enc, id)
	return err
}

func (s *Store) DeleteConnection(ctx context.Context, id int64) error {
	_, err := s.exec(ctx, `DELETE FROM connections WHERE id=?`, id)
	return err
}

func (s *Store) AddAudit(ctx context.Context, a Audit) {
	// Best-effort; never block a request on audit failure.
	a.TS = time.Now().UTC()
	_, _ = s.exec(ctx,
		`INSERT INTO audit (ts,"user",conn_id,action,detail,success) VALUES (?,?,?,?,?,?)`,
		a.TS.Format(time.RFC3339), a.User, a.ConnID, a.Action, a.Detail, boolToInt(a.Success))
	if s.sink != nil {
		s.sink(a)
	}
}

// PurgeAuditOlderThan deletes audit rows timestamped before cutoff and returns
// the number removed. ts is stored as RFC3339 UTC, so a lexical comparison is a
// correct chronological one.
func (s *Store) PurgeAuditOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.exec(ctx, `DELETE FROM audit WHERE ts < ?`, cutoff.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// IterAudit streams every audit row, oldest first, to fn. Used by the export
// endpoint so the full log can be dumped without buffering it all in memory.
func (s *Store) IterAudit(ctx context.Context, fn func(Audit) error) error {
	rows, err := s.query(ctx, `SELECT id,ts,"user",conn_id,action,detail,success FROM audit ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a Audit
		var ts string
		var succ int
		if err := rows.Scan(&a.ID, &ts, &a.User, &a.ConnID, &a.Action, &a.Detail, &succ); err != nil {
			return err
		}
		a.TS, _ = time.Parse(time.RFC3339, ts)
		a.Success = succ != 0
		if err := fn(a); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]Audit, error) {
	rows, err := s.query(ctx, `SELECT id,ts,"user",conn_id,action,detail,success FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Audit
	for rows.Next() {
		var a Audit
		var ts string
		var succ int
		if err := rows.Scan(&a.ID, &ts, &a.User, &a.ConnID, &a.Action, &a.Detail, &succ); err != nil {
			return nil, err
		}
		a.TS, _ = time.Parse(time.RFC3339, ts)
		a.Success = succ != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListGrants returns every grant on a connection, newest first. Used by the
// admin access panel.
func (s *Store) ListGrants(ctx context.Context, connID int64) ([]Grant, error) {
	rows, err := s.query(ctx,
		`SELECT id,conn_id,subject,level,created_by,created_at FROM connection_grants WHERE conn_id=? ORDER BY subject`, connID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetGrant upserts a grant: one (conn_id, subject) row, its level replaced on
// repeat. created_by/created_at reflect the most recent write.
func (s *Store) SetGrant(ctx context.Context, g Grant) error {
	_, err := s.exec(ctx,
		`INSERT INTO connection_grants (conn_id,subject,level,created_by,created_at)
		 VALUES (?,?,?,?,?)
		 ON CONFLICT(conn_id,subject) DO UPDATE SET level=excluded.level, created_by=excluded.created_by, created_at=excluded.created_at`,
		g.ConnID, g.Subject, g.Level, g.CreatedBy, time.Now().UTC().Format(time.RFC3339))
	return err
}

// DeleteGrant removes a grant by id, scoped to its connection so a mismatched
// pair is a no-op rather than deleting another connection's grant.
func (s *Store) DeleteGrant(ctx context.Context, connID, id int64) error {
	_, err := s.exec(ctx, `DELETE FROM connection_grants WHERE id=? AND conn_id=?`, id, connID)
	return err
}

// GrantForSubjects returns the highest-level grant on connID held by any of the
// given subjects (write outranks read), or nil if none match. This is the
// per-request access lookup on the read/write paths.
func (s *Store) GrantForSubjects(ctx context.Context, connID int64, subjects []string) (*Grant, error) {
	if len(subjects) == 0 {
		return nil, nil
	}
	ph, args := placeholders(subjects, connID)
	rows, err := s.query(ctx,
		`SELECT id,conn_id,subject,level,created_by,created_at FROM connection_grants
		 WHERE conn_id=? AND subject IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var best *Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		if best == nil || (g.Level == GrantWrite && best.Level != GrantWrite) {
			gc := g
			best = &gc
		}
	}
	return best, rows.Err()
}

// ListConnectionsForSubjects returns the connections any of the given subjects
// has a grant on (scoped-access mode). Distinct, ordered like ListConnections.
func (s *Store) ListConnectionsForSubjects(ctx context.Context, subjects []string) ([]Connection, error) {
	if len(subjects) == 0 {
		return nil, nil
	}
	ph, args := placeholders(subjects)
	rows, err := s.query(ctx,
		`SELECT DISTINCT c.id,c.name,c.kind,c.host,c.port,c.dbname,c.username,c.password_enc,c.options,c.readonly,c.created_by,c.created_at
		 FROM connections c JOIN connection_grants g ON g.conn_id=c.id
		 WHERE g.subject IN (`+ph+`) ORDER BY c.kind,c.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// placeholders builds an "?,?,?" list for subjects and the matching args slice,
// with any leading args (e.g. a conn id) prepended in order.
func placeholders(subjects []string, lead ...any) (string, []any) {
	ph := make([]string, len(subjects))
	args := make([]any, 0, len(lead)+len(subjects))
	args = append(args, lead...)
	for i, s := range subjects {
		ph[i] = "?"
		args = append(args, s)
	}
	return strings.Join(ph, ","), args
}

func scanGrant(r scanner) (Grant, error) {
	var g Grant
	var ts string
	if err := r.Scan(&g.ID, &g.ConnID, &g.Subject, &g.Level, &g.CreatedBy, &ts); err != nil {
		return g, err
	}
	g.CreatedAt, _ = time.Parse(time.RFC3339, ts)
	return g, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanConn(r scanner) (Connection, error) {
	var c Connection
	var ts string
	var ro int
	if err := r.Scan(&c.ID, &c.Name, &c.Kind, &c.Host, &c.Port, &c.DBName, &c.Username, &c.PasswordEnc, &c.Options, &ro, &c.CreatedBy, &ts); err != nil {
		return c, err
	}
	c.ReadOnly = ro != 0
	c.CreatedAt, _ = time.Parse(time.RFC3339, ts)
	return c, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// DSN builds a libpq-style connection string for a Postgres connection. The
// username, password, host, and database are URL-encoded (via net/url) so that
// special characters in credentials can't break parsing or inject connection
// parameters. Options is passed through verbatim as the query string.
func (c Connection) DSN(password string) string {
	opts := c.Options
	if opts == "" {
		// Default to attempting TLS (falls back to plaintext if the server has no
		// TLS) instead of disabling it outright credentials shouldn't cross the
		// network in the clear by default. For remote/production targets prefer an
		// explicit sslmode=verify-full in Options. An admin who needs plaintext can
		// still set sslmode=disable.
		opts = "sslmode=prefer"
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.Username, password),
		Host:     fmt.Sprintf("%s:%d", c.Host, c.Port),
		Path:     "/" + c.DBName,
		RawQuery: opts,
	}
	return u.String()
}

// Engine returns the connection's engine family ("postgres" | "mysql" | "redis").
// Mirrors the frontend dbkinds.ts mapping so both agree on which path serves a kind.
func (c Connection) Engine() string { return dbsql.Family(c.Kind) }

// DSNMySQL builds a go-sql-driver DSN for a MySQL/MariaDB target. Session safety
// settings (sql_mode, time_zone, charset) are pinned here so every freshly opened
// pooled connection re-applies them at handshake (never via per-query SET SESSION).
// Extra connection params can be supplied verbatim in Options ("k=v&k2=v2").
func (c Connection) DSNMySQL(password string) string {
	cfg := gomysql.NewConfig()
	cfg.User = c.Username
	cfg.Passwd = password
	cfg.Net = "tcp"
	cfg.Addr = fmt.Sprintf("%s:%d", c.Host, c.Port)
	cfg.DBName = c.DBName
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.InterpolateParams = false
	cfg.Params = map[string]string{
		"charset":   "utf8mb4",
		"sql_mode":  "'STRICT_ALL_TABLES,NO_ENGINE_SUBSTITUTION'",
		"time_zone": "'+00:00'",
	}
	// Merge any admin-supplied params (e.g. "tls=skip-verify"); these win.
	for _, kv := range strings.Split(c.Options, "&") {
		if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok && k != "" {
			cfg.Params[k] = v
		}
	}
	return cfg.FormatDSN()
}
