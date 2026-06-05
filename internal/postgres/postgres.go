// Package postgres provides introspection and query execution over a pgx pool.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Table is one relation in a schema.
type Table struct {
	Schema    string
	Name      string
	Kind      string // table | view | matview
	EstRows   int64
}

// Schema groups its tables.
type Schema struct {
	Name   string
	Tables []Table
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
// "public") still appear — that lets the UI distinguish an empty database from
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
func Browse(ctx context.Context, pool *pgxpool.Pool, schema, table string, limit, offset int) (*Result, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := fmt.Sprintf("SELECT * FROM %s.%s LIMIT %d OFFSET %d", quoteIdent(schema), quoteIdent(table), limit, offset)
	return Query(ctx, pool, q, false)
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
	if readOnly {
		if _, err := conn.Exec(ctx, "SET default_transaction_read_only = on"); err != nil {
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
		return fmt.Sprintf("%v", x)
	}
}

// quoteIdent safely double-quotes a SQL identifier.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
