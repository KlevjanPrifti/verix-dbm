// Package store persists connection profiles and an audit log in SQLite
// (pure-Go driver, so the binary stays static and cgo-free).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
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
