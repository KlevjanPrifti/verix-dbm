package mysql

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"verix-dbm/internal/dbsql"
)

// ── identifier / literal quoting (backticks) ──

func quoteIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

func qualified(schema, table string) string {
	if schema == "" {
		return quoteIdent(table)
	}
	return quoteIdent(schema) + "." + quoteIdent(table)
}

// quoteLiteral single-quotes a value, escaping the quote and backslash (MySQL
// treats backslash as an escape unless NO_BACKSLASH_ESCAPES is set).
func quoteLiteral(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "''")
	return "'" + s + "'"
}

func (e *Engine) QuoteIdent(s string) string            { return quoteIdent(s) }
func (e *Engine) QuoteLiteral(s string) string          { return quoteLiteral(s) }
func (e *Engine) Qualified(schema, table string) string { return qualified(schema, table) }
func (e *Engine) AtomicDDL() bool                       { return false } // MySQL DDL implicitly commits

func (e *Engine) DropTableSQL(schema, table string) string {
	return "DROP TABLE " + qualified(schema, table)
}
func (e *Engine) TruncateSQL(schema, table string) string {
	return "TRUNCATE TABLE " + qualified(schema, table)
}
func (e *Engine) DropColumnSQL(schema, table, column string) string {
	return "ALTER TABLE " + qualified(schema, table) + " DROP COLUMN " + quoteIdent(column)
}

// DropIndexSQL: unlike Postgres, a MySQL index is dropped relative to its table.
func (e *Engine) DropIndexSQL(schema, table, name string) string {
	return "ALTER TABLE " + qualified(schema, table) + " DROP INDEX " + quoteIdent(name)
}

// DropSchemaSQL drops a database; MySQL has no CASCADE keyword (dropping the
// database removes its tables regardless), so cascade is ignored.
func (e *Engine) DropSchemaSQL(name string, cascade bool) []string {
	_ = cascade
	return []string{"DROP DATABASE " + quoteIdent(name)}
}

// AlterSchemaSQL: MySQL cannot rename a database or set an owner, so there is no
// safe statement to emit. Returns nil ("nothing to change"); the SPA hides
// schema-alter for MySQL connections.
func (e *Engine) AlterSchemaSQL(name, newName, owner string) []string {
	_, _, _ = name, newName, owner
	return nil
}

// ── users (the MySQL analog of Postgres roles) ──

func userHost(name, host string) string {
	if host == "" {
		host = "%"
	}
	return quoteLiteral(name) + "@" + quoteLiteral(host)
}

// grantsFor returns the global GRANT statements for the privilege flags. The
// MySQL role editor is additive: it grants what is checked and never REVOKEs, so
// editing privileges can't silently strip a user's unrelated grants.
func grantsFor(uh string, a dbsql.RoleAttrs) []string {
	var out []string
	if a.Super {
		out = append(out, "GRANT SUPER ON *.* TO "+uh)
	}
	if a.CreateDB {
		out = append(out, "GRANT CREATE ON *.* TO "+uh)
	}
	if a.CreateRole {
		out = append(out, "GRANT CREATE USER ON *.* TO "+uh)
	}
	return out
}

func (e *Engine) AlterUserSQL(name, newName string, a dbsql.RoleAttrs) []string {
	uh := userHost(name, a.Host)
	var stmts []string
	if newName != "" && newName != name {
		nh := userHost(newName, a.Host)
		stmts = append(stmts, "RENAME USER "+uh+" TO "+nh)
		uh = nh
	}
	if a.Password != "" {
		stmts = append(stmts, "ALTER USER "+uh+" IDENTIFIED BY "+quoteLiteral(a.Password))
	}
	if a.Login {
		stmts = append(stmts, "ALTER USER "+uh+" ACCOUNT UNLOCK")
	} else {
		stmts = append(stmts, "ALTER USER "+uh+" ACCOUNT LOCK")
	}
	stmts = append(stmts, grantsFor(uh, a)...)
	return stmts
}

func (e *Engine) DropUserSQL(name, host string) []string {
	return []string{"DROP USER " + userHost(name, host)}
}

// ── form-driven DDL ──

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
		return []string{sql}, "mysql_ddl_add_column", nil
	case "modify-column":
		if f.Type == "" {
			return nil, "", fmt.Errorf("type is required")
		}
		// MySQL MODIFY redefines the whole column at once.
		sql := fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s", tbl, quoteIdent(f.Column), f.Type)
		if f.Nullable {
			sql += " NULL"
		} else {
			sql += " NOT NULL"
		}
		if f.Default != "" {
			sql += " DEFAULT " + f.Default
		}
		return []string{sql}, "mysql_ddl_modify_column", nil
	case "rename-table":
		if f.Name == "" {
			return nil, "", fmt.Errorf("new name is required")
		}
		return []string{fmt.Sprintf("ALTER TABLE %s RENAME TO %s", tbl, qualified(f.Schema, f.Name))}, "mysql_ddl_rename_table", nil
	case "new-schema":
		if f.Name == "" {
			return nil, "", fmt.Errorf("database name is required")
		}
		return []string{"CREATE DATABASE " + quoteIdent(f.Name)}, "mysql_ddl_create_schema", nil
	case "new-table":
		if f.Name == "" || f.Columns == "" {
			return nil, "", fmt.Errorf("table name and column definitions are required")
		}
		return []string{fmt.Sprintf("CREATE TABLE %s (\n%s\n)", qualified(f.Schema, f.Name), f.Columns)}, "mysql_ddl_create_table", nil
	case "new-index":
		if f.Name == "" || f.Columns == "" {
			return nil, "", fmt.Errorf("index name and columns are required")
		}
		unique := ""
		if f.Unique {
			unique = "UNIQUE "
		}
		return []string{fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", unique, quoteIdent(f.Name), tbl, f.Columns)}, "mysql_ddl_create_index", nil
	case "create-user":
		if f.Name == "" {
			return nil, "", fmt.Errorf("user name is required")
		}
		uh := userHost(f.Name, f.Role.Host)
		create := "CREATE USER " + uh
		if f.Role.Password != "" {
			create += " IDENTIFIED BY " + quoteLiteral(f.Role.Password)
		}
		stmts := []string{create}
		if !f.Role.Login {
			stmts = append(stmts, "ALTER USER "+uh+" ACCOUNT LOCK")
		}
		stmts = append(stmts, grantsFor(uh, f.Role)...)
		return stmts, "mysql_ddl_create_user", nil
	}
	return nil, "", fmt.Errorf("unknown form kind %q", f.Kind)
}

// ── server-side execution / file-access screen (MySQL primitives) ──

var reServerSide = regexp.MustCompile(`(?is)` +
	`\bload\s+data\b[^;]*\binfile\b` + // LOAD DATA [LOCAL] INFILE
	`|\binto\s+outfile\b|\binto\s+dumpfile\b` + // SELECT ... INTO OUTFILE/DUMPFILE
	`|\bload_file\s*\(`) // LOAD_FILE('/etc/passwd')

// IsServerSideExec reports whether sql uses a MySQL host file-access primitive
// (LOAD DATA INFILE, INTO OUTFILE/DUMPFILE, LOAD_FILE). These are not stopped by
// a read-only transaction and are blocked for non-admins (defense in depth). The
// package-level form lets callers screen SQL by engine without opening a pool.
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
