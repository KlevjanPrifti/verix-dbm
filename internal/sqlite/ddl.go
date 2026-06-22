package sqlite

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"verix-dbm/internal/dbsql"
)

// ── identifier / literal quoting (double quotes) ──

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func qualified(schema, table string) string {
	if schema == "" || schema == mainSchema {
		return quoteIdent(table)
	}
	return quoteIdent(schema) + "." + quoteIdent(table)
}

// quoteLiteral single-quotes a value, doubling the quote. SQLite, unlike MySQL,
// does not treat backslash as an escape, so only the single quote is escaped.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func (e *Engine) QuoteIdent(s string) string            { return quoteIdent(s) }
func (e *Engine) QuoteLiteral(s string) string          { return quoteLiteral(s) }
func (e *Engine) Qualified(schema, table string) string { return qualified(schema, table) }
func (e *Engine) AtomicDDL() bool                       { return true } // SQLite DDL is transactional

func (e *Engine) DropTableSQL(schema, table string) string {
	return "DROP TABLE " + qualified(schema, table)
}

// TruncateSQL: SQLite has no TRUNCATE; an unqualified DELETE is the equivalent
// (SQLite applies the "truncate optimization" to a DELETE with no WHERE).
func (e *Engine) TruncateSQL(schema, table string) string {
	return "DELETE FROM " + qualified(schema, table)
}

func (e *Engine) DropColumnSQL(schema, table, column string) string {
	return "ALTER TABLE " + qualified(schema, table) + " DROP COLUMN " + quoteIdent(column)
}

// DropIndexSQL: SQLite indexes live in the database namespace, not under a table,
// so they are dropped by (schema-qualified) name like Postgres.
func (e *Engine) DropIndexSQL(schema, table, name string) string {
	_ = table
	return "DROP INDEX " + qualified(schema, name)
}

// DropSchemaSQL / AlterSchemaSQL: SQLite has a single namespace, so there is no
// schema to drop or rename. Returns nil ("nothing to do"); the SPA hides these
// actions for engines that report no statements.
func (e *Engine) DropSchemaSQL(name string, cascade bool) []string  { return nil }
func (e *Engine) AlterSchemaSQL(name, newName, owner string) []string { return nil }

// AlterUserSQL / DropUserSQL: SQLite has no users or roles.
func (e *Engine) AlterUserSQL(name, newName string, a dbsql.RoleAttrs) []string { return nil }
func (e *Engine) DropUserSQL(name, host string) []string                        { return nil }

// ── form-driven DDL ──

// FormSQL builds the statement list for a form action. SQLite's ALTER TABLE is
// limited: it can add/drop/rename columns and rename a table, but cannot change a
// column's type or drop a constraint in place, and it has no schemas or users.
// Unsupported actions return a clear error rather than emitting invalid SQL.
func (e *Engine) FormSQL(f dbsql.FormSpec) ([]string, string, error) {
	tbl := qualified(f.Schema, f.Table)
	switch f.Kind {
	case "add-column":
		if f.Name == "" || f.Type == "" {
			return nil, "", fmt.Errorf("column name and type are required")
		}
		sql := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tbl, quoteIdent(f.Name), f.Type)
		if !f.Nullable {
			sql += " NOT NULL"
		}
		if f.Default != "" {
			sql += " DEFAULT " + f.Default
		}
		return []string{sql}, "sqlite_ddl_add_column", nil
	case "rename-table":
		if f.Name == "" {
			return nil, "", fmt.Errorf("new name is required")
		}
		return []string{fmt.Sprintf("ALTER TABLE %s RENAME TO %s", tbl, qualified(f.Schema, f.Name))}, "sqlite_ddl_rename_table", nil
	case "new-table":
		if f.Name == "" || f.Columns == "" {
			return nil, "", fmt.Errorf("table name and column definitions are required")
		}
		return []string{fmt.Sprintf("CREATE TABLE %s (\n%s\n)", qualified(f.Schema, f.Name), f.Columns)}, "sqlite_ddl_create_table", nil
	case "new-index":
		if f.Name == "" || f.Columns == "" {
			return nil, "", fmt.Errorf("index name and columns are required")
		}
		unique := ""
		if f.Unique {
			unique = "UNIQUE "
		}
		return []string{fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", unique, quoteIdent(f.Name), tbl, f.Columns)}, "sqlite_ddl_create_index", nil
	case "modify-column":
		return nil, "", fmt.Errorf("SQLite cannot modify a column in place; recreate the table to change its type")
	case "new-schema":
		return nil, "", fmt.Errorf("SQLite has a single namespace and does not support creating schemas")
	case "create-user":
		return nil, "", fmt.Errorf("SQLite has no users or roles")
	}
	return nil, "", fmt.Errorf("unknown form kind %q", f.Kind)
}

// ── server-side execution / file-access screen (SQLite primitives) ──

var reServerSide = regexp.MustCompile(`(?is)` +
	`\battach\b` + // ATTACH DATABASE '/path' (opens another file)
	`|\bload_extension\s*\(` + // load_extension('evil.so') => arbitrary code
	`|\bvacuum\b[^;]*\binto\b` + // VACUUM INTO 'file' (writes a file)
	`|\bwritefile\s*\(|\breadfile\s*\(`) // fileio extension helpers

// IsServerSideExec reports whether sql reaches another file or host code via a
// SQLite primitive (ATTACH, load_extension, VACUUM INTO, writefile/readfile).
// These are not stopped by query_only and are blocked for non-admins as defense
// in depth. The package-level form lets callers screen SQL without a pool.
func IsServerSideExec(sqlText string) bool { return reServerSide.MatchString(sqlText) }

func (e *Engine) IsServerSideExec(sqlText string) bool { return IsServerSideExec(sqlText) }

// ── statement generators ──

func (e *Engine) GenSelect(ctx context.Context, schema, table string) (string, error) {
	cols, err := e.Columns(ctx, schema, table)
	if err != nil {
		return "", err
	}
	if len(cols) == 0 {
		return fmt.Sprintf("SELECT *\nFROM %s\nLIMIT 100;", qualified(schema, table)), nil
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = quoteIdent(c.Name)
	}
	return fmt.Sprintf("SELECT %s\nFROM %s\nLIMIT 100;", strings.Join(names, ", "), qualified(schema, table)), nil
}

func (e *Engine) GenInsert(ctx context.Context, schema, table string) (string, error) {
	cols, err := e.Columns(ctx, schema, table)
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
		qualified(schema, table), strings.Join(names, ", "), strings.Join(vals, ", ")), nil
}

func (e *Engine) GenUpdate(ctx context.Context, schema, table string) (string, error) {
	cols, err := e.Columns(ctx, schema, table)
	if err != nil {
		return "", err
	}
	sets := make([]string, 0, len(cols))
	for _, c := range cols {
		if c.PK {
			continue
		}
		sets = append(sets, quoteIdent(c.Name)+" = null")
	}
	return fmt.Sprintf("UPDATE %s\nSET %s\nWHERE <condition>;",
		qualified(schema, table), strings.Join(sets, ",\n    ")), nil
}
