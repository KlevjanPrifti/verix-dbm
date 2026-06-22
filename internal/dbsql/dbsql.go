// Package dbsql defines the engine-neutral types and interfaces shared by every
// SQL backend (PostgreSQL, MySQL/MariaDB). The web layer talks to a dbsql.Engine
// and never imports a concrete driver package directly, so adding an engine is a
// new package implementing these interfaces plus a registry dispatch entry.
//
// The DTO structs mirror what the JSON API serialises; engine packages convert
// their own driver rows into these. dbsql intentionally imports no engine
// package (avoids an import cycle): the postgres adapter copies pgx-typed structs
// into these field-for-field.
package dbsql

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// Table is one relation in a schema.
type Table struct {
	Schema  string
	Name    string
	Kind    string // table | view | matview
	EstRows int64
}

// Schema groups its tables. For MySQL a "schema" is a database.
type Schema struct {
	Name   string
	Tables []Table
}

// Column describes one column of a relation.
type Column struct {
	Name    string
	Type    string
	NotNull bool
	Default string
	PK      bool
	AutoInc bool
}

// TypeText is the display type shown in the tree: a shortened type name with an
// "(auto increment)" suffix. The string replacements
// are Postgres-isms that no-op on MySQL's already-short type names; AutoInc is set
// by the engine (never re-derived from the type string here).
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

// Cat is a coarse type category used to pick the column's icon glyph. The
// substring matches hold for both Postgres and MySQL type spellings.
func (c Column) Cat() string {
	if c.PK {
		return "pk"
	}
	t := strings.ToLower(c.Type)
	switch {
	case strings.Contains(t, "int"), strings.Contains(t, "numeric"), strings.Contains(t, "decimal"),
		strings.Contains(t, "real"), strings.Contains(t, "double"), strings.Contains(t, "money"),
		strings.Contains(t, "serial"), strings.Contains(t, "float"):
		return "num"
	case strings.Contains(t, "char"), strings.Contains(t, "text"), strings.Contains(t, "uuid"),
		strings.Contains(t, "enum"):
		return "text"
	case strings.Contains(t, "timestamp"), strings.Contains(t, "date"), strings.Contains(t, "time"),
		strings.Contains(t, "interval"), strings.Contains(t, "year"):
		return "time"
	case strings.Contains(t, "bool"):
		return "bool"
	case strings.Contains(t, "json"):
		return "json"
	default:
		return "col"
	}
}

// Index describes a table index.
type Index struct {
	Name    string
	Unique  bool
	Primary bool
	Def     string
	Cols    string
}

// Key describes a table constraint (primary/foreign/unique/check).
type Key struct {
	Name string
	Type string // primary | foreign | unique | check | other
	Def  string
	Cols string
}

// Role is a database account/role. Host is the MySQL `'user'@'host'` host part
// (empty for Postgres). The privilege attributes prefill the role editor.
type Role struct {
	Name        string
	Host        string
	Super       bool
	CreateDB    bool
	CreateRole  bool
	CanLogin    bool
	Replication bool
	ConnLimit   int
	ValidUntil  string
}

