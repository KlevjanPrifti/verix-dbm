package web

// SQL engine endpoints: the explorer tree, introspection (columns/keys/indexes),
// browse + query + transaction, code generators, doc/usages, and form-driven
// DDL. They dispatch through s.apiSQL / s.reg.Engine, so Postgres, MySQL, and
// SQLite share them. Split out of api.go; mounted under /api.

import (
	"net/http"
	"strconv"
	"strings"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/dbsql"
	"verix-dbm/internal/mongodb"
	"verix-dbm/internal/store"
)

type resultDTO struct {
	Columns      []string   `json:"columns"`
	Rows         [][]string `json:"rows"`
	IsSelect     bool       `json:"isSelect"`
	RowsAffected int64      `json:"rowsAffected"`
	Command      string     `json:"command"`
	Duration     string     `json:"duration"`
	Truncated    bool       `json:"truncated"`
}

func toResultDTO(r *dbsql.Result) *resultDTO {
	if r == nil {
		return nil
	}
	return &resultDTO{
		Columns: r.Columns, Rows: r.Rows, IsSelect: r.IsSelect,
		RowsAffected: r.RowsAffected, Command: r.Command,
		Duration: r.Duration.String(), Truncated: r.Truncated,
	}
}

type columnDTO struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	TypeText string `json:"typeText"`
	Cat      string `json:"cat"`
	NotNull  bool   `json:"notNull"`
	Default  string `json:"default"`
	PK       bool   `json:"pk"`
	AutoInc  bool   `json:"autoInc"`
}

func toColumnDTO(c dbsql.Column) columnDTO {
	return columnDTO{
		Name: c.Name, Type: c.Type, TypeText: c.TypeText(), Cat: c.Cat(),
		NotNull: c.NotNull, Default: c.Default, PK: c.PK, AutoInc: c.AutoInc,
	}
}

// Explorer tree

