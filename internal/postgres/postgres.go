// Package postgres provides introspection and query execution over a pgx pool.
package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Table is one relation in a schema.
type Table struct {
	Schema  string
	Name    string
	Kind    string // table | view | matview
	EstRows int64
}

// Schema groups its tables.
type Schema struct {
	Name   string
	Tables []Table
}

// Role is a cluster role/user (pg_roles). It carries the privilege attributes
// the role editor prefills from, plus the connection limit and validity the
// tree shows. Cluster-wide, so the API gates listing/editing to admins.
type Role struct {
	Name        string
	Super       bool
	CreateDB    bool
	CreateRole  bool
	CanLogin    bool
	Replication bool
	ConnLimit   int
	ValidUntil  string
}

// Roles lists cluster roles (pg_roles), skipping the built-in pg_* predefined
// roles so the UI shows only user-managed accounts.
func Roles(ctx context.Context, pool *pgxpool.Pool) ([]Role, error) {
	const q = `
SELECT rolname, rolsuper, rolcreatedb, rolcreaterole, rolcanlogin, rolreplication,
       rolconnlimit, COALESCE(rolvaliduntil::text, '')
FROM pg_roles
WHERE rolname NOT LIKE 'pg\_%'
ORDER BY rolname`
	rows, err := pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Role
	for rows.Next() {
		var rl Role
		if err := rows.Scan(&rl.Name, &rl.Super, &rl.CreateDB, &rl.CreateRole, &rl.CanLogin, &rl.Replication, &rl.ConnLimit, &rl.ValidUntil); err != nil {
			return nil, err
		}
		out = append(out, rl)
	}
	return out, rows.Err()
}

// Result is the outcome of a query: either a row set (IsSelect) or a command tag.
type Result struct {
	Columns      []string
	Rows         [][]string
	IsSelect     bool
	RowsAffected int64
	Command      string
	Duration     time.Duration
	Truncated    bool
}

const defaultStatementTimeout = "30s"

// Schemas lists non-system schemas with their relations and estimated row
// counts. It LEFT JOINs from pg_namespace so empty schemas (e.g. a fresh
// "public") still appear that lets the UI distinguish an empty database from
// a failed/misdirected connection.
func Schemas(ctx context.Context, pool *pgxpool.Pool) ([]Schema, error) {
	const q = `
SELECT n.nspname AS schema,
       c.relname AS name,
       CASE c.relkind WHEN 'r' THEN 'table' WHEN 'v' THEN 'view' WHEN 'm' THEN 'matview' WHEN 'p' THEN 'table' ELSE c.relkind::text END AS kind,
       COALESCE(c.reltuples,0)::bigint AS est_rows
FROM pg_namespace n
LEFT JOIN pg_class c
       ON c.relnamespace = n.oid
      AND c.relkind IN ('r','v','m','p')
WHERE n.nspname NOT IN ('pg_catalog','information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
  AND n.nspname NOT LIKE 'pg_temp%'
  AND n.nspname NOT LIKE 'pg_toast_temp%'
ORDER BY n.nspname, c.relname`
	rows, err := pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	order := []string{}
	byName := map[string]*Schema{}
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
			s = &Schema{Name: schema}
			byName[schema] = s
			order = append(order, schema)
		}
		// NULL name => the schema has no relations (LEFT JOIN miss).
		if name.Valid {
			s.Tables = append(s.Tables, Table{Schema: schema, Name: name.String, Kind: kind.String, EstRows: est.Int64})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Schema, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out, nil
}

// DatabaseName returns the database the pool is connected to (current_database()).
func DatabaseName(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var name string
	err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&name)
	return name, err
}

// Browse returns a page of rows from a table using identifier-quoted names.
// Browsing is always read-only.
func Browse(ctx context.Context, pool *pgxpool.Pool, schema, table string, limit, offset int) (*Result, error) {
	return BrowseWhere(ctx, pool, schema, table, "", "", limit, offset, true)
}

// BrowseWhere returns a page of rows with optional WHERE and ORDER BY fragments
// (raw SQL the user typed into the grid's filter bar). Callers always run this
// read-only (readOnly=true): the filter is unparameterized raw SQL, so the
// read-only transaction is what guarantees an injected expression can't mutate
// data or invoke a volatile/side-effecting function with write intent.
// Multi-statement input is additionally rejected by the pgx extended protocol.
func BrowseWhere(ctx context.Context, pool *pgxpool.Pool, schema, table, where, order string, limit, offset int, readOnly bool) (*Result, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := fmt.Sprintf("SELECT * FROM %s.%s", quoteIdent(schema), quoteIdent(table))
	if w := strings.TrimSpace(where); w != "" {
		q += " WHERE " + w
	}
	if o := strings.TrimSpace(order); o != "" {
		q += " ORDER BY " + o
	}
	q += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	return Query(ctx, pool, q, readOnly)
}

