package web

// Connection CRUD, the "test connection" probe, and per-connection access
// grants. Split out of api.go; part of the JSON API mounted under /api.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/dbsql"
	"verix-dbm/internal/store"
)

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
	switch c.Engine() {
	case dbsql.FamilyRedis:
		err = pingRedis(ctx, c, in.Password)
	case dbsql.FamilyMySQL:
		err = pingMySQL(ctx, c, in.Password)
	case dbsql.FamilySQLite:
		err = pingSQLite(ctx, c, s.cfg.SQLiteDir)
	case dbsql.FamilyMongo:
		err = pingMongo(ctx, c, in.Password)
	default:
		err = pingPG(ctx, c, in.Password)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
