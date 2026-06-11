package web

// JSON API consumed by the React/Vite SPA (internal/web/spa). It mirrors the
// HTMX handlers but speaks JSON: same auth middleware, same CSRF (X-CSRF-Token
// header), same role gating. Mounted under /api by Router().

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/postgres"
	"verix-dbm/internal/redisdb"
	"verix-dbm/internal/store"
)

// mountAPI registers the JSON routes. It runs inside the authed group, so every
// handler can assume auth.FromContext succeeds.
func (s *Server) mountAPI(r chi.Router) {
	r.Get("/me", s.apiMe)

	r.Get("/connections", s.apiListConnections)
	r.Post("/connections", s.apiCreateConnection)
	r.Post("/connections/test", s.apiTestConnection)
	r.Get("/connections/{id}", s.apiGetConnection)
	r.Put("/connections/{id}", s.apiUpdateConnection)
	r.Delete("/connections/{id}", s.apiDeleteConnection)

	// Per-connection access grants (admin only). Effective only when
	// DBM_SCOPED_ACCESS is on; manageable regardless so access can be set up first.
	r.Get("/connections/{id}/grants", s.apiListGrants)
	r.Put("/connections/{id}/grants", s.apiSetGrant)
	r.Delete("/connections/{id}/grants/{gid}", s.apiDeleteGrant)

	r.Get("/c/{id}/explorer", s.apiExplorer)
	r.Get("/c/{id}/pg/columns", s.apiColumns)
	r.Get("/c/{id}/pg/indexes", s.apiIndexes)
	r.Get("/c/{id}/pg/keys", s.apiKeys)
	r.Get("/c/{id}/grid", s.apiGrid)
	r.Post("/c/{id}/pg/query", s.apiQuery)
	r.Post("/c/{id}/pg/tx", s.apiExecTx)
	r.Get("/c/{id}/pg/generate", s.apiGenerate)
	r.Get("/c/{id}/pg/doc", s.apiDoc)
	r.Get("/c/{id}/pg/usages", s.apiUsages)
	r.Get("/c/{id}/pg/form", s.apiDDLFormPrefill)
	r.Post("/c/{id}/pg/ddl/run", s.apiRunForm)
	r.Post("/c/{id}/pg/table/apply", s.apiApplyTable)
	r.Post("/c/{id}/pg/table/drop", s.apiDropTable)
	r.Post("/c/{id}/pg/table/truncate", s.apiTruncate)
	r.Post("/c/{id}/pg/column/drop", s.apiDropColumn)
	r.Post("/c/{id}/pg/index/drop", s.apiDropIndex)
	r.Post("/c/{id}/pg/schema/drop", s.apiDropSchema)
	r.Post("/c/{id}/pg/schema/alter", s.apiAlterSchema)
	r.Get("/c/{id}/pg/roles", s.apiRoles)
	r.Post("/c/{id}/pg/role/drop", s.apiDropRole)
	r.Post("/c/{id}/pg/role/alter", s.apiAlterRole)

	r.Get("/c/{id}/redis/keys", s.apiRedisKeys)
	r.Get("/c/{id}/redis/value", s.apiRedisValue)
	r.Post("/c/{id}/redis/cmd", s.apiRedisCmd)

	r.Get("/audit", s.apiAudit)
	r.Get("/audit/export", s.apiAuditExport)

	// Key rotation: re-encrypt every stored credential under the current primary
	// key (admin only).
	r.Post("/admin/reencrypt", s.apiReencrypt)
}

// JSON plumbing

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

// DTOs

type connDTO struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	DBName   string `json:"dbname"`
	Username string `json:"username"`
	Options  string `json:"options"`
	ReadOnly bool   `json:"readOnly"`
}

func toConnDTO(c store.Connection) connDTO {
	return connDTO{
		ID: c.ID, Name: c.Name, Kind: c.Kind, Host: c.Host, Port: c.Port,
		DBName: c.DBName, Username: c.Username, Options: c.Options, ReadOnly: c.ReadOnly,
	}
}