// Column describes one column of a relation (for the Explorer "columns" node).
type Column struct {
	Name    string
	Type    string
	NotNull bool
	Default string
	PK      bool
	AutoInc bool
}

// TypeText is the display type shown in the tree: shortened type name, with an
// "(auto increment)" / "not null" suffix the way DataGrip renders it.
func (c Column) TypeText() string {
	t := c.Type
	t = strings.ReplaceAll(t, "character varying", "varchar")
	t = strings.ReplaceAll(t, "timestamp without time zone", "timestamp")
	t = strings.ReplaceAll(t, "time without time zone", "time")
	if c.AutoInc {
		t += " (auto increment)"
	}
	return t
}

// Cat is a coarse type category used to pick the column's icon glyph.
func (c Column) Cat() string {
	if c.PK {
		return "pk"
	}
	t := strings.ToLower(c.Type)
	switch {
	case strings.Contains(t, "int"), strings.Contains(t, "numeric"), strings.Contains(t, "decimal"),
		strings.Contains(t, "real"), strings.Contains(t, "double"), strings.Contains(t, "money"), strings.Contains(t, "serial"):
		return "num"
	case strings.Contains(t, "char"), strings.Contains(t, "text"), strings.Contains(t, "uuid"):
		return "text"
	case strings.Contains(t, "timestamp"), strings.Contains(t, "date"), strings.Contains(t, "time"), strings.Contains(t, "interval"):
		return "time"
	case strings.Contains(t, "bool"):
		return "bool"
	case strings.Contains(t, "json"):
		return "json"
	default:
		return "col"
	}
}

// Columns lists the columns of a table/view in ordinal order.
func Columns(ctx context.Context, pool *pgxpool.Pool, schema, table string) ([]Column, error) {
	const q = `
SELECT a.attname,
       format_type(a.atttypid, a.atttypmod) AS type,
       a.attnotnull,
       COALESCE(pg_get_expr(d.adbin, d.adrelid), '') AS dflt,
       COALESCE((SELECT true FROM pg_index ix
                  WHERE ix.indrelid = a.attrelid AND ix.indisprimary
                    AND a.attnum = ANY(ix.indkey)), false) AS pk,
       (a.attidentity <> '') AS ident
FROM pg_attribute a
JOIN pg_class c     ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`
	rows, err := pool.Query(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var col Column
		var ident bool
		if err := rows.Scan(&col.Name, &col.Type, &col.NotNull, &col.Default, &col.PK, &ident); err != nil {
			return nil, err
		}
		col.AutoInc = ident || strings.HasPrefix(strings.ToLower(col.Default), "nextval")
		out = append(out, col)
	}
	return out, rows.Err()
}

// parenContents returns the text inside the first (or last) (...) pair of s.
func parenContents(s string, last bool) string {
	open := strings.Index(s, "(")
	if last {
		open = strings.LastIndex(s, "(")
	}
	if open < 0 {
		return ""
	}
	rel := strings.Index(s[open:], ")")
	if rel < 0 {
		return ""
	}
	return s[open+1 : open+rel]
}

// Index describes a table index (for the Explorer "indexes" node).
type Index struct {
	Name    string
	Unique  bool
	Primary bool
	Def     string
	Cols    string // column list parsed from the index definition
}

