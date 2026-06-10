// Package web wires the HTTP router, the JSON API, and the embedded React SPA.
package web

import (
	"net/http"
	"strconv"
	"time"

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
}

func NewServer(cfg *config.Config, st *store.Store, reg *conn.Registry, a *auth.Authenticator, box *crypto.Box) *Server {
	return &Server{cfg: cfg, st: st, reg: reg, auth: a, box: box}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	// Only derive the client IP from forwarded headers when explicitly told we
	// sit behind a trusted proxy; otherwise clients could spoof X-Forwarded-For.
	if s.cfg.TrustProxy {
		r.Use(middleware.RealIP)
	}
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders(s.cfg))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	// Throttle auth endpoints against brute-force / redirect spam, per client IP.
	authLimit := newRateLimiter(20, time.Minute)
	r.Group(func(r chi.Router) {
		r.Use(authLimit.middleware)
		r.Get("/auth/login", s.auth.Login)
		r.Get("/auth/callback", s.auth.Callback)
	})
	// Logout is POST + CSRF (not GET) so a cross-site page can't force a logout.
	r.Post("/auth/logout", s.auth.Logout)

	// A generous per-user limiter on the authed surface a backstop against a
	// runaway client or scripted abuse of the query/command endpoints, without
	// throttling normal interactive use. Keyed by user so a shared egress IP
	// doesn't pool everyone together.
	authedLimit := newRateLimiter(600, time.Minute)
	r.Group(func(r chi.Router) {
		r.Use(s.auth.Middleware)
		r.Use(authedLimit.middlewareBy(sessionKey))
		// Root serves the React SPA, the only UI.
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/app", http.StatusFound)
		})

		// CSV export streams a file download rather than JSON, so it lives
		// outside the /api surface; the SPA links to it directly.
		r.Get("/c/{id}/export", s.exportTable)

		// JSON API for the React/Vite SPA, plus the SPA shell + assets.
		r.Route("/api", s.mountAPI)
		r.Handle("/app", s.spaHandler())
		r.Handle("/app/*", s.spaHandler())
	})
	return r
}

func idParam(r *http.Request) int64 {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return id
}

// connFor loads the connection referenced in the URL.
func (s *Server) connFor(r *http.Request) (store.Connection, error) {
	return s.st.GetConnection(r.Context(), idParam(r))
}
