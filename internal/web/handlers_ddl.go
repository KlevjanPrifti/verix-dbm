package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/postgres"
	"verix-dbm/internal/store"
)

// Read-only generators (introspect → text/plain for the clipboard)

// pgDDL returns the CREATE TABLE statement for a relation as plain text.
func (s *Server) pgDDL(w http.ResponseWriter, r *http.Request) {
	_, pool, ok := s.pgPoolFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	ddl, err := postgres.CreateTableDDL(r.Context(), pool, q.Get("schema"), q.Get("table"))
	writeText(w, ddl, err)
}

// pgGenerate returns a SELECT/INSERT/UPDATE/CREATE statement as plain text.
func (s *Server) pgGenerate(w http.ResponseWriter, r *http.Request) {
	_, pool, ok := s.pgPoolFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	schema, table := q.Get("schema"), q.Get("table")
	var (
		sql string
		err error
	)
	switch q.Get("kind") {
	case "select":
		sql, err = postgres.GenSelect(r.Context(), pool, schema, table)
	case "insert":
		sql, err = postgres.GenInsert(r.Context(), pool, schema, table)
	case "update":
		sql, err = postgres.GenUpdate(r.Context(), pool, schema, table)
	case "create":
		sql, err = postgres.CreateTableDDL(r.Context(), pool, schema, table)
	default:
		http.Error(w, "unknown kind", http.StatusBadRequest)
		return
	}
	writeText(w, sql, err)
}

func writeText(w http.ResponseWriter, body string, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(body))
}

// pgDoc opens a "quick documentation" tab: columns + comment for a relation.
func (s *Server) pgDoc(w http.ResponseWriter, r *http.Request) {
	c, pool, ok := s.pgPoolFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	schema, table := q.Get("schema"), q.Get("table")
	d := map[string]any{"Conn": c, "Schema": schema, "Table": table}
	cols, err := postgres.Columns(r.Context(), pool, schema, table)
	if err != nil {
		d["Err"] = err.Error()
		s.rnd.partial(w, "docResult", d)
		return
	}
	d["Columns"] = cols
	d["Keys"], _ = postgres.Keys(r.Context(), pool, schema, table)
	d["Indexes"], _ = postgres.Indexes(r.Context(), pool, schema, table)
	d["Comment"], _ = postgres.TableComment(r.Context(), pool, schema, table)
	s.rnd.partial(w, "docResult", d)
}

// pgUsages opens a tab listing inbound foreign keys referencing a relation.
func (s *Server) pgUsages(w http.ResponseWriter, r *http.Request) {
	c, pool, ok := s.pgPoolFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	schema, table := q.Get("schema"), q.Get("table")
	d := map[string]any{"Conn": c, "Schema": schema, "Table": table}
	usages, err := postgres.FindUsages(r.Context(), pool, schema, table)
	if err != nil {
		d["Err"] = err.Error()
	}
	d["Usages"] = usages
	s.rnd.partial(w, "usagesResult", d)
}

// Mutating DDL

// requireWrite resolves the connection + pool while enforcing CSRF, write/admin
// role, and the connection's read-only flag. It writes the error response and
// returns ok=false on any failure — the client-side menu gating is UX only.
func (s *Server) requireWrite(w http.ResponseWriter, r *http.Request, admin bool) (auth.User, store.Connection, *pgxpool.Pool, bool) {
	u, _ := auth.FromContext(r.Context())
	if !s.auth.CheckCSRF(r) {
		http.Error(w, "bad csrf", http.StatusForbidden)
		return u, store.Connection{}, nil, false
	}
	if admin && !u.Admin {
		http.Error(w, "admin required", http.StatusForbidden)
		return u, store.Connection{}, nil, false
	}
	if !u.Write {
		http.Error(w, "write access required", http.StatusForbidden)
		return u, store.Connection{}, nil, false
	}
	c, err := s.connFor(r)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return u, store.Connection{}, nil, false
	}
	if c.ReadOnly {
		http.Error(w, "connection is read-only", http.StatusConflict)
		return u, c, nil, false
	}
	pool, err := s.reg.PG(r.Context(), c)
	if err != nil {
		http.Error(w, "connect: "+err.Error(), http.StatusBadGateway)
		return u, c, nil, false
	}
	return u, c, pool, true
}

// execDDL runs a generated statement, audits it, and — on success — asks the
// client to refresh the affected connection's subtree via an HX-Trigger header.
// (Fetch-based callers ignore the header and refresh themselves.)
func (s *Server) execDDL(w http.ResponseWriter, r *http.Request, u auth.User, c store.Connection, pool *pgxpool.Pool, action, sql string) error {
	_, err := postgres.Exec(r.Context(), pool, sql)
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: action, Detail: truncate(sql, 500), Success: err == nil})
	if err != nil {
		return err
	}
	w.Header().Set("HX-Trigger", fmt.Sprintf(`{"verix:refreshConn":%d}`, c.ID))
	return nil
}

// confirm-style endpoints: JS posts via fetch after a confirm() prompt; errors
// come back as plain text the client shows in an alert.

