// Package web wires the HTTP router, handlers, and HTMX/template rendering.
package web

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/config"
	"verix-dbm/internal/conn"
	"verix-dbm/internal/crypto"
	"verix-dbm/internal/store"
)

type Server struct {
	cfg  *config.Config
	st   *store.Store
	reg  *conn.Registry
	auth *auth.Authenticator
	box  *crypto.Box
	rnd  *renderer
}

func NewServer(cfg *config.Config, st *store.Store, reg *conn.Registry, a *auth.Authenticator, box *crypto.Box) *Server {
	return &Server{cfg: cfg, st: st, reg: reg, auth: a, box: box, rnd: newRenderer()}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Handle("/static/*", staticFS())
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	r.Get("/auth/login", s.auth.Login)
	r.Get("/auth/callback", s.auth.Callback)
	r.Get("/auth/logout", s.auth.Logout)

	r.Group(func(r chi.Router) {
		r.Use(s.auth.Middleware)
		r.Get("/", s.workbench)
		r.Post("/connections", s.createConnection)
		r.Post("/connections/test", s.testConnection)
		r.Post("/connections/{id}", s.updateConnection)
		r.Post("/connections/{id}/delete", s.deleteConnection)
		r.Get("/c/{id}/edit", s.editConnForm)

		// Explorer tree (lazy fragments) + tab content.
		r.Get("/c/{id}/explorer", s.explorer)
		r.Get("/c/{id}/pg/columns", s.pgColumns)
		r.Get("/c/{id}/pg/indexes", s.pgIndexes)
		r.Get("/c/{id}/pg/keys", s.pgKeys)
		r.Get("/c/{id}/grid", s.gridView)
		r.Get("/c/{id}/console", s.consoleTab)

		// DataGrip-style context-menu actions.
		// Read-only generators (clipboard text) + info tabs.
		r.Get("/c/{id}/pg/ddl", s.pgDDL)
		r.Get("/c/{id}/pg/generate", s.pgGenerate)
		r.Get("/c/{id}/pg/doc", s.pgDoc)
		r.Get("/c/{id}/pg/usages", s.pgUsages)
		r.Get("/c/{id}/export", s.exportTable)
		// Mutating DDL — guarded by CSRF + write/admin + read-only in the handlers.
		r.Get("/c/{id}/pg/form", s.pgDDLForm)
		r.Post("/c/{id}/pg/ddl/run", s.pgRunForm)
		r.Post("/c/{id}/pg/table/drop", s.pgDropTable)
		r.Post("/c/{id}/pg/table/truncate", s.pgTruncate)
		r.Post("/c/{id}/pg/column/drop", s.pgDropColumn)
		r.Post("/c/{id}/pg/index/drop", s.pgDropIndex)

		// Legacy full-page views (still reachable) + shared HTMX endpoints.
		r.Get("/c/{id}", s.openConnection)
		r.Get("/c/{id}/pg", s.pgView)
		r.Post("/c/{id}/pg/query", s.pgQuery)
		r.Get("/c/{id}/redis", s.redisView)
		r.Get("/c/{id}/redis/keys", s.redisKeys)
		r.Get("/c/{id}/redis/value", s.redisValue)
		r.Post("/c/{id}/redis/cmd", s.redisCmd)

		r.Get("/audit", s.audit)
	})
	return r
}

// view is the data passed to templates. A single wide struct keeps templates simple.
type view struct {
	User        auth.User
	Active      string
	Flash       string
	Error       string
	Boxed       bool // center content in a max-width column (dashboard/audit)
	HasConn     bool
	Conn        store.Connection
	Connections []store.Connection
	Data        any
}

func (s *Server) newView(r *http.Request, active string) view {
	u, _ := auth.FromContext(r.Context())
	return view{User: u, Active: active}
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	v := s.newView(r, "dashboard")
	conns, err := s.st.ListConnections(r.Context())
	if err != nil {
		v.Error = err.Error()
	}
	v.Connections = conns
	s.rnd.page(w, "dashboard", v)
}

func (s *Server) createConnection(w http.ResponseWriter, r *http.Request) {
	if !s.auth.CheckCSRF(r) {
		http.Error(w, "bad csrf", http.StatusForbidden)
		return
	}
	u, _ := auth.FromContext(r.Context())
	if !u.Admin {
		http.Error(w, "admin required", http.StatusForbidden)
		return
	}
	port, _ := strconv.Atoi(r.FormValue("port"))
	c := store.Connection{
		Name:      r.FormValue("name"),
		Kind:      r.FormValue("kind"),
		Host:      r.FormValue("host"),
		Port:      port,
		DBName:    r.FormValue("dbname"),
		Username:  r.FormValue("username"),
		Options:   r.FormValue("options"),
		ReadOnly:  r.FormValue("readonly") == "on",
		CreatedBy: u.Email,
	}
	if pw := r.FormValue("password"); pw != "" {
		enc, err := s.box.Encrypt(pw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		c.PasswordEnc = enc
	} else if enc := r.FormValue("password_enc"); enc != "" {
		// "Save as copy" carries the existing ciphertext so the clone keeps creds.
		c.PasswordEnc = enc
	}
	id, err := s.st.CreateConnection(r.Context(), c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: id, Action: "create_connection", Detail: c.Name, Success: true})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	if !s.auth.CheckCSRF(r) {
		http.Error(w, "bad csrf", http.StatusForbidden)
		return
	}
	u, _ := auth.FromContext(r.Context())
	if !u.Admin {
		http.Error(w, "admin required", http.StatusForbidden)
		return
	}
	id := idParam(r)
	if err := s.st.DeleteConnection(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.reg.Forget(id)
	s.st.AddAudit(r.Context(), store.Audit{User: u.Email, ConnID: id, Action: "delete_connection", Success: true})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) openConnection(w http.ResponseWriter, r *http.Request) {
	c, err := s.st.GetConnection(r.Context(), idParam(r))
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}
	if c.Kind == "redis" {
		http.Redirect(w, r, "/c/"+strconv.FormatInt(c.ID, 10)+"/redis", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/c/"+strconv.FormatInt(c.ID, 10)+"/pg", http.StatusFound)
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.FromContext(r.Context())
	if !u.Admin {
		http.Error(w, "admin required", http.StatusForbidden)
		return
	}
	v := s.newView(r, "audit")
	v.Boxed = true
	rows, err := s.st.ListAudit(r.Context(), 200)
	if err != nil {
		v.Error = err.Error()
	}
	v.Data = rows
	s.rnd.page(w, "audit", v)
}

func idParam(r *http.Request) int64 {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return id
}

// connFor loads the connection referenced in the URL.
func (s *Server) connFor(r *http.Request) (store.Connection, error) {
	return s.st.GetConnection(r.Context(), idParam(r))
}

var _ = context.Background