type connInput struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	DBName   string `json:"dbname"`
	Username string `json:"username"`
	Password string `json:"password"`
	// CopyFrom is the source connection id for "Save as copy": the server reuses
	// that connection's stored ciphertext, so the secret never reaches the client.
	CopyFrom int64  `json:"copyFrom"`
	Options  string `json:"options"`
	ReadOnly bool   `json:"readOnly"`
}

type resultDTO struct {
	Columns      []string   `json:"columns"`
	Rows         [][]string `json:"rows"`
	IsSelect     bool       `json:"isSelect"`
	RowsAffected int64      `json:"rowsAffected"`
	Command      string     `json:"command"`
	Duration     string     `json:"duration"`
	Truncated    bool       `json:"truncated"`
}

func toResultDTO(r *postgres.Result) *resultDTO {
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

func toColumnDTO(c postgres.Column) columnDTO {
	return columnDTO{
		Name: c.Name, Type: c.Type, TypeText: c.TypeText(), Cat: c.Cat(),
		NotNull: c.NotNull, Default: c.Default, PK: c.PK, AutoInc: c.AutoInc,
	}
}

// Session

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"name": u.Name, "email": u.Email, "admin": u.Admin, "write": u.Write,
		},
		"csrf":         u.CSRF,
		"scopedAccess": s.cfg.ScopedAccess,
	})
}

// Connections

func (s *Server) apiListConnections(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	var conns []store.Connection
	var err error
	if s.cfg.ScopedAccess && !u.Admin {
		// Scoped mode: a non-admin sees only connections granted to one of their
		// groups/roles.
		conns, err = s.st.ListConnectionsForSubjects(r.Context(), u.Subjects())
	} else {
		conns, err = s.st.ListConnections(r.Context())
	}
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]connDTO, 0, len(conns))
	for _, c := range conns {
		out = append(out, toConnDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

func (s *Server) apiGetConnection(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	if !u.Admin {
		apiErr(w, http.StatusForbidden, "admin required")
		return
	}
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return
	}
	// The stored password ciphertext is intentionally NOT returned the browser
	// never needs it (duplication carries it server-side via copyFrom).
	writeJSON(w, http.StatusOK, map[string]any{"connection": toConnDTO(c)})
}

func (s *Server) apiCreateConnection(w http.ResponseWriter, r *http.Request) {
	u, ok := s.apiRequireAdmin(w, r)
	if !ok {
		return
	}
	var in connInput
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	c := store.Connection{
		Name: in.Name, Kind: in.Kind, Host: in.Host, Port: in.Port,
		DBName: in.DBName, Username: in.Username, Options: in.Options,
		ReadOnly: in.ReadOnly, CreatedBy: u.Email,
	}
	if in.Password != "" {
		enc, err := s.box.Encrypt(in.Password)
		if err != nil {
			apiErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		c.PasswordEnc = enc
	} else if in.CopyFrom > 0 {
		// "Save as copy": carry the source connection's ciphertext server-side so
		// the plaintext/ciphertext never round-trips through the browser.
		if src, err := s.st.GetConnection(r.Context(), in.CopyFrom); err == nil {
			c.PasswordEnc = src.PasswordEnc
		}
	}
	id, err := s.st.CreateConnection(r.Context(), c)
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: id, Action: "create_connection", Detail: c.Name, Success: true})
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) apiUpdateConnection(w http.ResponseWriter, r *http.Request) {
	u, ok := s.apiRequireAdmin(w, r)
	if !ok {
		return
	}
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return
	}
	var in connInput
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	c.Name, c.Kind, c.Host, c.Port = in.Name, in.Kind, in.Host, in.Port
	c.DBName, c.Username, c.Options, c.ReadOnly = in.DBName, in.Username, in.Options, in.ReadOnly
	updatePw := false
	if in.Password != "" {
		enc, err := s.box.Encrypt(in.Password)
		if err != nil {
			apiErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		c.PasswordEnc = enc
		updatePw = true
	}
	if err := s.st.UpdateConnection(r.Context(), c, updatePw); err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reg.Forget(c.ID)
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: "update_connection", Detail: c.Name, Success: true})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiDeleteConnection(w http.ResponseWriter, r *http.Request) {
	u, ok := s.apiRequireAdmin(w, r)
	if !ok {
		return
	}
	id := idParam(r)
	if err := s.st.DeleteConnection(r.Context(), id); err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reg.Forget(id)
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: id, Action: "delete_connection", Success: true})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Connection access grants

