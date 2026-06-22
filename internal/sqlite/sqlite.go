// Package sqlite provides introspection and query execution over a database/sql
// pool for SQLite database files (pure-Go modernc.org/sqlite driver). It
// implements dbsql.Engine so the web layer treats it exactly like the Postgres
// and MySQL engines, reusing the grid/console/doc/usages surfaces unchanged.
//
// SQLite has no network address, no users/roles, and a single database
// namespace, so introspection comes from sqlite_master and the table-valued
// PRAGMA functions, and a synthetic schema named "main" stands in for the one
// namespace. Read-only execution pins a connection and toggles
// PRAGMA query_only (the SQLite analog of a read-only transaction), always
// clearing it before the connection returns to the shared pool.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"verix-dbm/internal/dbsql"
)

const (
	statementTimeout = 30 * time.Second
	maxRows          = 1000
	// mainSchema is the single namespace SQLite exposes; it stands in for the
	// per-database "schema" the SPA's tree and grid expect.
	mainSchema = "main"
)

// Engine is a live SQLite connection (a *sql.DB pool over one file).
type Engine struct{ db *sql.DB }

// New wraps an open *sql.DB as a dbsql.Engine.
func New(db *sql.DB) *Engine { return &Engine{db: db} }

var _ dbsql.Engine = (*Engine)(nil)

// querier is satisfied by *sql.DB, *sql.Tx, and *sql.Conn.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Query runs arbitrary SQL. readOnly pins a single connection and sets
// PRAGMA query_only=ON for the call (SQLite has no read-only transaction option
// the driver enforces). The flag is always reset to OFF before the connection
// returns to the pool, using a fresh context so a cancelled ctx can't skip the
// reset and leave a permanently read-only pooled connection. The schema argument
// is ignored: SQLite has one namespace and the app qualifies generated SQL.
func (e *Engine) Query(ctx context.Context, sqlText string, readOnly bool, schema string) (*dbsql.Result, error) {
	_ = schema
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	start := time.Now()
	if !readOnly {
		return runStatement(ctx, e.db, sqlText, start)
	}
	conn, err := e.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	// LIFO: query_only=OFF runs first (resetting the pooled conn), then Close.
	defer conn.Close()
	defer func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rcancel()
		_, _ = conn.ExecContext(rctx, "PRAGMA query_only=OFF")
	}()
	if _, err := conn.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		return nil, err
	}
	return runStatement(ctx, conn, sqlText, start)
}

// Exec runs a single mutating statement (no read-only guard).
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
	rows, err := q.QueryContext(ctx, sqlText)
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
				row[i] = "∅" // NULL sentinel (matches the Postgres/MySQL engines)
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
// (raw SQL from the grid filter bar). Always read-only: the query_only guard is
// what stops an injected expression from mutating data.
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

// ExecScript runs several statements as one transaction. SQLite DDL is
// transactional, so a mid-batch failure rolls the whole batch back
// (AtomicDDL reports true).
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

// DatabaseName returns the file's base name (or "main" for an in-memory DB).
func (e *Engine) DatabaseName(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	rows, err := e.db.QueryContext(ctx, "SELECT name, file FROM pragma_database_list")
	if err != nil {
		return mainSchema, nil //nolint:nilerr // best-effort label, never fatal
	}
	defer rows.Close()
	for rows.Next() {
		var name, file string
		if err := rows.Scan(&name, &file); err != nil {
			return mainSchema, nil //nolint:nilerr
		}
		if name == mainSchema && file != "" {
			return filepath.Base(file), nil
		}
	}
	return mainSchema, nil
}

// Schemas returns SQLite's single namespace ("main") with its tables and views.
// est_rows is left at 0: SQLite has no cheap row estimate without a scan.
func (e *Engine) Schemas(ctx context.Context) ([]dbsql.Schema, error) {
	const q = `
SELECT name,
       CASE type WHEN 'view' THEN 'view' ELSE 'table' END AS kind
FROM sqlite_master
WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%'
ORDER BY type, name`
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	rows, err := e.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	s := dbsql.Schema{Name: mainSchema}
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, err
		}
		s.Tables = append(s.Tables, dbsql.Table{Schema: mainSchema, Name: name, Kind: kind})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return []dbsql.Schema{s}, nil
}

