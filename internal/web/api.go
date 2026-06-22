package web

// JSON API consumed by the React/Vite SPA (internal/web/spa). It mirrors the
// HTMX handlers but speaks JSON: same auth middleware, same CSRF (X-CSRF-Token
// header), same role gating. Mounted under /api by Router().
//
// Handlers are split across api_*.go by domain (connections, sql, redis, mongo,
// audit); this file holds the route table, the JSON plumbing, the session
// endpoint, and the shared request gates.

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/dbsql"
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

	r.Get("/c/{id}/mongo/docs", s.apiMongoDocs)
	r.Get("/c/{id}/mongo/indexes", s.apiMongoIndexes)
	r.Post("/c/{id}/mongo/cmd", s.apiMongoCmd)

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

// apiSQL resolves the URL's connection + its dbsql.Engine for read endpoints
// (Postgres or MySQL, per the connection's kind).
func (s *Server) apiSQL(w http.ResponseWriter, r *http.Request) (store.Connection, dbsql.Engine, bool) {
	c, err := s.connFor(r)
	if err != nil {
		apiErr(w, http.StatusNotFound, "connection not found")
		return store.Connection{}, nil, false
	}
	eng, err := s.reg.Engine(r.Context(), c)
	if err != nil {
		apiErr(w, http.StatusBadGateway, "connect: "+err.Error())
		return c, nil, false
	}
	return c, eng, true
}

// apiRequireWrite is the JSON twin of requireWrite: CSRF + write/admin + the
// connection's read-only flag, then resolve the engine.
func (s *Server) apiRequireWrite(w http.ResponseWriter, r *http.Request, admin bool) (auth.User, store.Connection, dbsql.Engine, bool) {
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
	eng, err := s.reg.Engine(r.Context(), c)
	if err != nil {
		apiErr(w, http.StatusBadGateway, "connect: "+err.Error())
		return u, c, nil, false
	}
	return u, c, eng, true
}