type grantDTO struct {
	ID      int64  `json:"id"`
	Subject string `json:"subject"`
	Level   string `json:"level"`
}

// apiListGrants returns the grants on a connection (admin only). Read-only, so
// no CSRF gate; the admin capability is the control.
func (s *Server) apiListGrants(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	if !u.Admin {
		apiErr(w, http.StatusForbidden, "admin required")
		return
	}
	grants, err := s.st.ListGrants(r.Context(), idParam(r))
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]grantDTO, 0, len(grants))
	for _, g := range grants {
		out = append(out, grantDTO{ID: g.ID, Subject: g.Subject, Level: g.Level})
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": out})
}

// apiSetGrant upserts one (subject, level) grant on a connection (admin only).
func (s *Server) apiSetGrant(w http.ResponseWriter, r *http.Request) {
	u, ok := s.apiRequireAdmin(w, r)
	if !ok {
		return
	}
	connID := idParam(r)
	if _, err := s.st.GetConnection(r.Context(), connID); err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return
	}
	var in struct {
		Subject string `json:"subject"`
		Level   string `json:"level"`
	}
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	in.Subject = strings.TrimSpace(in.Subject)
	if in.Subject == "" {
		apiErr(w, http.StatusBadRequest, "subject required")
		return
	}
	if !store.ValidGrantLevel(in.Level) {
		apiErr(w, http.StatusBadRequest, "level must be 'read' or 'write'")
		return
	}
	if err := s.st.SetGrant(r.Context(), store.Grant{ConnID: connID, Subject: in.Subject, Level: in.Level, CreatedBy: u.Email}); err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: connID, Action: "grant_set", Detail: in.Subject + "=" + in.Level, Success: true})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// apiDeleteGrant removes a grant by id, scoped to its connection (admin only).