// Columns lists a relation's columns in declared order. AutoInc is approximated:
// an INTEGER PRIMARY KEY is SQLite's rowid alias and auto-assigns.
func (e *Engine) Columns(ctx context.Context, schema, table string) ([]dbsql.Column, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	rows, err := e.db.QueryContext(ctx,
		`SELECT name, type, "notnull", IFNULL(dflt_value,''), pk FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dbsql.Column
	for rows.Next() {
		var (
			c       dbsql.Column
			notnull int
			pk      int
		)
		if err := rows.Scan(&c.Name, &c.Type, &notnull, &c.Default, &pk); err != nil {
			return nil, err
		}
		c.NotNull = notnull != 0
		c.PK = pk != 0
		c.AutoInc = c.PK && strings.Contains(strings.ToUpper(c.Type), "INT")
		out = append(out, c)
	}
	return out, rows.Err()
}

// Indexes lists a table's indexes via PRAGMA index_list / index_info. The
// original CREATE INDEX text is read from sqlite_master when present (it is NULL
// for the implicit indexes backing PRIMARY KEY / UNIQUE constraints).
func (e *Engine) Indexes(ctx context.Context, schema, table string) ([]dbsql.Index, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	rows, err := e.db.QueryContext(ctx,
		`SELECT name, "unique", origin FROM pragma_index_list(?) ORDER BY seq`, table)
	if err != nil {
		return nil, err
	}
	type rawIx struct {
		name   string
		unique bool
		origin string
	}
	var list []rawIx
	for rows.Next() {
		var ix rawIx
		var uniq int
		if err := rows.Scan(&ix.name, &uniq, &ix.origin); err != nil {
			rows.Close()
			return nil, err
		}
		ix.unique = uniq != 0
		list = append(list, ix)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]dbsql.Index, 0, len(list))
	for _, ix := range list {
		cols, err := e.indexColumns(ctx, ix.name)
		if err != nil {
			return nil, err
		}
		idx := dbsql.Index{Name: ix.name, Unique: ix.unique, Primary: ix.origin == "pk", Cols: cols}
		if def, ok := e.indexDef(ctx, ix.name); ok {
			idx.Def = def
		} else {
			idx.Def = reconstructIndexDef(table, idx)
		}
		out = append(out, idx)
	}
	return out, nil
}

func (e *Engine) indexColumns(ctx context.Context, index string) (string, error) {
	rows, err := e.db.QueryContext(ctx, `SELECT name FROM pragma_index_info(?) ORDER BY seqno`, index)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var n sql.NullString
		if err := rows.Scan(&n); err != nil {
			return "", err
		}
		if n.Valid {
			cols = append(cols, n.String)
		}
	}
	return strings.Join(cols, ", "), rows.Err()
}

// indexDef returns the stored CREATE INDEX text, or ok=false for implicit indexes.
func (e *Engine) indexDef(ctx context.Context, index string) (string, bool) {
	var def sql.NullString
	err := e.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&def)
	if err != nil || !def.Valid || def.String == "" {
		return "", false
	}
	return def.String, true
}

// Keys lists a table's constraints: primary key, unique indexes, and foreign keys.
func (e *Engine) Keys(ctx context.Context, schema, table string) ([]dbsql.Key, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	var out []dbsql.Key

	// Primary key (columns ordered by their position in the key).
	pkRows, err := e.db.QueryContext(ctx,
		`SELECT name FROM pragma_table_info(?) WHERE pk>0 ORDER BY pk`, table)
	if err != nil {
		return nil, err
	}
	var pkCols []string
	for pkRows.Next() {
		var n string
		if err := pkRows.Scan(&n); err != nil {
			pkRows.Close()
			return nil, err
		}
		pkCols = append(pkCols, n)
	}
	pkRows.Close()
	if err := pkRows.Err(); err != nil {
		return nil, err
	}
	if len(pkCols) > 0 {
		cols := strings.Join(pkCols, ", ")
		out = append(out, dbsql.Key{Name: "PRIMARY", Type: "primary", Cols: cols, Def: "PRIMARY KEY (" + cols + ")"})
	}

	// Unique constraints (indexes with origin 'u').
	uRows, err := e.db.QueryContext(ctx,
		`SELECT name FROM pragma_index_list(?) WHERE origin='u' ORDER BY seq`, table)
	if err != nil {
		return nil, err
	}
	var uniques []string
	for uRows.Next() {
		var n string
		if err := uRows.Scan(&n); err != nil {
			uRows.Close()
			return nil, err
		}
		uniques = append(uniques, n)
	}
	uRows.Close()
	if err := uRows.Err(); err != nil {
		return nil, err
	}
	for _, name := range uniques {
		cols, err := e.indexColumns(ctx, name)
		if err != nil {
			return nil, err
		}
		out = append(out, dbsql.Key{Name: name, Type: "unique", Cols: cols, Def: "UNIQUE (" + cols + ")"})
	}

	// Foreign keys (grouped by the pragma's id, which numbers each constraint).
	fkRows, err := e.db.QueryContext(ctx,
		`SELECT id, "from", "table", "to" FROM pragma_foreign_key_list(?) ORDER BY id, seq`, table)
	if err != nil {
		return nil, err
	}
	defer fkRows.Close()
	type fk struct {
		cols, refTable, refCols string
	}
	order := []int{}
	byID := map[int]*fk{}
	for fkRows.Next() {
		var id int
		var from, refTable, to string
		if err := fkRows.Scan(&id, &from, &refTable, &to); err != nil {
			return nil, err
		}
		f, ok := byID[id]
		if !ok {
			f = &fk{refTable: refTable}
			byID[id] = f
			order = append(order, id)
		}
		f.cols = appendCol(f.cols, from)
		f.refCols = appendCol(f.refCols, to)
	}
	if err := fkRows.Err(); err != nil {
		return nil, err
	}
	for _, id := range order {
		f := byID[id]
		out = append(out, dbsql.Key{
			Name: fmt.Sprintf("fk_%s_%d", table, id),
			Type: "foreign",
			Cols: f.cols,
			Def:  fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)", f.cols, quoteIdent(f.refTable), f.refCols),
		})
	}
	return out, nil
}

// FindUsages lists foreign keys in other tables that reference schema.table.
func (e *Engine) FindUsages(ctx context.Context, schema, table string) ([]dbsql.Usage, error) {
	const q = `
SELECT m.name AS child, fk.id AS fkid, fk."from" AS col, fk."to" AS refcol
FROM sqlite_master m
JOIN pragma_foreign_key_list(m.name) fk
WHERE m.type='table' AND fk."table" = ?
ORDER BY m.name, fk.id, fk.seq`
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	rows, err := e.db.QueryContext(ctx, q, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type acc struct {
		child, cols, refCols string
	}
	order := []string{}
	byKey := map[string]*acc{}
	for rows.Next() {
		var child, col, refCol string
		var fkid int
		if err := rows.Scan(&child, &fkid, &col, &refCol); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s/%d", child, fkid)
		a, ok := byKey[key]
		if !ok {
			a = &acc{child: child}
			byKey[key] = a
			order = append(order, key)
		}
		a.cols = appendCol(a.cols, col)
		a.refCols = appendCol(a.refCols, refCol)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]dbsql.Usage, 0, len(order))
	for _, key := range order {
		a := byKey[key]
		out = append(out, dbsql.Usage{
			Schema: mainSchema,
			Table:  a.child,
			Name:   key,
			Def:    fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)", a.cols, quoteIdent(table), a.refCols),
		})
	}
	return out, nil
}

// TableComment returns "": SQLite has no table comments.
func (e *Engine) TableComment(ctx context.Context, schema, table string) (string, error) {
	return "", nil
}

// CreateTableDDL returns the relation's original CREATE statement from sqlite_master.
func (e *Engine) CreateTableDDL(ctx context.Context, schema, table string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	var ddl sql.NullString
	err := e.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type IN ('table','view') AND name=?`, table).Scan(&ddl)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("relation %q not found", table)
	}
	if err != nil {
		return "", err
	}
	if !ddl.Valid {
		return "", nil
	}
	return ddl.String + ";", nil
}

// Roles returns nil: SQLite has no users or roles.
func (e *Engine) Roles(ctx context.Context) ([]dbsql.Role, error) { return nil, nil }

// ── statement helpers ──

// returnsRows reports whether a statement yields a result set (QueryContext)
// versus a command (ExecContext, for the affected-row count). Like the MySQL
// engine, a DML statement with a RETURNING clause is the known edge case this
// first-word check does not catch.
func returnsRows(sqlText string) bool {
	switch firstWord(sqlText) {
	case "SELECT", "WITH", "VALUES", "PRAGMA", "EXPLAIN":
		return true
	}
	return false
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

// appendCol joins a comma-separated column accumulator.
func appendCol(acc, col string) string {
	if acc == "" {
		return col
	}
	return acc + ", " + col
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
