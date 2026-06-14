package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"verix-dbm/internal/dbsql"
)

// Engine adapts this package's pool-based functions to the dbsql.Engine
// interface. It is a thin shim: the proven introspection/query/DDL code in
// postgres.go and ddl.go is unchanged; Engine only forwards calls and copies the
// package's structs into the engine-neutral dbsql.* DTOs at the boundary.
type Engine struct{ pool *pgxpool.Pool }

// New wraps a pgx pool as a dbsql.Engine.
func New(pool *pgxpool.Pool) *Engine { return &Engine{pool: pool} }

var _ dbsql.Engine = (*Engine)(nil)

// ── Dialect ──

func (e *Engine) QuoteIdent(s string) string            { return quoteIdent(s) }
func (e *Engine) QuoteLiteral(s string) string          { return QuoteLiteral(s) }
func (e *Engine) Qualified(schema, table string) string { return Qualified(schema, table) }
func (e *Engine) IsServerSideExec(sql string) bool      { return IsServerSideExec(sql) }
func (e *Engine) AtomicDDL() bool                       { return true }

func (e *Engine) DropTableSQL(schema, table string) string {
	return "DROP TABLE " + Qualified(schema, table)
}
func (e *Engine) TruncateSQL(schema, table string) string {
	return "TRUNCATE TABLE " + Qualified(schema, table)
}
func (e *Engine) DropColumnSQL(schema, table, column string) string {
	return "ALTER TABLE " + Qualified(schema, table) + " DROP COLUMN " + quoteIdent(column)
}
func (e *Engine) DropIndexSQL(schema, table, name string) string {
	_ = table // pg indexes are schema-scoped, not table-qualified
	return "DROP INDEX " + Qualified(schema, name)
}
func (e *Engine) DropSchemaSQL(name string, cascade bool) []string {
	return []string{DropSchemaSQL(name, cascade)}
}
func (e *Engine) AlterSchemaSQL(name, newName, owner string) []string {
	return AlterSchemaSQL(name, newName, owner)
}
func (e *Engine) AlterUserSQL(name, newName string, a dbsql.RoleAttrs) []string {
	return AlterRoleSQL(name, newName, toRoleAttrs(a))
}
func (e *Engine) DropUserSQL(name, host string) []string {
	_ = host // pg roles have no host part
	return []string{DropRoleSQL(name)}
}

// FormSQL ports the form-driven DDL builder (formerly web.buildFormSQL) so the
// Postgres dialect owns its syntax. Returns a one-element statement list for
// every kind (pg DDL is single-statement here).
func (e *Engine) FormSQL(f dbsql.FormSpec) ([]string, string, error) {
	tbl := Qualified(f.Schema, f.Table)
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
		return []string{sql}, "pg_ddl_add_column", nil
	case "modify-column":
		if f.Type == "" {
			return nil, "", fmt.Errorf("type is required")
		}
		col := quoteIdent(f.Column)
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
		return []string{fmt.Sprintf("ALTER TABLE %s %s", tbl, strings.Join(parts, ", "))}, "pg_ddl_modify_column", nil
	case "rename-table":
		if f.Name == "" {
			return nil, "", fmt.Errorf("new name is required")
		}
		return []string{fmt.Sprintf("ALTER TABLE %s RENAME TO %s", tbl, quoteIdent(f.Name))}, "pg_ddl_rename_table", nil
	case "new-schema":
		if f.Name == "" {
			return nil, "", fmt.Errorf("schema name is required")
		}
		sql := "CREATE SCHEMA " + quoteIdent(f.Name)
		if f.Owner != "" {
			sql += " AUTHORIZATION " + quoteIdent(f.Owner)
		}
		return []string{sql}, "pg_ddl_create_schema", nil
	case "new-table":
		if f.Name == "" || f.Columns == "" {
			return nil, "", fmt.Errorf("table name and column definitions are required")
		}
		return []string{fmt.Sprintf("CREATE TABLE %s (\n%s\n)", Qualified(f.Schema, f.Name), f.Columns)}, "pg_ddl_create_table", nil
	case "new-index":
		if f.Name == "" || f.Columns == "" {
			return nil, "", fmt.Errorf("index name and columns are required")
		}
		unique := ""
		if f.Unique {
			unique = "UNIQUE "
		}
		return []string{fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", unique, quoteIdent(f.Name), tbl, f.Columns)}, "pg_ddl_create_index", nil
	case "create-user":
		if f.Name == "" {
			return nil, "", fmt.Errorf("role name is required")
		}
		return []string{CreateRoleSQL(f.Name, toRoleAttrs(f.Role))}, "pg_ddl_create_role", nil
	}
	return nil, "", fmt.Errorf("unknown form kind %q", f.Kind)
}

// ── Engine (introspection / query / DDL) ──

func (e *Engine) Schemas(ctx context.Context) ([]dbsql.Schema, error) {
	ss, err := Schemas(ctx, e.pool)
	if err != nil {
		return nil, err
	}
	out := make([]dbsql.Schema, len(ss))
	for i, s := range ss {
		tables := make([]dbsql.Table, len(s.Tables))
		for j, t := range s.Tables {
			tables[j] = dbsql.Table{Schema: t.Schema, Name: t.Name, Kind: t.Kind, EstRows: t.EstRows}
		}
		out[i] = dbsql.Schema{Name: s.Name, Tables: tables}
	}
	return out, nil
}