func (s *Server) apiExplorer(w http.ResponseWriter, r *http.Request) {
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return
	}
	engine := c.Engine()
	if engine == dbsql.FamilyRedis {
		writeJSON(w, http.StatusOK, map[string]any{"kind": "redis"})
		return
	}
	if engine == dbsql.FamilyMongo {
		client, err := s.reg.Mongo(r.Context(), c)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"kind": "mongodb", "error": "connect: " + err.Error()})
			return
		}
		dbs, err := mongodb.Databases(r.Context(), client)
		resp := map[string]any{"kind": "mongodb", "databases": dbs}
		if err != nil {
			resp["error"] = err.Error()
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	eng, err := s.reg.Engine(r.Context(), c)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"kind": engine, "error": "connect: " + err.Error()})
		return
	}
	schemas, err := eng.Schemas(r.Context())
	resp := map[string]any{"kind": engine, "schemas": schemas}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) apiColumns(w http.ResponseWriter, r *http.Request) {
	_, eng, ok := s.apiSQL(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	cols, err := eng.Columns(r.Context(), q.Get("schema"), q.Get("table"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out := make([]columnDTO, 0, len(cols))
	for _, c := range cols {
		out = append(out, toColumnDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"columns": out})
}

func (s *Server) apiIndexes(w http.ResponseWriter, r *http.Request) {
	_, eng, ok := s.apiSQL(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	ix, err := eng.Indexes(r.Context(), q.Get("schema"), q.Get("table"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"indexes": ix})
}

func (s *Server) apiKeys(w http.ResponseWriter, r *http.Request) {
	_, eng, ok := s.apiSQL(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	keys, err := eng.Keys(r.Context(), q.Get("schema"), q.Get("table"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

// Data grid

func (s *Server) apiGrid(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	c, eng, ok := s.apiSQL(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	schema, table := q.Get("schema"), q.Get("table")
	where, order := q.Get("where"), q.Get("order")
	if serverSideBlocked(u, c.Kind, where, order) {
		apiErr(w, http.StatusForbidden, serverSideBlockedMsg)
		return
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 0 {
		page = 0
	}
	// Page size is client-selectable; fall back to the default
	// and clamp to the 1000-row result cap BrowseWhere/Query enforce.
	size, _ := strconv.Atoi(q.Get("size"))
	if size <= 0 || size > 1000 {
		size = browseLimit
	}
	res, err := eng.BrowseWhere(r.Context(), schema, table, where, order, size, page*size, true)
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: "sql_browse", Detail: schema + "." + table, Success: err == nil})
	resp := map[string]any{
		"result":   toResultDTO(res),
		"readOnly": c.ReadOnly || !s.access(r.Context(), u, c).Write,
		"page":     page,
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// SQL query console

func (s *Server) apiQuery(w http.ResponseWriter, r *http.Request) {
	if !s.auth.CheckCSRF(r) {
		apiErr(w, http.StatusForbidden, "bad csrf")
		return
	}
	u, _ := auth.FromContext(r.Context())
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return
	}
	var in struct {
		SQL     string `json:"sql"`
		Confirm bool   `json:"confirm"`
		Schema  string `json:"schema"`
	}
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	sql := strings.TrimSpace(in.SQL)
	readOnly := c.ReadOnly || !s.access(r.Context(), u, c).Write
	resp := map[string]any{"readOnly": readOnly}
	if sql == "" {
		resp["error"] = "empty statement"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if serverSideBlocked(u, c.Kind, sql) {
		resp["error"] = serverSideBlockedMsg
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if !readOnly && dbsql.NeedsConfirm(sql) && !in.Confirm {
		resp["needConfirm"] = true
		resp["sql"] = sql
		writeJSON(w, http.StatusOK, resp)
		return
	}
	eng, err := s.reg.Engine(r.Context(), c)
	if err != nil {
		resp["error"] = "connect: " + err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	res, err := eng.Query(r.Context(), sql, readOnly, in.Schema)
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: "sql_query", Detail: auditDetail(sql), Success: err == nil})
	resp["result"] = toResultDTO(res)
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// apiExecTx runs a batch of write statements as one atomic transaction. It backs
// the grid's "Tx: Manual" mode, where row inserts/edits/deletes are queued in the
// browser and committed together so they all land or all roll back. Same guards
// as apiQuery: CSRF, write + read-only gating, the server-side program/file
// block, and a destructive-statement confirm gate applied across the whole batch.
func (s *Server) apiExecTx(w http.ResponseWriter, r *http.Request) {
	u, c, eng, ok := s.apiRequireWrite(w, r, false)
	if !ok {
		return
	}
	var in struct {
		Statements []string `json:"statements"`
		Confirm    bool     `json:"confirm"`
	}
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	stmts := make([]string, 0, len(in.Statements))
	for _, st := range in.Statements {
		if st = strings.TrimSpace(st); st != "" {
			stmts = append(stmts, st)
		}
	}
	if len(stmts) == 0 {
		apiErr(w, http.StatusBadRequest, "no statements to commit")
		return
	}
	if serverSideBlocked(u, c.Kind, stmts...) {
		apiErr(w, http.StatusForbidden, serverSideBlockedMsg)
		return
	}
	if !in.Confirm {
		for _, st := range stmts {
			if dbsql.NeedsConfirm(st) {
				writeJSON(w, http.StatusOK, map[string]any{"needConfirm": true})
				return
			}
		}
	}
	err := eng.ExecScript(r.Context(), stmts)
	s.st.AddAudit(r.Context(), store.Audit{
		User: u.Email, ConnID: c.ID, Action: "sql_tx",
		Detail: auditDetail(strings.Join(stmts, "; ")), Success: err == nil,
	})
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(stmts)})
}

// Generators / introspection

func (s *Server) apiGenerate(w http.ResponseWriter, r *http.Request) {
	_, eng, ok := s.apiSQL(w, r)
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
		sql, err = eng.GenSelect(r.Context(), schema, table)
	case "insert":
		sql, err = eng.GenInsert(r.Context(), schema, table)
	case "update":
		sql, err = eng.GenUpdate(r.Context(), schema, table)
	case "create":
		sql, err = eng.CreateTableDDL(r.Context(), schema, table)
	default:
		apiErr(w, http.StatusBadRequest, "unknown kind")
		return
	}
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sql": sql})
}

func (s *Server) apiDoc(w http.ResponseWriter, r *http.Request) {
	_, eng, ok := s.apiSQL(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	schema, table := q.Get("schema"), q.Get("table")
	cols, err := eng.Columns(r.Context(), schema, table)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	keys, _ := eng.Keys(r.Context(), schema, table)
	indexes, _ := eng.Indexes(r.Context(), schema, table)
	comment, _ := eng.TableComment(r.Context(), schema, table)
	out := make([]columnDTO, 0, len(cols))
	for _, c := range cols {
		out = append(out, toColumnDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema": schema, "table": table, "columns": out,
		"keys": keys, "indexes": indexes, "comment": comment,
	})
}

func (s *Server) apiUsages(w http.ResponseWriter, r *http.Request) {
	_, eng, ok := s.apiSQL(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	schema, table := q.Get("schema"), q.Get("table")
	usages, err := eng.FindUsages(r.Context(), schema, table)
	resp := map[string]any{"schema": schema, "table": table, "usages": usages}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// DDL

// apiDDLFormPrefill returns the live column definition so the SPA can prefill
// the Modify-column modal (other DDL forms need no server-side prefill).
func (s *Server) apiDDLFormPrefill(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return
	}
	if !s.access(r.Context(), u, c).Write {
		apiErr(w, http.StatusForbidden, "write access required")
		return
	}
	q := r.URL.Query()
	resp := map[string]any{"nullable": true}
	if q.Get("kind") == "modify-column" {
		if eng, e := s.reg.Engine(r.Context(), c); e == nil {
			if cols, e2 := eng.Columns(r.Context(), q.Get("schema"), q.Get("table")); e2 == nil {
				for _, col := range cols {
					if col.Name == q.Get("column") {
						resp["name"] = col.Name
						resp["type"] = col.Type
						resp["nullable"] = !col.NotNull
						resp["default"] = col.Default
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) apiRunForm(w http.ResponseWriter, r *http.Request) {
	u, c, eng, ok := s.apiRequireWrite(w, r, false)
	if !ok {
		return
	}
	var in struct {
		Kind       string `json:"kind"`
		Schema     string `json:"schema"`
		Table      string `json:"table"`
		Column     string `json:"column"`
		Name       string `json:"name"`
		Type       string `json:"type"`
		Default    string `json:"default"`
		Columns    string `json:"columns"`
		Nullable   bool   `json:"nullable"`
		Unique     bool   `json:"unique"`
		Owner      string `json:"owner"`
		Password   string `json:"password"`
		Login      bool   `json:"login"`
		CreateDB   bool   `json:"createdb"`
		CreateRole bool   `json:"createrole"`
		Superuser  bool   `json:"superuser"`
		Host       string `json:"host"` // mysql user host part (default %)
	}
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	// Role/user management is cluster-wide and reserved for admins.
	if in.Kind == "create-user" && !u.Admin {
		apiErr(w, http.StatusForbidden, "admin required")
		return
	}
	spec := dbsql.FormSpec{
		Kind: in.Kind, Schema: in.Schema, Table: in.Table, Column: in.Column,
		Name: strings.TrimSpace(in.Name), Type: strings.TrimSpace(in.Type),
		Default: strings.TrimSpace(in.Default), Columns: strings.TrimSpace(in.Columns),
		Nullable: in.Nullable, Unique: in.Unique, Owner: strings.TrimSpace(in.Owner),
		Role: dbsql.RoleAttrs{
			Login: in.Login, Super: in.Superuser, CreateDB: in.CreateDB, CreateRole: in.CreateRole,
			Password: in.Password, Host: strings.TrimSpace(in.Host),
		},
	}
	stmts, action, err := eng.FormSQL(spec)
	if err == nil {
		// A single-statement form runs as one audited Exec; multi-statement forms
		// (e.g. MySQL CREATE USER + GRANT) run as one audited statement list.
		if len(stmts) == 1 {
			err = s.execDDLAudit(r, u, c, eng, action, stmts[0])
		} else {
			err = s.execScriptAudit(r, u, c, eng, action, stmts)
		}
	}
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// apiApplyTable executes the statement list a table-designer "create"/"modify"
// produced, atomically (see postgres.ExecScript). The SQL is built and previewed
// client-side, so this mirrors the query console's trust model any write user
// can already run arbitrary DDL there while adding transactional safety and a
// single audit entry for the whole edit.
func (s *Server) apiApplyTable(w http.ResponseWriter, r *http.Request) {
	u, c, eng, ok := s.apiRequireWrite(w, r, false)
	if !ok {
		return
	}
	var in struct {
		Action     string   `json:"action"`
		Statements []string `json:"statements"`
	}
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if len(in.Statements) == 0 {
		apiErr(w, http.StatusBadRequest, "no statements to run")
		return
	}
	action := in.Action
	if action == "" {
		action = "sql_ddl_table"
	}
	err := eng.ExecScript(r.Context(), in.Statements)
	s.st.AddAudit(r.Context(), store.Audit{
		User: u.Email, ConnID: c.ID, Action: action,
		Detail: auditDetail(strings.Join(in.Statements, "; ")), Success: err == nil,
	})
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiDropTable(w http.ResponseWriter, r *http.Request) {
	s.apiDDLMutate(w, r, true, "sql_ddl_drop_table", func(d dbsql.Dialect, in ddlBody) string {
		return d.DropTableSQL(in.Schema, in.Table)
	})
}

func (s *Server) apiTruncate(w http.ResponseWriter, r *http.Request) {
	s.apiDDLMutate(w, r, true, "sql_ddl_truncate", func(d dbsql.Dialect, in ddlBody) string {
		return d.TruncateSQL(in.Schema, in.Table)
	})
}

func (s *Server) apiDropColumn(w http.ResponseWriter, r *http.Request) {
	s.apiDDLMutate(w, r, true, "sql_ddl_drop_column", func(d dbsql.Dialect, in ddlBody) string {
		return d.DropColumnSQL(in.Schema, in.Table, in.Column)
	})
}

func (s *Server) apiDropIndex(w http.ResponseWriter, r *http.Request) {
	s.apiDDLMutate(w, r, true, "sql_ddl_drop_index", func(d dbsql.Dialect, in ddlBody) string {
		return d.DropIndexSQL(in.Schema, in.Table, in.Name)
	})
}

type ddlBody struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Column string `json:"column"`
	Name   string `json:"name"`
}

// apiDDLMutate is the JSON twin of the fetch-based confirm endpoints: gate on
// CSRF + write/admin + read-only, build the statement, exec, audit.
func (s *Server) apiDDLMutate(w http.ResponseWriter, r *http.Request, admin bool, action string, build func(dbsql.Dialect, ddlBody) string) {
	u, c, eng, ok := s.apiRequireWrite(w, r, admin)
	if !ok {
		return
	}
	var in ddlBody
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.execDDLAudit(r, u, c, eng, action, build(eng, in)); err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// execDDLAudit runs a generated statement and records it (no HX-Trigger header:
// the SPA refreshes its own tree on success).
func (s *Server) execDDLAudit(r *http.Request, u auth.User, c store.Connection, eng dbsql.Engine, action, sql string) error {
	_, err := eng.Exec(r.Context(), sql)
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: action, Detail: auditDetail(sql), Success: err == nil})
	return err
}

// execScriptAudit runs a statement list as one transaction (Engine.ExecScript)
// and records the whole list as one audit entry the twin of execDDLAudit for the
// rename/owner/privilege edits that compile to more than one statement. NOTE: on
// MySQL/MariaDB a mid-batch DDL failure is not rolled back (Engine.AtomicDDL is
// false there); the returned error names the statement that failed.
func (s *Server) execScriptAudit(r *http.Request, u auth.User, c store.Connection, eng dbsql.Engine, action string, stmts []string) error {
	err := eng.ExecScript(r.Context(), stmts)
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: action, Detail: auditDetail(strings.Join(stmts, "; ")), Success: err == nil})
	return err
}

// Schemas: drop / alter. Dropping a schema can take its tables with it, so it is
// admin-gated like drop-table; renaming/reassigning owner is a write op.

func (s *Server) apiDropSchema(w http.ResponseWriter, r *http.Request) {
	u, c, eng, ok := s.apiRequireWrite(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Schema  string `json:"schema"`
		Cascade bool   `json:"cascade"`
	}
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if strings.TrimSpace(in.Schema) == "" {
		apiErr(w, http.StatusBadRequest, "schema is required")
		return
	}
	if err := s.execScriptAudit(r, u, c, eng, "sql_ddl_drop_schema", eng.DropSchemaSQL(in.Schema, in.Cascade)); err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiAlterSchema(w http.ResponseWriter, r *http.Request) {
	u, c, eng, ok := s.apiRequireWrite(w, r, false)
	if !ok {
		return
	}
	var in struct {
		Schema  string `json:"schema"`
		NewName string `json:"newName"`
		Owner   string `json:"owner"`
	}
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	stmts := eng.AlterSchemaSQL(strings.TrimSpace(in.Schema), strings.TrimSpace(in.NewName), strings.TrimSpace(in.Owner))
	if len(stmts) == 0 {
		apiErr(w, http.StatusBadRequest, "nothing to change")
		return
	}
	if err := s.execScriptAudit(r, u, c, eng, "sql_ddl_alter_schema", stmts); err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Roles: list / drop / alter. Roles are cluster-wide, so every endpoint here is
// admin-gated (listing too it exposes the cluster's accounts).

func (s *Server) apiRoles(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	if !u.Admin {
		apiErr(w, http.StatusForbidden, "admin required")
		return
	}
	_, eng, ok := s.apiSQL(w, r)
	if !ok {
		return
	}
	roles, err := eng.Roles(r.Context())
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": roles})
}

func (s *Server) apiDropRole(w http.ResponseWriter, r *http.Request) {
	u, c, eng, ok := s.apiRequireWrite(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
		Host string `json:"host"` // mysql user host part (ignored by Postgres)
	}
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		apiErr(w, http.StatusBadRequest, "role name is required")
		return
	}
	if err := s.execScriptAudit(r, u, c, eng, "sql_ddl_drop_role", eng.DropUserSQL(strings.TrimSpace(in.Name), strings.TrimSpace(in.Host))); err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiAlterRole(w http.ResponseWriter, r *http.Request) {
	u, c, eng, ok := s.apiRequireWrite(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Name       string `json:"name"`
		NewName    string `json:"newName"`
		Password   string `json:"password"`
		Login      bool   `json:"login"`
		CreateDB   bool   `json:"createdb"`
		CreateRole bool   `json:"createrole"`
		Superuser  bool   `json:"superuser"`
		Host       string `json:"host"` // mysql user host part (ignored by Postgres)
	}
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		apiErr(w, http.StatusBadRequest, "role name is required")
		return
	}
	stmts := eng.AlterUserSQL(strings.TrimSpace(in.Name), strings.TrimSpace(in.NewName), dbsql.RoleAttrs{
		Login: in.Login, Super: in.Superuser, CreateDB: in.CreateDB, CreateRole: in.CreateRole,
		Password: in.Password, Host: strings.TrimSpace(in.Host),
	})
	if len(stmts) == 0 {
		apiErr(w, http.StatusBadRequest, "nothing to change")
		return
	}
	if err := s.execScriptAudit(r, u, c, eng, "sql_ddl_alter_role", stmts); err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