// Usage is one place a table is referenced from (an inbound foreign key).
type Usage struct {
	Schema string
	Table  string
	Name   string
	Def    string
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

// RoleAttrs is the privilege set the create/alter-role forms collect.
type RoleAttrs struct {
	Login      bool
	Super      bool
	CreateDB   bool
	CreateRole bool
	Password   string // create: "" => no password; alter: "" => leave unchanged
	Host       string // mysql: user host part (default "%"); ignored by Postgres
}

// FormSpec carries the typed fields of a form-driven DDL action. The dialect's
// FormSQL turns it into one or more statements; what's valid per kind matches the
// pre-existing Postgres form (add-column, modify-column, rename-table, new-schema,
// new-table, new-index, create-user).
type FormSpec struct {
	Kind     string
	Schema   string
	Table    string
	Column   string
	Name     string
	Type     string
	Default  string
	Columns  string
	Nullable bool
	Unique   bool
	Owner    string
	Role     RoleAttrs
}

// Dialect is the pure (no I/O) half of an engine: identifier quoting and the SQL
// string builders whose syntax differs per engine. Returning []string lets a
// builder emit a multi-statement edit (e.g. MySQL CREATE USER + GRANT).
type Dialect interface {
	QuoteIdent(s string) string
	QuoteLiteral(s string) string
	Qualified(schema, table string) string

	DropTableSQL(schema, table string) string
	TruncateSQL(schema, table string) string
	DropColumnSQL(schema, table, column string) string
	DropIndexSQL(schema, table, name string) string

	DropSchemaSQL(name string, cascade bool) []string
	AlterSchemaSQL(name, newName, owner string) []string
	AlterUserSQL(name, newName string, a RoleAttrs) []string
	DropUserSQL(name, host string) []string

	// FormSQL builds the statement list for a form-driven DDL action and the
	// audit action label. An empty kind or missing required field returns an error.
	FormSQL(spec FormSpec) (stmts []string, action string, err error)

	// IsServerSideExec reports whether sql reaches host OS program execution or
	// server-side file access (engine-specific; a read-only tx does NOT stop these).
	IsServerSideExec(sql string) bool

	// AtomicDDL reports whether a failed multi-statement DDL batch rolls back.
	// Postgres: true. MySQL/MariaDB: false (DDL implicitly commits).
	AtomicDDL() bool
}

// Engine is a live connection to one SQL target: the Dialect plus the
// introspection / query / DDL operations the SQL handlers call.
type Engine interface {
	Dialect

	Schemas(ctx context.Context) ([]Schema, error)
	DatabaseName(ctx context.Context) (string, error)
	Columns(ctx context.Context, schema, table string) ([]Column, error)
	Indexes(ctx context.Context, schema, table string) ([]Index, error)
	Keys(ctx context.Context, schema, table string) ([]Key, error)
	Roles(ctx context.Context) ([]Role, error)

	Query(ctx context.Context, sql string, readOnly bool, schema string) (*Result, error)
	BrowseWhere(ctx context.Context, schema, table, where, order string, limit, offset int, readOnly bool) (*Result, error)
	Exec(ctx context.Context, sql string) (*Result, error)
	ExecScript(ctx context.Context, stmts []string) error

	GenSelect(ctx context.Context, schema, table string) (string, error)
	GenInsert(ctx context.Context, schema, table string) (string, error)
	GenUpdate(ctx context.Context, schema, table string) (string, error)
	CreateTableDDL(ctx context.Context, schema, table string) (string, error)
	TableComment(ctx context.Context, schema, table string) (string, error)
	FindUsages(ctx context.Context, schema, table string) ([]Usage, error)
}

var (
	reDestructive = regexp.MustCompile(`(?is)^\s*(drop|truncate)\b`)
	reDelUpd      = regexp.MustCompile(`(?is)^\s*(delete|update)\b`)
	reHasWhere    = regexp.MustCompile(`(?is)\bwhere\b`)
)

// NeedsConfirm reports whether a statement is destructive enough to require an
// explicit confirmation: DROP/TRUNCATE, or a DELETE/UPDATE with no WHERE clause.
// Dialect-neutral, so it lives here and is shared by every SQL engine.
func NeedsConfirm(sql string) bool {
	if reDestructive.MatchString(sql) {
		return true
	}
	if reDelUpd.MatchString(sql) && !reHasWhere.MatchString(sql) {
		return true
	}
	return false
}

// Engine kind -> engine family. Mirrors internal/web/spa/src/dbkinds.ts so the
// backend and the connection picker agree on which engine serves each kind.
const (
	FamilyPostgres = "postgres"
	FamilyMySQL    = "mysql"
	FamilyRedis    = "redis"
	FamilySQLite   = "sqlite"
	FamilyMongo    = "mongodb"
)

var kindFamily = map[string]string{
	// PostgreSQL-wire kinds all share the Postgres engine.
	"postgres": FamilyPostgres, 
	"cockroach": FamilyPostgres, 
	"greenplum": FamilyPostgres,
	"redshift": FamilyPostgres, 
	"yugabyte": FamilyPostgres, 
	"timescale": FamilyPostgres,
	"aurorapg": FamilyPostgres,
	// MySQL-wire kinds share the MySQL engine.
	"mysql": FamilyMySQL, 
	"mariadb": FamilyMySQL,
	// Embedded file engine.
	"sqlite": FamilySQLite,
	// Document store (its own non-SQL vertical, like Redis).
	"mongodb": FamilyMongo,
	// Key-value.
	"redis": FamilyRedis,
}

// Family maps a connection kind id to its engine family. Unknown kinds default to
// Postgres (the historical fallback).
func Family(kind string) string {
	if f, ok := kindFamily[kind]; ok {
		return f
	}
	return FamilyPostgres
}