// Indexes lists the indexes on a table.
func Indexes(ctx context.Context, pool *pgxpool.Pool, schema, table string) ([]Index, error) {
	const q = `
SELECT c2.relname, i.indisunique, i.indisprimary, pg_get_indexdef(i.indexrelid)
FROM pg_index i
JOIN pg_class c      ON c.oid = i.indrelid
JOIN pg_namespace n  ON n.oid = c.relnamespace
JOIN pg_class c2     ON c2.oid = i.indexrelid
WHERE n.nspname = $1 AND c.relname = $2
ORDER BY c2.relname`
	rows, err := pool.Query(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Index
	for rows.Next() {
		var ix Index
		if err := rows.Scan(&ix.Name, &ix.Unique, &ix.Primary, &ix.Def); err != nil {
			return nil, err
		}
		ix.Cols = parenContents(ix.Def, true)
		out = append(out, ix)
	}
	return out, rows.Err()
}

// Key describes a table constraint (for the Explorer "keys" node).
type Key struct {
	Name string
	Type string // primary | foreign | unique | check | other
	Def  string
	Cols string // column list parsed from the constraint definition
}

// Keys lists the constraints on a table (primary/foreign/unique/check).
func Keys(ctx context.Context, pool *pgxpool.Pool, schema, table string) ([]Key, error) {
	const q = `
SELECT con.conname, con.contype, pg_get_constraintdef(con.oid)
FROM pg_constraint con
JOIN pg_class c      ON c.oid = con.conrelid
JOIN pg_namespace n  ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2
ORDER BY con.contype, con.conname`
	rows, err := pool.Query(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Key
	for rows.Next() {
		var k Key
		var t string
		if err := rows.Scan(&k.Name, &t, &k.Def); err != nil {
			return nil, err
		}
		k.Type = constraintType(t)
		k.Cols = parenContents(k.Def, false)
		out = append(out, k)
	}
	return out, rows.Err()
}

func constraintType(c string) string {
	switch c {
	case "p":
		return "primary"
	case "f":
		return "foreign"
	case "u":
		return "unique"
	case "c":
		return "check"
	default:
		return c
	}
}

var (
	reDestructive = regexp.MustCompile(`(?is)^\s*(drop|truncate)\b`)
	reDelUpd      = regexp.MustCompile(`(?is)^\s*(delete|update)\b`)
	reHasWhere    = regexp.MustCompile(`(?is)\bwhere\b`)
)

// NeedsConfirm reports whether a statement is destructive enough to require an
// explicit confirmation: DROP/TRUNCATE, or a DELETE/UPDATE with no WHERE clause.
func NeedsConfirm(sql string) bool {
	if reDestructive.MatchString(sql) {
		return true
	}
	if reDelUpd.MatchString(sql) && !reHasWhere.MatchString(sql) {
		return true
	}
	return false
}

// Query runs arbitrary SQL. readOnly wraps it in a read-only transaction.
func Query(ctx context.Context, pool *pgxpool.Pool, sql string, readOnly bool) (*Result, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SET statement_timeout = '"+defaultStatementTimeout+"'"); err != nil {
		return nil, err
	}
	// This is a session-level setting on a pooled connection, so it must be set
	// explicitly every time, otherwise a connection left in read-only mode by a
	// prior query gets reused for a write and fails with SQLSTATE 25006.
	if readOnly {
		if _, err := conn.Exec(ctx, "SET default_transaction_read_only = on"); err != nil {
			return nil, err
		}
	} else {
		if _, err := conn.Exec(ctx, "SET default_transaction_read_only = off"); err != nil {
			return nil, err
		}
	}

	start := time.Now()
	rows, err := conn.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fds := rows.FieldDescriptions()
	res := &Result{Duration: 0}
	if len(fds) == 0 {
		// Command (INSERT/UPDATE/DELETE/DDL without a row set).
		rows.Close()
		tag := rows.CommandTag()
		res.IsSelect = false
		res.RowsAffected = tag.RowsAffected()
		res.Command = tag.String()
		res.Duration = time.Since(start)
		return res, rows.Err()
	}

	res.IsSelect = true
	for _, fd := range fds {
		res.Columns = append(res.Columns, string(fd.Name))
	}
	const maxRows = 1000
	for rows.Next() {
		if len(res.Rows) >= maxRows {
			res.Truncated = true
			break
		}
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		row := make([]string, len(vals))
		for i, v := range vals {
			row[i] = format(v)
		}
		res.Rows = append(res.Rows, row)
	}
	res.Duration = time.Since(start)
	if err := rows.Err(); err != nil {
		return res, err
	}
	return res, nil
}

func format(v any) string {
	switch x := v.(type) {
	case nil:
		return "∅" // NULL
	case []byte:
		return string(x)
	case time.Time:
		return x.Format(time.RFC3339)
	case string:
		return x
	default:
		// pgx returns several types (numeric, interval, bit, …) as pgtype
		// structs whose %v is a raw field dump. Most implement driver.Valuer,
		// which yields the clean scalar (e.g. numeric → "60.5"); recurse so the
		// resulting string/[]byte/number is formatted normally.
		if val, ok := x.(driver.Valuer); ok {
			if dv, err := val.Value(); err == nil && dv != nil {
				return format(dv)
			}
		}
		return fmt.Sprintf("%v", x)
	}
}

// quoteIdent safely double-quotes a SQL identifier.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// reServerSide matches SQL that reaches the database host's OS program
// execution and server-side file access none of which a read-only transaction
// blocks. They depend purely on the connected role's privileges.
var reServerSide = regexp.MustCompile(`(?is)` +
	`\bcopy\b[^;]*\bprogram\b` + // COPY … TO/FROM PROGRAM 'cmd'
	`|\bpg_read_file\b|\bpg_read_binary_file\b|\bpg_ls_dir\b|\bpg_stat_file\b|\bpg_ls_logdir\b|\bpg_ls_waldir\b` +
	`|\blo_import\b|\blo_export\b` +
	`|\bpg_execute_server_program\b|\bpg_read_server_files\b|\bpg_write_server_files\b`)

// IsServerSideExec reports whether sql uses a server-side execution / file
// primitive (COPY … PROGRAM, pg_read_file, large-object import/export, …). These
// can yield RCE or arbitrary file read on the database host when the connected
// role is privileged and a read-only transaction does NOT stop them. Callers
// block them for non-admin users (defense in depth; the real control is using a
// least-privileged DB role). This is a deliberately conservative keyword screen,
// not a SQL parser, so it can over-match that's the safe direction here.
func IsServerSideExec(sql string) bool {
	return reServerSide.MatchString(sql)
}
