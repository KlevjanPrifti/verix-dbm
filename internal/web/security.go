package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/config"
	"verix-dbm/internal/dbsql"
	"verix-dbm/internal/mysql"
	"verix-dbm/internal/postgres"
	"verix-dbm/internal/sqlite"
)

// serverSideBlocked reports whether the given SQL fragments (a console statement,
// or a grid/export WHERE+ORDER BY) use a server-side execution / file primitive
// that must be denied to this user. Admins are exempt (they're trusted with the
// connection's full DB-role privileges); everyone else including read-only
// users whose filters would otherwise run such functions is blocked. The real
// control is using a least-privileged DB role on each connection (see SECURITY.md);
// this is defense in depth. (H3)
const serverSideBlockedMsg = "blocked: server-side program execution / file access is restricted to admins see SECURITY.md"

func serverSideBlocked(u auth.User, kind string, fragments ...string) bool {
	if u.Admin {
		return false
	}
	joined := strings.Join(fragments, "\n")
	switch dbsql.Family(kind) {
	case dbsql.FamilyMySQL:
		return mysql.IsServerSideExec(joined)
	case dbsql.FamilySQLite:
		return sqlite.IsServerSideExec(joined)
	}
	return postgres.IsServerSideExec(joined)
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
	// SQL dollar-quoted: PASSWORD $$…$$ or $tag$…$tag$. RE2 has no backreferences,
	// so the closing tag is matched loosely; that only ever over-redacts, which is
	// the safe direction for an audit line.
	reSQLPasswordDollar = regexp.MustCompile(`(?i)\b(password|identified by)(\s+)\$[A-Za-z0-9_]*\$[\s\S]*?\$[A-Za-z0-9_]*\$`)
	// Mongo / JSON: "pwd": "…" or "password": "…" in a command document
	// (e.g. db.createUser({user:…, pwd:"secret"})).
	reJSONPassword = regexp.MustCompile(`(?i)("?(?:pwd|password)"?\s*:\s*)"(?:[^"\\]|\\.)*"`)
	// Redis: requirepass <val>, and AUTH <val> at the start of a command line.
	reRedisRequirepass = regexp.MustCompile(`(?i)\b(requirepass)(\s+)\S+`)
	reRedisAuth        = regexp.MustCompile(`(?im)^(\s*auth)(\s+)\S+`)
)

// auditDetail redacts credentials from a statement before it is persisted in the
// audit log, then truncates it. DDL like CREATE/ALTER ROLE … PASSWORD 'secret'
// and Redis AUTH/CONFIG SET requirepass would otherwise store cleartext secrets
// in SQLite (and any backup of it).
func auditDetail(s string) string {
	s = reSQLPassword.ReplaceAllString(s, `${1}${2}'***'`)
	s = reSQLPasswordDollar.ReplaceAllString(s, `${1}${2}'***'`)
	s = reJSONPassword.ReplaceAllString(s, `${1}"***"`)
	s = reRedisRequirepass.ReplaceAllString(s, `${1}${2}***`)
	s = reRedisAuth.ReplaceAllString(s, `${1}${2}***`)
	return truncate(s, 500)
}

// --- Security headers (M2) --------------------------------------------------

// securityHeaders sets response headers that harden the app against clickjacking,
// MIME sniffing, referrer leakage, and (when served over TLS) downgrade attacks,
// plus a CSP. script-src is locked to same-origin (the Vite-built React SPA needs
// no eval); style-src keeps 'unsafe-inline' for inline style attributes + the
// Google Fonts @import. Everything else is same-origin and framing is denied
// outright. Tightening style-src further is gated on self-hosting fonts.
// originOf returns the scheme://host of a URL, or "" if it can't be parsed into
// an absolute origin. Used to whitelist the IdP in the CSP form-action directive.
func originOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func securityHeaders(cfg *config.Config) func(http.Handler) http.Handler {
	// RP-initiated logout submits the logout form to /auth/logout, which 302s to
	// the IdP's end_session_endpoint. Browsers apply form-action to the whole
	// redirect chain, so the IdP origin must be allowed or the logout redirect is
	// blocked (session is cleared but the user is stranded). Allow the issuer's
	// scheme://host; the end_session endpoint shares it.
	formAction := "'self'"
	if origin := originOf(cfg.OIDCIssuer); origin != "" {
		formAction += " " + origin
	}
	csp := strings.Join([]string{
		"default-src 'self'",
		// The Vite-built React SPA ships no eval/new Function, so script-src stays
		// strict: 'self' only, no 'unsafe-eval'.
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
		"font-src 'self' https://fonts.gstatic.com",
		"img-src 'self' data:",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action " + formAction,
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
