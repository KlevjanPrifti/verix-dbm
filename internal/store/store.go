// Package store persists connection profiles and an audit log in SQLite
// (pure-Go driver, so the binary stays static and cgo-free).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Connection is a saved target (Postgres or Redis). Password is stored
// encrypted (see internal/crypto); the plaintext never touches SQLite.
type Connection struct {
	ID          int64
	Name        string
	Kind        string // "postgres" | "redis"
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

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: serialize writes; plenty for metadata
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS connections (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
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
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  ts        TEXT NOT NULL,
  user      TEXT NOT NULL DEFAULT '',
  conn_id   INTEGER NOT NULL DEFAULT 0,
  action    TEXT NOT NULL,
  detail    TEXT NOT NULL DEFAULT '',
  success   INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS connection_grants (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  conn_id    INTEGER NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
  subject    TEXT NOT NULL,
  level      TEXT NOT NULL,
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(conn_id, subject)
);`)
	return err
}

func (s *Store) ListConnections(ctx context.Context) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,kind,host,port,dbname,username,password_enc,options,readonly,created_by,created_at FROM connections ORDER BY kind,name`)
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
	row := s.db.QueryRowContext(ctx, `SELECT id,name,kind,host,port,dbname,username,password_enc,options,readonly,created_by,created_at FROM connections WHERE id=?`, id)
	return scanConn(row)
}

func (s *Store) CreateConnection(ctx context.Context, c Connection) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO connections (name,kind,host,port,dbname,username,password_enc,options,readonly,created_by,created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		c.Name, c.Kind, c.Host, c.Port, c.DBName, c.Username, c.PasswordEnc, c.Options, boolToInt(c.ReadOnly), c.CreatedBy, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateConnection saves edits to an existing connection. The password is only
// rewritten when updatePw is true (an empty field in the edit form means "keep").
func (s *Store) UpdateConnection(ctx context.Context, c Connection, updatePw bool) error {
	if updatePw {
		_, err := s.db.ExecContext(ctx,
			`UPDATE connections SET name=?,kind=?,host=?,port=?,dbname=?,username=?,password_enc=?,options=?,readonly=? WHERE id=?`,
			c.Name, c.Kind, c.Host, c.Port, c.DBName, c.Username, c.PasswordEnc, c.Options, boolToInt(c.ReadOnly), c.ID)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE connections SET name=?,kind=?,host=?,port=?,dbname=?,username=?,options=?,readonly=? WHERE id=?`,
		c.Name, c.Kind, c.Host, c.Port, c.DBName, c.Username, c.Options, boolToInt(c.ReadOnly), c.ID)
	return err
}

func (s *Store) DeleteConnection(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM connections WHERE id=?`, id)
	return err
}

func (s *Store) AddAudit(ctx context.Context, a Audit) {
	// Best-effort; never block a request on audit failure.
	_, _ = s.db.ExecContext(ctx,
		`INSERT INTO audit (ts,user,conn_id,action,detail,success) VALUES (?,?,?,?,?,?)`,
		time.Now().UTC().Format(time.RFC3339), a.User, a.ConnID, a.Action, a.Detail, boolToInt(a.Success))
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]Audit, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,ts,user,conn_id,action,detail,success FROM audit ORDER BY id DESC LIMIT ?`, limit)
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
	rows, err := s.db.QueryContext(ctx,
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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO connection_grants (conn_id,subject,level,created_by,created_at)
		 VALUES (?,?,?,?,?)
		 ON CONFLICT(conn_id,subject) DO UPDATE SET level=excluded.level, created_by=excluded.created_by, created_at=excluded.created_at`,
		g.ConnID, g.Subject, g.Level, g.CreatedBy, time.Now().UTC().Format(time.RFC3339))
	return err
}

// DeleteGrant removes a grant by id, scoped to its connection so a mismatched
// pair is a no-op rather than deleting another connection's grant.
func (s *Store) DeleteGrant(ctx context.Context, connID, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM connection_grants WHERE id=? AND conn_id=?`, id, connID)
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
	rows, err := s.db.QueryContext(ctx,
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
	rows, err := s.db.QueryContext(ctx,
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
