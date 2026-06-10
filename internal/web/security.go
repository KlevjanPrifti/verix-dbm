package web

import (
	"net/http"
	"regexp"
	"strings"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/config"
	"verix-dbm/internal/postgres"
)

// serverSideBlocked reports whether the given SQL fragments (a console statement,
// or a grid/export WHERE+ORDER BY) use a server-side execution / file primitive
// that must be denied to this user. Admins are exempt (they're trusted with the
// connection's full DB-role privileges); everyone else including read-only
// users whose filters would otherwise run such functions is blocked. The real
// control is using a least-privileged DB role on each connection (see SECURITY.md);
// this is defense in depth. (H3)
const serverSideBlockedMsg = "blocked: server-side program execution / file access is restricted to admins see SECURITY.md"

func serverSideBlocked(u auth.User, fragments ...string) bool {
	if u.Admin {
		return false
	}
	return postgres.IsServerSideExec(strings.Join(fragments, "\n"))
}

// sessionKey identifies the caller for per-user rate limiting: the authenticated
// email when present (set by auth.Middleware), else the client IP.
func sessionKey(r *http.Request) string {
	if u, ok := auth.FromContext(r.Context()); ok && u.Email != "" {
		return "u:" + u.Email
	}
	return "ip:" + clientIP(r)
}

// --- Audit redaction (M1) ---------------------------------------------------

var (
	// SQL: PASSWORD '…' / IDENTIFIED BY '…' (handles doubled-quote escaping).
	reSQLPassword = regexp.MustCompile(`(?i)\b(password|identified by)(\s+)'(?:[^']|'')*'`)
	// Redis: requirepass <val>, and AUTH <val> at the start of a command line.
	reRedisRequirepass = regexp.MustCompile(`(?i)\b(requirepass)(\s+)\S+`)
	reRedisAuth        = regexp.MustCompile(`(?im)^(\s*auth)(\s+)\S+`)
)

// auditDetail redacts credentials from a statement before it is persisted in the
// audit log, then truncates it. DDL like CREATE/ALTER ROLE … PASSWORD 'secret'
// and Redis AUTH/CONFIG SET requirepass would otherwise store cleartext secrets
// in SQLite (and any backup of it).
func auditDetail(s string) string {
	s = reSQLPassword.ReplaceAllString(s, `$1$2'***'`)
	s = reRedisRequirepass.ReplaceAllString(s, `$1$2***`)
	s = reRedisAuth.ReplaceAllString(s, `$1$2***`)
	return truncate(s, 500)
}

// --- Security headers (M2) --------------------------------------------------

// securityHeaders sets response headers that harden the app against clickjacking,
// MIME sniffing, referrer leakage, and (when served over TLS) downgrade attacks,
// plus a CSP. The CSP keeps 'unsafe-eval' (the legacy Alpine.js workbench needs
// it) and 'unsafe-inline' for styles (inline style attributes + the Google Fonts
// @import); scripts and everything else are locked to same-origin, and framing is
// denied outright. Tightening script/style-src further is gated on dropping
// Alpine and self-hosting fonts.
func securityHeaders(cfg *config.Config) func(http.Handler) http.Handler {
	csp := strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' 'unsafe-eval'",
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
		"font-src 'self' https://fonts.gstatic.com",
		"img-src 'self' data:",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
	https := len(cfg.BaseURL) >= 5 && cfg.BaseURL[:5] == "https"

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Frame-Options", "DENY")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Content-Security-Policy", csp)
			if https {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}