func (e *Engine) DatabaseName(ctx context.Context) (string, error) { return DatabaseName(ctx, e.pool) }

func (e *Engine) Columns(ctx context.Context, schema, table string) ([]dbsql.Column, error) {
	cs, err := Columns(ctx, e.pool, schema, table)
	if err != nil {
		return nil, err
	}
	out := make([]dbsql.Column, len(cs))
	for i, c := range cs {
		out[i] = toDBColumn(c)
	}
	return out, nil
}

func (e *Engine) Indexes(ctx context.Context, schema, table string) ([]dbsql.Index, error) {
	ix, err := Indexes(ctx, e.pool, schema, table)
	if err != nil {
		return nil, err
	}
	out := make([]dbsql.Index, len(ix))
	for i, x := range ix {
		out[i] = dbsql.Index{Name: x.Name, Unique: x.Unique, Primary: x.Primary, Def: x.Def, Cols: x.Cols}
	}
	return out, nil
}

func (e *Engine) Keys(ctx context.Context, schema, table string) ([]dbsql.Key, error) {
	ks, err := Keys(ctx, e.pool, schema, table)
	if err != nil {
		return nil, err
	}
	out := make([]dbsql.Key, len(ks))
	for i, k := range ks {
		out[i] = dbsql.Key{Name: k.Name, Type: k.Type, Def: k.Def, Cols: k.Cols}
	}
	return out, nil
}

func (e *Engine) Roles(ctx context.Context) ([]dbsql.Role, error) {
	rs, err := Roles(ctx, e.pool)
	if err != nil {
		return nil, err
	}
	out := make([]dbsql.Role, len(rs))
	for i, r := range rs {
		out[i] = dbsql.Role{
			Name: r.Name, Super: r.Super, CreateDB: r.CreateDB, CreateRole: r.CreateRole,
			CanLogin: r.CanLogin, Replication: r.Replication, ConnLimit: r.ConnLimit, ValidUntil: r.ValidUntil,
		}
	}
	return out, nil
}

func (e *Engine) Query(ctx context.Context, sql string, readOnly bool, schema string) (*dbsql.Result, error) {
	r, err := Query(ctx, e.pool, sql, readOnly, schema)
	return toDBResult(r), err
}

func (e *Engine) BrowseWhere(ctx context.Context, schema, table, where, order string, limit, offset int, readOnly bool) (*dbsql.Result, error) {
	r, err := BrowseWhere(ctx, e.pool, schema, table, where, order, limit, offset, readOnly)
	return toDBResult(r), err
}

func (e *Engine) Exec(ctx context.Context, sql string) (*dbsql.Result, error) {
	r, err := Exec(ctx, e.pool, sql)
	return toDBResult(r), err
}

func (e *Engine) ExecScript(ctx context.Context, stmts []string) error {
	return ExecScript(ctx, e.pool, stmts)
}

func (e *Engine) GenSelect(ctx context.Context, schema, table string) (string, error) {
	return GenSelect(ctx, e.pool, schema, table)
}
func (e *Engine) GenInsert(ctx context.Context, schema, table string) (string, error) {
	return GenInsert(ctx, e.pool, schema, table)
}
func (e *Engine) GenUpdate(ctx context.Context, schema, table string) (string, error) {
	return GenUpdate(ctx, e.pool, schema, table)
}
func (e *Engine) CreateTableDDL(ctx context.Context, schema, table string) (string, error) {
	return CreateTableDDL(ctx, e.pool, schema, table)
}
func (e *Engine) TableComment(ctx context.Context, schema, table string) (string, error) {
	return TableComment(ctx, e.pool, schema, table)
}

func (e *Engine) FindUsages(ctx context.Context, schema, table string) ([]dbsql.Usage, error) {
	us, err := FindUsages(ctx, e.pool, schema, table)
	if err != nil {
		return nil, err
	}
	out := make([]dbsql.Usage, len(us))
	for i, u := range us {
		out[i] = dbsql.Usage{Schema: u.Schema, Table: u.Table, Name: u.Name, Def: u.Def}
	}
	return out, nil
}

// ── conversions ──

func toDBColumn(c Column) dbsql.Column {
	return dbsql.Column{Name: c.Name, Type: c.Type, NotNull: c.NotNull, Default: c.Default, PK: c.PK, AutoInc: c.AutoInc}
}

func toDBResult(r *Result) *dbsql.Result {
	if r == nil {
		return nil
	}
	return &dbsql.Result{
		Columns: r.Columns, Rows: r.Rows, IsSelect: r.IsSelect, RowsAffected: r.RowsAffected,
		Command: r.Command, Duration: r.Duration, Truncated: r.Truncated,
	}
}

func toRoleAttrs(a dbsql.RoleAttrs) RoleAttrs {
	return RoleAttrs{Login: a.Login, Super: a.Super, CreateDB: a.CreateDB, CreateRole: a.CreateRole, Password: a.Password}
}
