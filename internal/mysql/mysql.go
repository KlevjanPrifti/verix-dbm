// Package mysql provides introspection and query execution over a database/sql
// pool for MySQL and MariaDB (same wire protocol, one driver). It implements
// dbsql.Engine so the web layer treats it exactly like the Postgres engine.
//
// Safety settings (sql_mode, time_zone, charset) are pinned at the DSN level
// (see store.DSNMySQL) so a pooled, reused connection always re-initialises them
// at handshake. Statement timeouts are enforced per call via a context deadline
// plus a MAX_EXECUTION_TIME hint on SELECTs; nothing is set with SET SESSION,
// which would leak across the shared pool.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"verix-dbm/internal/dbsql"
)

const (
	statementTimeout = 30 * time.Second
	maxRows          = 1000
	maxExecMillis    = 30000
)

// Engine is a live MySQL/MariaDB connection (a *sql.DB pool).
type Engine struct{ db *sql.DB }

// New wraps an open *sql.DB as a dbsql.Engine.
func New(db *sql.DB) *Engine { return &Engine{db: db} }

var _ dbsql.Engine = (*Engine)(nil)

// querier is satisfied by both *sql.DB and *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Query runs arbitrary SQL. readOnly wraps it in a READ ONLY transaction (the
// MySQL analog of Postgres's default_transaction_read_only), which is pinned to
// one connection for its lifetime, so no read-only state leaks back to the pool.
// The schema argument is ignored: MySQL has no per-statement search_path and a
// `USE` would mutate the pooled connection, so unqualified names resolve against
// the connection's default database (the app qualifies generated SQL itself).
func (e *Engine) Query(ctx context.Context, sqlText string, readOnly bool, schema string) (*dbsql.Result, error) {
	_ = schema
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	start := time.Now()
	if readOnly {
		tx, err := e.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, err
		}
		defer tx.Rollback() // read-only: rollback is the cheap, correct close
		return runStatement(ctx, tx, sqlText, start)
	}
	return runStatement(ctx, e.db, sqlText, start)
}

// Exec runs a single mutating statement outside a read-only transaction.
func (e *Engine) Exec(ctx context.Context, sqlText string) (*dbsql.Result, error) {
	return e.Query(ctx, sqlText, false, "")
}

// runStatement executes one statement, returning either a capped row set or a
// command tag with the affected-row count.
func runStatement(ctx context.Context, q querier, sqlText string, start time.Time) (*dbsql.Result, error) {
	if !returnsRows(sqlText) {
		res, err := q.ExecContext(ctx, sqlText)
		if err != nil {
			return nil, err
		}
		aff, _ := res.RowsAffected()
		return &dbsql.Result{IsSelect: false, RowsAffected: aff, Command: commandTag(sqlText), Duration: time.Since(start)}, nil
	}
	rows, err := q.QueryContext(ctx, withMaxExecHint(sqlText))
	if err != nil {
		return nil, err
	}
	defer rows.Close() // always close, including after the maxRows break, so the conn returns clean
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := &dbsql.Result{IsSelect: true, Columns: cols}
	raw := make([]sql.RawBytes, len(cols))
	scan := make([]any, len(cols))
	for i := range raw {
		scan[i] = &raw[i]
	}
	for rows.Next() {
		if len(out.Rows) >= maxRows {
			out.Truncated = true
			break
		}
		if err := rows.Scan(scan...); err != nil {
			return nil, err
		}
		row := make([]string, len(cols))
		for i, rb := range raw {
			if rb == nil {
				row[i] = "∅" // NULL sentinel (matches the Postgres engine)
			} else {
				row[i] = string(rb) // copy now: RawBytes is invalid after the next Next()
			}
		}
		out.Rows = append(out.Rows, row)
	}
	out.Duration = time.Since(start)
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// BrowseWhere returns a page of rows with optional WHERE / ORDER BY fragments
// (raw SQL from the grid filter bar). Always read-only: the read-only transaction
// is what stops an injected expression from mutating data.
func (e *Engine) BrowseWhere(ctx context.Context, schema, table, where, order string, limit, offset int, readOnly bool) (*dbsql.Result, error) {
	if limit <= 0 || limit > maxRows {
		limit = 100
	}
	q := "SELECT * FROM " + qualified(schema, table)
	if w := strings.TrimSpace(where); w != "" {
		q += " WHERE " + w
	}
	if o := strings.TrimSpace(order); o != "" {
		q += " ORDER BY " + o
	}
	q += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	return e.Query(ctx, q, readOnly, "")
}

// ExecScript runs several statements as one transaction. NOTE: MySQL/MariaDB DDL
// implicitly commits and cannot be rolled back, so a mid-batch failure of a DDL
// edit leaves earlier statements applied (AtomicDDL reports false). Pure DML
// batches on InnoDB are atomic.
func (e *Engine) ExecScript(ctx context.Context, stmts []string) error {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, s := range stmts {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", truncateStmt(s), err)
		}
	}
	return tx.Commit()
}