func (s *Server) apiDeleteGrant(w http.ResponseWriter, r *http.Request) {
	u, ok := s.apiRequireAdmin(w, r)
	if !ok {
		return
	}
	connID := idParam(r)
	gid, _ := strconv.ParseInt(chi.URLParam(r, "gid"), 10, 64)
	if err := s.st.DeleteGrant(r.Context(), connID, gid); err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: connID, Action: "grant_delete", Detail: strconv.FormatInt(gid, 10), Success: true})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiTestConnection(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.apiRequireAdmin(w, r); !ok {
		return
	}
	var in connInput
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	c := store.Connection{Kind: in.Kind, Host: in.Host, Port: in.Port, DBName: in.DBName, Username: in.Username, Options: in.Options}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	var err error
	if c.Kind == "redis" {
		err = pingRedis(ctx, c, in.Password)
	} else {
		err = pingPG(ctx, c, in.Password)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Explorer tree

func (s *Server) apiExplorer(w http.ResponseWriter, r *http.Request) {
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return
	}
	if c.Kind == "redis" {
		writeJSON(w, http.StatusOK, map[string]any{"kind": "redis"})
		return
	}
	pool, err := s.reg.PG(r.Context(), c)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"kind": "postgres", "error": "connect: " + err.Error()})
		return
	}
	schemas, err := postgres.Schemas(r.Context(), pool)
	resp := map[string]any{"kind": "postgres", "schemas": schemas}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) apiColumns(w http.ResponseWriter, r *http.Request) {
	_, pool, ok := s.apiPGPool(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	cols, err := postgres.Columns(r.Context(), pool, q.Get("schema"), q.Get("table"))
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
	_, pool, ok := s.apiPGPool(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	ix, err := postgres.Indexes(r.Context(), pool, q.Get("schema"), q.Get("table"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"indexes": ix})
}

func (s *Server) apiKeys(w http.ResponseWriter, r *http.Request) {
	_, pool, ok := s.apiPGPool(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	keys, err := postgres.Keys(r.Context(), pool, q.Get("schema"), q.Get("table"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

// Data grid

func (s *Server) apiGrid(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	c, pool, ok := s.apiPGPool(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	schema, table := q.Get("schema"), q.Get("table")
	where, order := q.Get("where"), q.Get("order")
	if serverSideBlocked(u, where, order) {
		apiErr(w, http.StatusForbidden, serverSideBlockedMsg)
		return
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 0 {
		page = 0
	}
	res, err := postgres.BrowseWhere(r.Context(), pool, schema, table, where, order, browseLimit, page*browseLimit, true)
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: "pg_browse", Detail: schema + "." + table, Success: err == nil})
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
	if serverSideBlocked(u, sql) {
		resp["error"] = serverSideBlockedMsg
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if !readOnly && postgres.NeedsConfirm(sql) && !in.Confirm {
		resp["needConfirm"] = true
		resp["sql"] = sql
		writeJSON(w, http.StatusOK, resp)
		return
	}
	pool, err := s.reg.PG(r.Context(), c)
	if err != nil {
		resp["error"] = "connect: " + err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	res, err := postgres.Query(r.Context(), pool, sql, readOnly)
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: "pg_query", Detail: auditDetail(sql), Success: err == nil})
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
	u, c, pool, ok := s.apiRequireWrite(w, r, false)
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
	if serverSideBlocked(u, stmts...) {
		apiErr(w, http.StatusForbidden, serverSideBlockedMsg)
		return
	}
	if !in.Confirm {
		for _, st := range stmts {
			if postgres.NeedsConfirm(st) {
				writeJSON(w, http.StatusOK, map[string]any{"needConfirm": true})
				return
			}
		}
	}
	err := postgres.ExecScript(r.Context(), pool, stmts)
	s.st.AddAudit(r.Context(), store.Audit{
		User: u.Email, ConnID: c.ID, Action: "pg_tx",
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
	_, pool, ok := s.apiPGPool(w, r)
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
	_, pool, ok := s.apiPGPool(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	schema, table := q.Get("schema"), q.Get("table")
	cols, err := postgres.Columns(r.Context(), pool, schema, table)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	keys, _ := postgres.Keys(r.Context(), pool, schema, table)
	indexes, _ := postgres.Indexes(r.Context(), pool, schema, table)
	comment, _ := postgres.TableComment(r.Context(), pool, schema, table)
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
	_, pool, ok := s.apiPGPool(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	schema, table := q.Get("schema"), q.Get("table")
	usages, err := postgres.FindUsages(r.Context(), pool, schema, table)
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
		if pool, e := s.reg.PG(r.Context(), c); e == nil {
			if cols, e2 := postgres.Columns(r.Context(), pool, q.Get("schema"), q.Get("table")); e2 == nil {
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
	u, c, pool, ok := s.apiRequireWrite(w, r, false)
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
	f := ddlForm{
		Conn: c, Kind: in.Kind, Schema: in.Schema, Table: in.Table, Column: in.Column,
		Name: strings.TrimSpace(in.Name), Type: strings.TrimSpace(in.Type),
		Default: strings.TrimSpace(in.Default), Columns: strings.TrimSpace(in.Columns),
		Nullable: in.Nullable, Unique: in.Unique,
		Owner:    strings.TrimSpace(in.Owner),
		Password: in.Password, Login: in.Login, CreateDB: in.CreateDB, CreateRole: in.CreateRole, Superuser: in.Superuser,
	}
	sql, action, err := buildFormSQL(f)
	if err == nil {
		err = s.execDDLAudit(r, u, c, pool, action, sql)
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
	u, c, pool, ok := s.apiRequireWrite(w, r, false)
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
		action = "pg_ddl_table"
	}
	err := postgres.ExecScript(r.Context(), pool, in.Statements)
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
	s.apiDDLMutate(w, r, true, "pg_ddl_drop_table", func(in ddlBody) string {
		return "DROP TABLE " + postgres.Qualified(in.Schema, in.Table)
	})
}

func (s *Server) apiTruncate(w http.ResponseWriter, r *http.Request) {
	s.apiDDLMutate(w, r, true, "pg_ddl_truncate", func(in ddlBody) string {
		return "TRUNCATE TABLE " + postgres.Qualified(in.Schema, in.Table)
	})
}

func (s *Server) apiDropColumn(w http.ResponseWriter, r *http.Request) {
	s.apiDDLMutate(w, r, true, "pg_ddl_drop_column", func(in ddlBody) string {
		return "ALTER TABLE " + postgres.Qualified(in.Schema, in.Table) + " DROP COLUMN " + postgres.QuoteIdent(in.Column)
	})
}

func (s *Server) apiDropIndex(w http.ResponseWriter, r *http.Request) {
	s.apiDDLMutate(w, r, true, "pg_ddl_drop_index", func(in ddlBody) string {
		return "DROP INDEX " + postgres.Qualified(in.Schema, in.Name)
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
func (s *Server) apiDDLMutate(w http.ResponseWriter, r *http.Request, admin bool, action string, build func(ddlBody) string) {
	u, c, pool, ok := s.apiRequireWrite(w, r, admin)
	if !ok {
		return
	}
	var in ddlBody
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.execDDLAudit(r, u, c, pool, action, build(in)); err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// execDDLAudit runs a generated statement and records it (no HX-Trigger header:
// the SPA refreshes its own tree on success).
func (s *Server) execDDLAudit(r *http.Request, u auth.User, c store.Connection, pool *pgxpool.Pool, action, sql string) error {
	_, err := postgres.Exec(r.Context(), pool, sql)
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: action, Detail: auditDetail(sql), Success: err == nil})
	return err
}

// execScriptAudit runs a statement list atomically (postgres.ExecScript) and
// records the whole list as one audit entry the twin of execDDLAudit for the
// rename/owner/privilege edits that compile to more than one statement.
func (s *Server) execScriptAudit(r *http.Request, u auth.User, c store.Connection, pool *pgxpool.Pool, action string, stmts []string) error {
	err := postgres.ExecScript(r.Context(), pool, stmts)
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: action, Detail: auditDetail(strings.Join(stmts, "; ")), Success: err == nil})
	return err
}

// Schemas: drop / alter. Dropping a schema can take its tables with it, so it is
// admin-gated like drop-table; renaming/reassigning owner is a write op.

func (s *Server) apiDropSchema(w http.ResponseWriter, r *http.Request) {
	u, c, pool, ok := s.apiRequireWrite(w, r, true)
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
	if err := s.execDDLAudit(r, u, c, pool, "pg_ddl_drop_schema", postgres.DropSchemaSQL(in.Schema, in.Cascade)); err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiAlterSchema(w http.ResponseWriter, r *http.Request) {
	u, c, pool, ok := s.apiRequireWrite(w, r, false)
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
	stmts := postgres.AlterSchemaSQL(strings.TrimSpace(in.Schema), strings.TrimSpace(in.NewName), strings.TrimSpace(in.Owner))
	if len(stmts) == 0 {
		apiErr(w, http.StatusBadRequest, "nothing to change")
		return
	}
	if err := s.execScriptAudit(r, u, c, pool, "pg_ddl_alter_schema", stmts); err != nil {
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
	_, pool, ok := s.apiPGPool(w, r)
	if !ok {
		return
	}
	roles, err := postgres.Roles(r.Context(), pool)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": roles})
}

func (s *Server) apiDropRole(w http.ResponseWriter, r *http.Request) {
	u, c, pool, ok := s.apiRequireWrite(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		apiErr(w, http.StatusBadRequest, "role name is required")
		return
	}
	if err := s.execDDLAudit(r, u, c, pool, "pg_ddl_drop_role", postgres.DropRoleSQL(in.Name)); err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiAlterRole(w http.ResponseWriter, r *http.Request) {
	u, c, pool, ok := s.apiRequireWrite(w, r, true)
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
	}
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		apiErr(w, http.StatusBadRequest, "role name is required")
		return
	}
	stmts := postgres.AlterRoleSQL(strings.TrimSpace(in.Name), strings.TrimSpace(in.NewName), postgres.RoleAttrs{
		Login: in.Login, Super: in.Superuser, CreateDB: in.CreateDB, CreateRole: in.CreateRole, Password: in.Password,
	})
	if len(stmts) == 0 {
		apiErr(w, http.StatusBadRequest, "nothing to change")
		return
	}
	if err := s.execScriptAudit(r, u, c, pool, "pg_ddl_alter_role", stmts); err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Redis

func (s *Server) apiRedisKeys(w http.ResponseWriter, r *http.Request) {
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return
	}
	client, err := s.reg.Redis(r.Context(), c)
	if err != nil {
		apiErr(w, http.StatusBadGateway, "connect: "+err.Error())
		return
	}
	cursor, _ := strconv.ParseUint(r.URL.Query().Get("cursor"), 10, 64)
	page, err := redisdb.Scan(r.Context(), client, orStar(r.URL.Query().Get("match")), cursor, 100)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": page.Keys, "cursor": page.Cursor})
}

func (s *Server) apiRedisValue(w http.ResponseWriter, r *http.Request) {
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return
	}
	client, err := s.reg.Redis(r.Context(), c)
	if err != nil {
		apiErr(w, http.StatusBadGateway, "connect: "+err.Error())
		return
	}
	val, err := redisdb.Get(r.Context(), client, r.URL.Query().Get("key"))
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"value": val})
}

func (s *Server) apiRedisCmd(w http.ResponseWriter, r *http.Request) {
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
		Cmd     string `json:"cmd"`
		Confirm bool   `json:"confirm"`
	}
	if err := readJSON(r, &in); err != nil {
		apiErr(w, http.StatusBadRequest, "bad json")
		return
	}
	args := redisdb.ParseArgs(in.Cmd)
	if len(args) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"error": "empty command"})
		return
	}
	readOnly := c.ReadOnly || !s.access(r.Context(), u, c).Write
	cmd := strings.ToLower(args[0])
	if readOnly && !redisReadAllow[cmd] {
		writeJSON(w, http.StatusOK, map[string]any{"error": "read-only: command '" + cmd + "' is not permitted"})
		return
	}
	if redisdb.NeedsConfirm(args) {
		if !u.Admin {
			writeJSON(w, http.StatusOK, map[string]any{"error": "admin required for '" + cmd + "'"})
			return
		}
		if !in.Confirm {
			writeJSON(w, http.StatusOK, map[string]any{"needConfirm": true, "cmd": in.Cmd})
			return
		}
	}
	client, err := s.reg.Redis(r.Context(), c)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	res, err := redisdb.Command(r.Context(), client, args)
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: c.ID, Action: "redis_cmd", Detail: auditDetail(in.Cmd), Success: err == nil})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"out": res})
}

// Audit

type auditDTO struct {
	TS      string `json:"ts"`
	User    string `json:"user"`
	ConnID  int64  `json:"connId"`
	Action  string `json:"action"`
	Detail  string `json:"detail"`
	Success bool   `json:"success"`
}

func (s *Server) apiAudit(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	if !u.Admin {
		apiErr(w, http.StatusForbidden, "admin required")
		return
	}
	rows, err := s.st.ListAudit(r.Context(), 200)
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]auditDTO, 0, len(rows))
	for _, a := range rows {
		out = append(out, auditDTO{
			TS: a.TS.Format(time.RFC3339), User: a.User, ConnID: a.ConnID,
			Action: a.Action, Detail: a.Detail, Success: a.Success,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": out})
}

// apiAuditExport streams the FULL audit log as a download for SIEM ingestion or
// forensics (admin only). format=jsonl (default) emits one JSON object per line;
// format=csv emits a header plus rows. It streams via IterAudit so a large log
// isn't buffered in memory.
func (s *Server) apiAuditExport(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	if !u.Admin {
		apiErr(w, http.StatusForbidden, "admin required")
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "jsonl"
	}
	switch format {
	case "jsonl":
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="audit.jsonl"`)
		enc := json.NewEncoder(w)
		_ = s.st.IterAudit(r.Context(), func(a store.Audit) error {
			return enc.Encode(auditDTO{
				TS: a.TS.Format(time.RFC3339), User: a.User, ConnID: a.ConnID,
				Action: a.Action, Detail: a.Detail, Success: a.Success,
			})
		})
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="audit.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"ts", "user", "conn_id", "action", "detail", "success"})
		_ = s.st.IterAudit(r.Context(), func(a store.Audit) error {
			return cw.Write([]string{
				a.TS.Format(time.RFC3339), a.User, strconv.FormatInt(a.ConnID, 10),
				a.Action, a.Detail, strconv.FormatBool(a.Success),
			})
		})
		cw.Flush()
	default:
		apiErr(w, http.StatusBadRequest, "format must be 'jsonl' or 'csv'")
	}
}

// apiReencrypt re-encrypts every stored credential under the current primary
// key, the second half of a non-destructive key rotation (admin only). It is
// safe to run repeatedly: connections already on the primary key are skipped.
func (s *Server) apiReencrypt(w http.ResponseWriter, r *http.Request) {
	u, ok := s.apiRequireAdmin(w, r)
	if !ok {
		return
	}
	conns, err := s.st.ListConnections(r.Context())
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var checked, rewritten, failed int
	for _, c := range conns {
		if c.PasswordEnc == "" {
			continue
		}
		checked++
		enc, changed, err := s.box.Reencrypt(c.PasswordEnc)
		if err != nil {
			failed++
			continue
		}
		if !changed {
			continue
		}
		if err := s.st.UpdatePasswordEnc(r.Context(), c.ID, enc); err != nil {
			failed++
			continue
		}
		s.reg.Forget(c.ID) // drop any cached pool so it re-decrypts next use
		rewritten++
	}
	s.st.AddAudit(r.Context(), store.Audit{
		User: u.Email, Action: "reencrypt",
		Detail:  "key=" + s.box.PrimaryID() + " checked=" + strconv.Itoa(checked) + " rewritten=" + strconv.Itoa(rewritten) + " failed=" + strconv.Itoa(failed),
		Success: failed == 0,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"primaryKey": s.box.PrimaryID(), "checked": checked, "rewritten": rewritten, "failed": failed,
	})
}

// shared gates

func (s *Server) apiRequireAdmin(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
	u, _ := auth.FromContext(r.Context())
	if !s.auth.CheckCSRF(r) {
		apiErr(w, http.StatusForbidden, "bad csrf")
		return u, false
	}
	if !u.Admin {
		apiErr(w, http.StatusForbidden, "admin required")
		return u, false
	}
	return u, true
}

// apiPGPool resolves the URL's connection + Postgres pool for read endpoints.
func (s *Server) apiPGPool(w http.ResponseWriter, r *http.Request) (store.Connection, *pgxpool.Pool, bool) {
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return store.Connection{}, nil, false
	}
	pool, err := s.reg.PG(r.Context(), c)
	if err != nil {
		apiErr(w, http.StatusBadGateway, "connect: "+err.Error())
		return c, nil, false
	}
	return c, pool, true
}

// apiRequireWrite is the JSON twin of requireWrite: CSRF + write/admin + the
// connection's read-only flag, then resolve the pool.
func (s *Server) apiRequireWrite(w http.ResponseWriter, r *http.Request, admin bool) (auth.User, store.Connection, *pgxpool.Pool, bool) {
	u, _ := auth.FromContext(r.Context())
	if !s.auth.CheckCSRF(r) {
		apiErr(w, http.StatusForbidden, "bad csrf")
		return u, store.Connection{}, nil, false
	}
	if admin && !u.Admin {
		apiErr(w, http.StatusForbidden, "admin required")
		return u, store.Connection{}, nil, false
	}
	// connFor enforces read access (and 404s an inaccessible connection); the
	// per-connection write capability is checked on the resolved connection.
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return u, store.Connection{}, nil, false
	}
	if !s.access(r.Context(), u, c).Write {
		apiErr(w, http.StatusForbidden, "write access required")
		return u, c, nil, false
	}
	if c.ReadOnly {
		apiErr(w, http.StatusConflict, "connection is read-only")
		return u, c, nil, false
	}
	pool, err := s.reg.PG(r.Context(), c)
	if err != nil {
		apiErr(w, http.StatusBadGateway, "connect: "+err.Error())
		return u, c, nil, false
	}
	return u, c, pool, true
}

var _ = redis.Nil
