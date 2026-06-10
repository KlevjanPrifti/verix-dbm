package web

import (
	"fmt"
	"strings"

	"verix-dbm/internal/postgres"
	"verix-dbm/internal/store"
)

// ddlForm and buildFormSQL back the form-driven DDL actions. The legacy
// server-rendered DDL modals have been removed; the JSON API in api.go is now
// the sole caller, translating its typed request into a ddlForm.

// ddlForm holds the parameters the DDL builders need.
type ddlForm struct {
	Conn     store.Connection
	CSRF     string
	Kind     string // add-column | modify-column | rename-table | new-schema | new-table | new-index | create-user
	Schema   string
	Table    string
	Column   string
	Name     string
	Type     string
	Nullable bool
	Default  string
	Columns  string
	Unique   bool
	Owner    string // new-schema: optional AUTHORIZATION role
	// create-user / create-role fields
	Password   string
	Login      bool
	CreateDB   bool
	CreateRole bool
	Superuser  bool
	Err        string
}

// roleAttrs projects the create/alter-role privilege fields onto the shared
// postgres.RoleAttrs the SQL builders consume.
func (f ddlForm) roleAttrs() postgres.RoleAttrs {
	return postgres.RoleAttrs{
		Login:      f.Login,
		Super:      f.Superuser,
		CreateDB:   f.CreateDB,
		CreateRole: f.CreateRole,
		Password:   f.Password,
	}
}

func buildFormSQL(f ddlForm) (sql, action string, err error) {
	tbl := postgres.Qualified(f.Schema, f.Table)
	switch f.Kind {
	case "add-column":
		if f.Name == "" || f.Type == "" {
			return "", "", fmt.Errorf("column name and type are required")
		}
		sql = fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tbl, postgres.QuoteIdent(f.Name), f.Type)
		if !f.Nullable {
			sql += " NOT NULL"
		}
		if f.Default != "" {
			sql += " DEFAULT " + f.Default
		}
		return sql, "pg_ddl_add_column", nil
	case "modify-column":
		if f.Type == "" {
			return "", "", fmt.Errorf("type is required")
		}
		col := postgres.QuoteIdent(f.Column)
		parts := []string{fmt.Sprintf("ALTER COLUMN %s TYPE %s", col, f.Type)}
		if f.Nullable {
			parts = append(parts, fmt.Sprintf("ALTER COLUMN %s DROP NOT NULL", col))
		} else {
			parts = append(parts, fmt.Sprintf("ALTER COLUMN %s SET NOT NULL", col))
		}
		if f.Default != "" {
			parts = append(parts, fmt.Sprintf("ALTER COLUMN %s SET DEFAULT %s", col, f.Default))
		} else {
			parts = append(parts, fmt.Sprintf("ALTER COLUMN %s DROP DEFAULT", col))
		}
		return fmt.Sprintf("ALTER TABLE %s %s", tbl, strings.Join(parts, ", ")), "pg_ddl_modify_column", nil
	case "rename-table":
		if f.Name == "" {
			return "", "", fmt.Errorf("new name is required")
		}
		return fmt.Sprintf("ALTER TABLE %s RENAME TO %s", tbl, postgres.QuoteIdent(f.Name)), "pg_ddl_rename_table", nil
	case "new-schema":
		if f.Name == "" {
			return "", "", fmt.Errorf("schema name is required")
		}
		sql = "CREATE SCHEMA " + postgres.QuoteIdent(f.Name)
		if f.Owner != "" {
			sql += " AUTHORIZATION " + postgres.QuoteIdent(f.Owner)
		}
		return sql, "pg_ddl_create_schema", nil
	case "new-table":
		if f.Name == "" || f.Columns == "" {
			return "", "", fmt.Errorf("table name and column definitions are required")
		}
		return fmt.Sprintf("CREATE TABLE %s (\n%s\n)", postgres.Qualified(f.Schema, f.Name), f.Columns), "pg_ddl_create_table", nil
	case "new-index":
		if f.Name == "" || f.Columns == "" {
			return "", "", fmt.Errorf("index name and columns are required")
		}
		unique := ""
		if f.Unique {
			unique = "UNIQUE "
		}
		return fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", unique, postgres.QuoteIdent(f.Name), tbl, f.Columns), "pg_ddl_create_index", nil
	case "create-user":
		if f.Name == "" {
			return "", "", fmt.Errorf("role name is required")
		}
		return postgres.CreateRoleSQL(f.Name, f.roleAttrs()), "pg_ddl_create_role", nil
	}
	return "", "", fmt.Errorf("unknown form kind %q", f.Kind)
}