func (s *Server) pgDropTable(w http.ResponseWriter, r *http.Request) {
	u, c, pool, ok := s.requireWrite(w, r, true)
	if !ok {
		return
	}
	sql := "DROP TABLE " + postgres.Qualified(r.FormValue("schema"), r.FormValue("table"))
	if err := s.execDDL(w, r, u, c, pool, "pg_ddl_drop_table", sql); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func (s *Server) pgTruncate(w http.ResponseWriter, r *http.Request) {
	u, c, pool, ok := s.requireWrite(w, r, true)
	if !ok {
		return
	}
	sql := "TRUNCATE TABLE " + postgres.Qualified(r.FormValue("schema"), r.FormValue("table"))
	if err := s.execDDL(w, r, u, c, pool, "pg_ddl_truncate", sql); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func (s *Server) pgDropColumn(w http.ResponseWriter, r *http.Request) {
	u, c, pool, ok := s.requireWrite(w, r, true)
	if !ok {
		return
	}
	sql := fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s",
		postgres.Qualified(r.FormValue("schema"), r.FormValue("table")),
		postgres.QuoteIdent(r.FormValue("column")))
	if err := s.execDDL(w, r, u, c, pool, "pg_ddl_drop_column", sql); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func (s *Server) pgDropIndex(w http.ResponseWriter, r *http.Request) {
	u, c, pool, ok := s.requireWrite(w, r, true)
	if !ok {
		return
	}
	sql := "DROP INDEX " + postgres.Qualified(r.FormValue("schema"), r.FormValue("name"))
	if err := s.execDDL(w, r, u, c, pool, "pg_ddl_drop_index", sql); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

// Form-backed DDL (a modal collects parameters, then POSTs here)

// ddlForm holds everything the DDL modals need, both for the initial GET render
// and for re-rendering with .Err after a failed POST.
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
	Owner string // new-schema: optional AUTHORIZATION role
	// create-user / create-role fields
	Password   string
	Login      bool
	CreateDB   bool
	CreateRole bool
	Superuser  bool
	Err        string
}

// pgDDLForm renders the parameter modal for a DDL action (loaded via HTMX).
func (s *Server) pgDDLForm(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	if !u.Write {
		http.Error(w, "write access required", http.StatusForbidden)
		return
	}
	c, err := s.connFor(r)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	f := ddlForm{Conn: c, CSRF: u.CSRF, Kind: q.Get("kind"), Schema: q.Get("schema"), Table: q.Get("table"), Column: q.Get("column"), Nullable: true}
	if f.Kind == "modify-column" {
		// Prefill from the live column definition.
		if pool, e := s.reg.PG(r.Context(), c); e == nil {
			if cols, e2 := postgres.Columns(r.Context(), pool, f.Schema, f.Table); e2 == nil {
				for _, col := range cols {
					if col.Name == f.Column {
						f.Name, f.Type, f.Nullable, f.Default = col.Name, col.Type, !col.NotNull, col.Default
					}
				}
			}
		}
	}
	s.rnd.partial(w, "ddlForm", f)
}

// pgRunForm handles every form-backed DDL POST. On failure it re-renders the
// modal with the entered values and an error so the user can correct them.
func (s *Server) pgRunForm(w http.ResponseWriter, r *http.Request) {
	u, c, pool, ok := s.requireWrite(w, r, false)
	if !ok {
		return
	}
	f := ddlForm{
		Conn: c, CSRF: u.CSRF,
		Kind:    r.FormValue("kind"),
		Schema:  r.FormValue("schema"),
		Table:   r.FormValue("table"),
		Column:  r.FormValue("column"),
		Name:    strings.TrimSpace(r.FormValue("name")),
		Type:    strings.TrimSpace(r.FormValue("type")),
		Default: strings.TrimSpace(r.FormValue("default")),
		Columns: strings.TrimSpace(r.FormValue("columns")),
		Nullable: r.FormValue("nullable") == "on",
		Unique:   r.FormValue("unique") == "on",
	}

	sql, action, err := buildFormSQL(f)
	if err == nil {
		err = s.execDDL(w, r, u, c, pool, action, sql)
	}
	if err != nil {
		f.Err = err.Error()
		s.rnd.partial(w, "ddlForm", f) // 200 so HTMX swaps the form back in
		return
	}
	// Success: empty body clears #ddl-host (closes the modal); HX-Trigger refreshes.
	w.WriteHeader(http.StatusOK)
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
		// Build CREATE ROLE … WITH <options>. A role that can log in is what
		// Postgres calls a "user"; the rest are optional privilege flags.
		opts := []string{}
		if f.Login {
			opts = append(opts, "LOGIN")
		} else {
			opts = append(opts, "NOLOGIN")
		}
		if f.Superuser {
			opts = append(opts, "SUPERUSER")
		}
		if f.CreateDB {
			opts = append(opts, "CREATEDB")
		}
		if f.CreateRole {
			opts = append(opts, "CREATEROLE")
		}
		if f.Password != "" {
			opts = append(opts, "PASSWORD "+postgres.QuoteLiteral(f.Password))
		}
		return fmt.Sprintf("CREATE ROLE %s WITH %s", postgres.QuoteIdent(f.Name), strings.Join(opts, " ")), "pg_ddl_create_role", nil
	}
	return "", "", fmt.Errorf("unknown form kind %q", f.Kind)
}