// DatabaseName returns the connection's default database (empty if none).
func (e *Engine) DatabaseName(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	var name sql.NullString
	if err := e.db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&name); err != nil {
		return "", err
	}
	return name.String, nil
}

// Schemas lists non-system databases with their tables and estimated row counts.
// The LEFT JOIN keeps empty databases visible (mirrors the Postgres engine).
func (e *Engine) Schemas(ctx context.Context) ([]dbsql.Schema, error) {
	const q = `
SELECT s.SCHEMA_NAME AS db,
       t.TABLE_NAME  AS name,
       CASE t.TABLE_TYPE WHEN 'BASE TABLE' THEN 'table'
                         WHEN 'VIEW' THEN 'view'
                         ELSE LOWER(COALESCE(t.TABLE_TYPE,'')) END AS kind,
       COALESCE(t.TABLE_ROWS, 0) AS est_rows
FROM information_schema.SCHEMATA s
LEFT JOIN information_schema.TABLES t
       ON t.TABLE_SCHEMA = s.SCHEMA_NAME
WHERE s.SCHEMA_NAME NOT IN ('mysql','sys','performance_schema','information_schema')
ORDER BY s.SCHEMA_NAME, t.TABLE_NAME`
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	rows, err := e.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var order []string
	byName := map[string]*dbsql.Schema{}
	for rows.Next() {
		var (
			schema string
			name   sql.NullString
			kind   sql.NullString
			est    sql.NullInt64
		)
		if err := rows.Scan(&schema, &name, &kind, &est); err != nil {
			return nil, err
		}
		s, ok := byName[schema]
		if !ok {
			s = &dbsql.Schema{Name: schema}
			byName[schema] = s
			order = append(order, schema)
		}
		if name.Valid {
			s.Tables = append(s.Tables, dbsql.Table{Schema: schema, Name: name.String, Kind: kind.String, EstRows: est.Int64})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]dbsql.Schema, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out, nil
}

// Columns lists a relation's columns in ordinal order. COLUMN_TYPE (not
// DATA_TYPE) is used so lengths/unsigned/enum values show like pg's format_type.
func (e *Engine) Columns(ctx context.Context, schema, table string) ([]dbsql.Column, error) {
	const q = `
SELECT c.COLUMN_NAME,
       c.COLUMN_TYPE,
       (c.IS_NULLABLE = 'NO')            AS not_null,
       COALESCE(c.COLUMN_DEFAULT, '')    AS dflt,
       (c.COLUMN_KEY = 'PRI')            AS pk,
       (c.EXTRA LIKE '%auto_increment%') AS auto_inc
FROM information_schema.COLUMNS c
WHERE c.TABLE_SCHEMA = ? AND c.TABLE_NAME = ?
ORDER BY c.ORDINAL_POSITION`
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	rows, err := e.db.QueryContext(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dbsql.Column
	for rows.Next() {
		var c dbsql.Column
		if err := rows.Scan(&c.Name, &c.Type, &c.NotNull, &c.Default, &c.PK, &c.AutoInc); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Indexes lists a table's indexes (grouped from information_schema.STATISTICS).
// There is no SHOW-equivalent of pg_get_indexdef, so Def is reconstructed.
func (e *Engine) Indexes(ctx context.Context, schema, table string) ([]dbsql.Index, error) {
	const q = `
SELECT INDEX_NAME,
       (NON_UNIQUE = 0)         AS is_unique,
       (INDEX_NAME = 'PRIMARY') AS is_primary,
       GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ', ') AS cols
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
GROUP BY INDEX_NAME, NON_UNIQUE
ORDER BY (INDEX_NAME = 'PRIMARY') DESC, INDEX_NAME`
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	rows, err := e.db.QueryContext(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dbsql.Index
	for rows.Next() {
		var ix dbsql.Index
		var cols sql.NullString
		if err := rows.Scan(&ix.Name, &ix.Unique, &ix.Primary, &cols); err != nil {
			return nil, err
		}
		ix.Cols = cols.String
		ix.Def = reconstructIndexDef(table, ix)
		out = append(out, ix)
	}
	return out, rows.Err()
}

// Keys lists a table's constraints (primary/unique/foreign). FK definitions are
// reconstructed from REFERENTIAL_CONSTRAINTS (no pg_get_constraintdef analog).
func (e *Engine) Keys(ctx context.Context, schema, table string) ([]dbsql.Key, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	const cq = `
SELECT tc.CONSTRAINT_NAME, tc.CONSTRAINT_TYPE,
       GROUP_CONCAT(kcu.COLUMN_NAME ORDER BY kcu.ORDINAL_POSITION SEPARATOR ', ') AS cols
FROM information_schema.TABLE_CONSTRAINTS tc
JOIN information_schema.KEY_COLUMN_USAGE kcu
  ON kcu.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
 AND kcu.CONSTRAINT_NAME  = tc.CONSTRAINT_NAME
 AND kcu.TABLE_SCHEMA = tc.TABLE_SCHEMA
 AND kcu.TABLE_NAME   = tc.TABLE_NAME
WHERE tc.TABLE_SCHEMA = ? AND tc.TABLE_NAME = ?
  AND tc.CONSTRAINT_TYPE IN ('PRIMARY KEY','UNIQUE','FOREIGN KEY')
GROUP BY tc.CONSTRAINT_NAME, tc.CONSTRAINT_TYPE
ORDER BY FIELD(tc.CONSTRAINT_TYPE,'PRIMARY KEY','UNIQUE','FOREIGN KEY'), tc.CONSTRAINT_NAME`
	rows, err := e.db.QueryContext(ctx, cq, schema, table)
	if err != nil {
		return nil, err
	}
	type rawKey struct {
		ctype, cols string
	}
	order := []string{}
	keys := map[string]*rawKey{}
	for rows.Next() {
		var name, ctype string
		var cols sql.NullString
		if err := rows.Scan(&name, &ctype, &cols); err != nil {
			rows.Close()
			return nil, err
		}
		keys[name] = &rawKey{ctype: ctype, cols: cols.String}
		order = append(order, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// FK reference details, keyed by constraint name.
	fkDefs, err := e.foreignKeyDefs(ctx, schema, table)
	if err != nil {
		return nil, err
	}
	out := make([]dbsql.Key, 0, len(order))
	for _, name := range order {
		k := keys[name]
		key := dbsql.Key{Name: name, Cols: k.cols}
		switch k.ctype {
		case "PRIMARY KEY":
			key.Type = "primary"
			key.Def = "PRIMARY KEY (" + k.cols + ")"
		case "UNIQUE":
			key.Type = "unique"
			key.Def = "UNIQUE (" + k.cols + ")"
		case "FOREIGN KEY":
			key.Type = "foreign"
			if d, ok := fkDefs[name]; ok {
				key.Def = d
			} else {
				key.Def = "FOREIGN KEY (" + k.cols + ")"
			}
		default:
			key.Type = "other"
		}
		out = append(out, key)
	}
	return out, nil
}

// foreignKeyDefs returns a reconstructed "FOREIGN KEY (...) REFERENCES ..." per FK
// constraint name on schema.table.
func (e *Engine) foreignKeyDefs(ctx context.Context, schema, table string) (map[string]string, error) {
	const q = `
SELECT rc.CONSTRAINT_NAME,
       GROUP_CONCAT(kcu.COLUMN_NAME ORDER BY kcu.ORDINAL_POSITION SEPARATOR ', ')            AS cols,
       rc.REFERENCED_TABLE_NAME,
       GROUP_CONCAT(kcu.REFERENCED_COLUMN_NAME ORDER BY kcu.ORDINAL_POSITION SEPARATOR ', ') AS ref_cols,
       rc.UPDATE_RULE, rc.DELETE_RULE
FROM information_schema.REFERENTIAL_CONSTRAINTS rc
JOIN information_schema.KEY_COLUMN_USAGE kcu
  ON kcu.CONSTRAINT_SCHEMA = rc.CONSTRAINT_SCHEMA
 AND kcu.CONSTRAINT_NAME  = rc.CONSTRAINT_NAME
WHERE rc.CONSTRAINT_SCHEMA = ? AND rc.TABLE_NAME = ?
GROUP BY rc.CONSTRAINT_NAME, rc.REFERENCED_TABLE_NAME, rc.UPDATE_RULE, rc.DELETE_RULE`
	rows, err := e.db.QueryContext(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, cols, refTable, refCols, upd, del string
		if err := rows.Scan(&name, &cols, &refTable, &refCols, &upd, &del); err != nil {
			return nil, err
		}
		out[name] = fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s) ON UPDATE %s ON DELETE %s",
			cols, quoteIdent(refTable), refCols, upd, del)
	}
	return out, rows.Err()
}

// FindUsages lists foreign keys in other tables that reference schema.table.
func (e *Engine) FindUsages(ctx context.Context, schema, table string) ([]dbsql.Usage, error) {
	const q = `
SELECT kcu.TABLE_SCHEMA, kcu.TABLE_NAME, kcu.CONSTRAINT_NAME,
       GROUP_CONCAT(kcu.COLUMN_NAME ORDER BY kcu.ORDINAL_POSITION SEPARATOR ', ')            AS cols,
       GROUP_CONCAT(kcu.REFERENCED_COLUMN_NAME ORDER BY kcu.ORDINAL_POSITION SEPARATOR ', ') AS ref_cols
FROM information_schema.KEY_COLUMN_USAGE kcu
WHERE kcu.REFERENCED_TABLE_SCHEMA = ? AND kcu.REFERENCED_TABLE_NAME = ?
GROUP BY kcu.TABLE_SCHEMA, kcu.TABLE_NAME, kcu.CONSTRAINT_NAME
ORDER BY kcu.TABLE_SCHEMA, kcu.TABLE_NAME, kcu.CONSTRAINT_NAME`
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	rows, err := e.db.QueryContext(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dbsql.Usage
	for rows.Next() {
		var u dbsql.Usage
		var cols, refCols string
		if err := rows.Scan(&u.Schema, &u.Table, &u.Name, &cols, &refCols); err != nil {
			return nil, err
		}
		u.Def = fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)", cols, quoteIdent(table), refCols)
		out = append(out, u)
	}
	return out, rows.Err()
}

// TableComment returns the relation's comment (information_schema.TABLES).
func (e *Engine) TableComment(ctx context.Context, schema, table string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	var c sql.NullString
	err := e.db.QueryRowContext(ctx,
		"SELECT TABLE_COMMENT FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?",
		schema, table).Scan(&c)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return c.String, err
}

// CreateTableDDL returns the table's CREATE statement via SHOW CREATE TABLE,
// which already includes indexes and foreign keys inline.
func (e *Engine) CreateTableDDL(ctx context.Context, schema, table string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	var name, ddl string
	if err := e.db.QueryRowContext(ctx, "SHOW CREATE TABLE "+qualified(schema, table)).Scan(&name, &ddl); err != nil {
		return "", err
	}
	return ddl + ";", nil
}

// Roles lists database users (mysql.user). The privilege columns queried exist in
// both MySQL and MariaDB; account-lock state is not read (it differs across
// variants), so CanLogin is reported true and the editor's lock toggle drives it.
func (e *Engine) Roles(ctx context.Context) ([]dbsql.Role, error) {
	const q = `SELECT User, Host, Super_priv, Create_priv, Create_user_priv FROM mysql.user ORDER BY User, Host`
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	rows, err := e.db.QueryContext(ctx, q)
	if err != nil {
		// Many app accounts can't read mysql.user (1142 table-level / 1227 global
		// privilege denial). That's expected, not a failure: report no visible roles
		// so the explorer shows an empty list instead of a red error.
		var myErr *mysqldriver.MySQLError
		if errors.As(err, &myErr) && (myErr.Number == 1142 || myErr.Number == 1227) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []dbsql.Role
	for rows.Next() {
		var name, host, super, createPriv, createUser string
		if err := rows.Scan(&name, &host, &super, &createPriv, &createUser); err != nil {
			return nil, err
		}
		out = append(out, dbsql.Role{
			Name: name, Host: host, CanLogin: true,
			Super: super == "Y", CreateDB: createPriv == "Y", CreateRole: createUser == "Y",
		})
	}
	return out, rows.Err()
}

// ── statement helpers ──

// returnsRows reports whether a statement yields a result set (so it runs via
// QueryContext) versus a command (ExecContext, for the affected-row count).
func returnsRows(sqlText string) bool {
	switch firstWord(sqlText) {
	case "SELECT", "SHOW", "DESC", "DESCRIBE", "EXPLAIN", "WITH", "TABLE", "VALUES", "CALL", "ANALYZE", "CHECK", "HELP":
		return true
	}
	return false
}

// withMaxExecHint adds a statement-scoped MAX_EXECUTION_TIME optimizer hint to a
// bare SELECT (the only place MySQL honours it), leaving everything else as-is.
func withMaxExecHint(sqlText string) string {
	trimmed := strings.TrimLeft(sqlText, " \t\r\n")
	if firstWord(trimmed) != "SELECT" {
		return sqlText
	}
	return "SELECT " + fmt.Sprintf("/*+ MAX_EXECUTION_TIME(%d) */ ", maxExecMillis) + trimmed[len("SELECT"):]
}

func commandTag(sqlText string) string {
	w := firstWord(sqlText)
	if w == "" {
		return "OK"
	}
	return w
}

func firstWord(s string) string {
	s = strings.TrimLeft(s, " \t\r\n(")
	i := strings.IndexFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '(' || r == ';'
	})
	if i < 0 {
		i = len(s)
	}
	return strings.ToUpper(s[:i])
}

func truncateStmt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

func reconstructIndexDef(table string, ix dbsql.Index) string {
	if ix.Primary {
		return "PRIMARY KEY (" + ix.Cols + ")"
	}
	unique := ""
	if ix.Unique {
		unique = "UNIQUE "
	}
	return fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", unique, quoteIdent(ix.Name), quoteIdent(table), ix.Cols)
}
