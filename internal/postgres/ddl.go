package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// QuoteIdent safely double-quotes a SQL identifier (exported wrapper around the
// internal quoteIdent). Use this for every schema/table/column/index name that
// originates from user input before it goes into a statement string.
func QuoteIdent(s string) string { return quoteIdent(s) }

// QuoteLiteral safely single-quotes a string literal.
func QuoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// Qualified returns "schema"."table" with both identifiers quoted.
func Qualified(schema, table string) string {
	return quoteIdent(schema) + "." + quoteIdent(table)
}

// Exec runs a single mutating statement (DDL/DML) outside a read-only
// transaction. It is the write counterpart of Query(..., readOnly=true).
func Exec(ctx context.Context, pool *pgxpool.Pool, sql string) (*Result, error) {
	return Query(ctx, pool, sql, false)
}

// ── Statement generators (read-only: introspect, then build a string) ──────────

// GenSelect builds "SELECT col, … FROM s.t;" listing the real columns.
func GenSelect(ctx context.Context, pool *pgxpool.Pool, schema, table string) (string, error) {
	cols, err := Columns(ctx, pool, schema, table)
	if err != nil {
		return "", err
	}
	if len(cols) == 0 {
		return fmt.Sprintf("SELECT *\nFROM %s\nLIMIT 100;", Qualified(schema, table)), nil
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = quoteIdent(c.Name)
	}
	return fmt.Sprintf("SELECT %s\nFROM %s\nLIMIT 100;", strings.Join(names, ", "), Qualified(schema, table)), nil
}

// GenInsert builds "INSERT INTO s.t (cols) VALUES (null, …);".
func GenInsert(ctx context.Context, pool *pgxpool.Pool, schema, table string) (string, error) {
	cols, err := Columns(ctx, pool, schema, table)
	if err != nil {
		return "", err
	}
	names := make([]string, len(cols))
	vals := make([]string, len(cols))
	for i, c := range cols {
		names[i] = quoteIdent(c.Name)
		vals[i] = "null"
	}
	return fmt.Sprintf("INSERT INTO %s (%s)\nVALUES (%s);",
		Qualified(schema, table), strings.Join(names, ", "), strings.Join(vals, ", ")), nil
}

// GenUpdate builds "UPDATE s.t SET col = null, … WHERE …;".
func GenUpdate(ctx context.Context, pool *pgxpool.Pool, schema, table string) (string, error) {
	cols, err := Columns(ctx, pool, schema, table)
	if err != nil {
		return "", err
	}
	sets := make([]string, 0, len(cols))
	for _, c := range cols {
		if c.PK {
			continue // don't suggest updating the primary key
		}
		sets = append(sets, quoteIdent(c.Name)+" = null")
	}
	return fmt.Sprintf("UPDATE %s\nSET %s\nWHERE <condition>;",
		Qualified(schema, table), strings.Join(sets, ",\n    ")), nil
}

// CreateTableDDL reconstructs a CREATE TABLE statement (plus any standalone
// indexes) from live introspection. It's the "Copy DDL" / "Generate CREATE"
// source and is purely read-only.
func CreateTableDDL(ctx context.Context, pool *pgxpool.Pool, schema, table string) (string, error) {
	cols, err := Columns(ctx, pool, schema, table)
	if err != nil {
		return "", err
	}
	keys, err := Keys(ctx, pool, schema, table)
	if err != nil {
		return "", err
	}
	idx, err := Indexes(ctx, pool, schema, table)
	if err != nil {
		return "", err
	}

	var lines []string
	for _, c := range cols {
		line := "    " + quoteIdent(c.Name) + " " + c.Type
		if c.NotNull {
			line += " NOT NULL"
		}
		if c.Default != "" {
			line += " DEFAULT " + c.Default
		}
		lines = append(lines, line)
	}
	// Constraints rendered with their canonical pg definition.
	keyNames := map[string]bool{}
	for _, k := range keys {
		keyNames[k.Name] = true
		lines = append(lines, "    CONSTRAINT "+quoteIdent(k.Name)+" "+k.Def)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n%s\n);", Qualified(schema, table), strings.Join(lines, ",\n"))

	// Standalone indexes not backing a constraint (pg_get_indexdef is complete).
	for _, ix := range idx {
		if keyNames[ix.Name] || ix.Primary {
			continue
		}
		b.WriteString("\n\n" + ix.Def + ";")
	}
	return b.String(), nil
}

// TableComment returns the relation's COMMENT, if any.
func TableComment(ctx context.Context, pool *pgxpool.Pool, schema, table string) (string, error) {
	const q = `
SELECT COALESCE(obj_description(c.oid), '')
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`
	var s string
	err := pool.QueryRow(ctx, q, schema, table).Scan(&s)
	return s, err
}

// Usage is one place a table is referenced from (an inbound foreign key).
type Usage struct {
	Schema string // schema of the referencing table
	Table  string // referencing table
	Name   string // constraint name
	Def    string // constraint definition
}

// FindUsages lists foreign keys in other tables that reference schema.table.
func FindUsages(ctx context.Context, pool *pgxpool.Pool, schema, table string) ([]Usage, error) {
	const q = `
SELECT n.nspname, c.relname, con.conname, pg_get_constraintdef(con.oid)
FROM pg_constraint con
JOIN pg_class c       ON c.oid = con.conrelid
JOIN pg_namespace n   ON n.oid = c.relnamespace
JOIN pg_class tc      ON tc.oid = con.confrelid
JOIN pg_namespace tn  ON tn.oid = tc.relnamespace
WHERE con.contype = 'f' AND tn.nspname = $1 AND tc.relname = $2
ORDER BY n.nspname, c.relname, con.conname`
	rows, err := pool.Query(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Usage
	for rows.Next() {
		var u Usage
		if err := rows.Scan(&u.Schema, &u.Table, &u.Name, &u.Def); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
