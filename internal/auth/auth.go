// Package auth handles Keycloak OIDC login (with a DEV auto-login fallback),
// server-side sessions, role-based capabilities, and CSRF tokens.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"verix-dbm/internal/config"
)

type ctxKey int

const userKey ctxKey = 0

const cookieName = "dbm_session"
const sessionTTL = 12 * time.Hour

// User is the authenticated principal and its capabilities.
type User struct {
	Subject string
	Name    string
	Email   string
	Roles   []string
	Groups  []string // Keycloak group memberships (for per-connection grants)
	Admin   bool     // may do anything, incl. destructive ops & connection CRUD
	Write   bool     // may mutate data
	Read    bool     // may browse/query (deny-by-default; write/admin imply it)
	CSRF    string
}

// Subjects returns the identifiers a per-connection grant may be keyed on: the
// user's realm roles and group memberships, unioned. Used by scoped-access mode
// to decide which connections a non-admin user can reach.
func (u User) Subjects() []string {
	return mergeRoles(u.Roles, u.Groups)
}

type session struct {
	user    User
	expires time.Time
	// csrf is a per-session token, independent of the session id, embedded in
	// rendered pages and validated on state-changing requests.
	csrf string
	// idToken is the raw OIDC ID token, kept for RP-initiated logout (id_token_hint).
	idToken string
	// oauthState is the expected callback state for an in-flight login.
	oauthState string
}

type Authenticator struct {
	cfg      *config.Config
	sessions sessionStore

	provider   *oidc.Provider
	verifier   *oidc.IDTokenVerifier
	atVerifier *oidc.IDTokenVerifier // verifies the access token (audience check skipped)
	oauth      *oauth2.Config
	endSession string // OIDC end_session_endpoint (from provider metadata)

	// auditFn records security-relevant auth events (login success/failure).
	// Optional; nil-safe via audit().
	auditFn func(ctx context.Context, action, detail string, success bool)
}

// SetAudit registers a recorder for auth events (wired to the store by main).
func (a *Authenticator) SetAudit(fn func(ctx context.Context, action, detail string, success bool)) {
	a.auditFn = fn
}

func (a *Authenticator) audit(ctx context.Context, action, detail string, success bool) {
	if a.auditFn != nil {
		a.auditFn(ctx, action, detail, success)
	}
}

func New(ctx context.Context, cfg *config.Config) (*Authenticator, error) {
	sessions, err := newSessionStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	a := &Authenticator{cfg: cfg, sessions: sessions}
	if !cfg.DevMode {
		p, err := oidc.NewProvider(ctx, cfg.OIDCIssuer)
		if err != nil {
			return nil, err
		}
		a.provider = p
		a.verifier = p.Verifier(&oidc.Config{ClientID: cfg.OIDCClientID})
		// Access tokens are JWTs signed by the same realm but audienced to the
		// resource server, not the client so verify signature/issuer/expiry but
		// skip the audience check. Used to validate realm roles before trusting them.
		a.atVerifier = p.Verifier(&oidc.Config{ClientID: cfg.OIDCClientID, SkipClientIDCheck: true})
		// Pull the RP-initiated logout endpoint out of the discovery document.
		var md struct {
			EndSession string `json:"end_session_endpoint"`
		}
		if err := p.Claims(&md); err == nil {
			a.endSession = md.EndSession
		}
		a.oauth = &oauth2.Config{
			ClientID:     cfg.OIDCClientID,
			ClientSecret: cfg.OIDCClientSecret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  cfg.OIDCRedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		}
	}
	return a, nil
}

// Middleware requires an authenticated session with at least read access,
// redirecting unauthenticated callers to login and rejecting authenticated-but-
// unauthorised ones with 403 (deny-by-default: a valid session is not enough).
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := a.current(r); ok {
			if !u.Read {
				a.forbidden(w)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
			return
		}
		if a.cfg.DevMode {
			// Auto-login a local admin so the app is usable without Keycloak.
			u := User{Subject: "dev", Name: "Dev Admin", Email: "dev@localhost", Roles: []string{"dev"}, Admin: true, Write: true, Read: true}
			tok := a.put(u, "")
			setCookie(w, tok, a.cfg)
			u.CSRF = a.csrfFor(tok)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
			return
		}
		http.Redirect(w, r, "/auth/login", http.StatusFound)
	})
}

// forbidden tells an authenticated user they lack any verix-dbm role. It's a
// dead end on purpose redirecting to login would just loop, since they already
// have a valid session.
func (a *Authenticator) forbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte("Access denied: your account has no verix-dbm role.\n" +
		"Ask an administrator to grant you the " + a.cfg.OIDCReadRole + ", " +
		a.cfg.OIDCWriteRole + ", or " + a.cfg.OIDCAdminRole + " realm role."))
}

// Login starts the OIDC code flow (or no-ops to "/" in dev mode).
func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request) {
	if a.cfg.DevMode {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	state := token()
	// Stash the expected state in a short-lived cookie-less session entry.
	a.sessions.Put("state:"+state, &session{expires: time.Now().Add(10 * time.Minute), oauthState: state}, 10*time.Minute)
	http.SetCookie(w, &http.Cookie{Name: "dbm_state", Value: state, Path: "/", HttpOnly: true, Secure: secure(a.cfg), SameSite: http.SameSiteLaxMode, MaxAge: 600})
	http.Redirect(w, r, a.oauth.AuthCodeURL(state), http.StatusFound)
}

// Callback completes the OIDC code flow.
func (a *Authenticator) Callback(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie("dbm_state")
	if err != nil || r.URL.Query().Get("state") != stateCookie.Value {
		a.audit(r.Context(), "auth_login_failed", "invalid state", false)
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	oauth2Token, err := a.oauth.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		a.audit(r.Context(), "auth_login_failed", "token exchange failed", false)
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}
	rawID, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		a.audit(r.Context(), "auth_login_failed", "no id_token", false)
		http.Error(w, "no id_token", http.StatusBadGateway)
		return
	}
	idToken, err := a.verifier.Verify(r.Context(), rawID)
	if err != nil {
		a.audit(r.Context(), "auth_login_failed", "id_token verify failed", false)
		http.Error(w, "id_token verify failed", http.StatusUnauthorized)
		return
	}
	var claims struct {
		Sub         string   `json:"sub"`
		Name        string   `json:"name"`
		Email       string   `json:"email"`
		Groups      []string `json:"groups"`
		RealmAccess struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
	}
	if err := idToken.Claims(&claims); err != nil {
		a.audit(r.Context(), "auth_login_failed", "claims parse failed", false)
		http.Error(w, "claims parse failed", http.StatusBadGateway)
		return
	}
	// Keycloak places realm roles in the access token by default and only in the
	// ID token if a mapper opts in so merge roles from both. The access token is
	// signature-verified (atVerifier) before we trust its roles; if it isn't a
	// verifiable JWT we fall back to the ID token's roles only.
	roles := claims.RealmAccess.Roles
	groups := claims.Groups
	if at, err := a.atVerifier.Verify(r.Context(), oauth2Token.AccessToken); err == nil {
		var ac struct {
			Groups      []string `json:"groups"`
			RealmAccess struct {
				Roles []string `json:"roles"`
			} `json:"realm_access"`
		}
		if at.Claims(&ac) == nil {
			roles = mergeRoles(roles, ac.RealmAccess.Roles)
			groups = mergeRoles(groups, ac.Groups)
		}
	}
	u := User{Subject: claims.Sub, Name: claims.Name, Email: claims.Email, Roles: roles, Groups: groups}
	a.applyCaps(&u)
	a.audit(r.Context(), "auth_login", u.Email, true)
	tok := a.put(u, rawID)
	setCookie(w, tok, a.cfg)
	http.Redirect(w, r, "/", http.StatusFound)
}

// Logout clears the local session and, when possible, ends the Keycloak SSO
// session too (RP-initiated logout). Without the latter the IdP would silently
// re-authenticate the user on the next /auth/login redirect.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	// POST + CSRF so a cross-origin page can't force the user out.
	if !a.CheckCSRF(r) {
		http.Error(w, "bad csrf", http.StatusForbidden)
		return
	}
	var idHint string
	if c, err := r.Cookie(cookieName); err == nil {
		if s, ok := a.sessions.Get(c.Value); ok {
			idHint = s.idToken
		}
		a.sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})

	// Dev mode has no IdP; just land on home.
	if a.cfg.DevMode || a.endSession == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	q := url.Values{}
	if idHint != "" {
		q.Set("id_token_hint", idHint)
	}
	q.Set("client_id", a.cfg.OIDCClientID)
	q.Set("post_logout_redirect_uri", a.cfg.BaseURL)
	http.Redirect(w, r, a.endSession+"?"+q.Encode(), http.StatusFound)
}

// applyCaps maps the user's realm roles onto capabilities. Admin implies write
// implies read. With OpenRead, any authenticated user gets read regardless of
// roles (the opt-in pre-1.0 behaviour).
func (a *Authenticator) applyCaps(u *User) {
	for _, role := range u.Roles {
		switch role {
		case a.cfg.OIDCAdminRole:
			u.Admin, u.Write, u.Read = true, true, true
		case a.cfg.OIDCWriteRole:
			u.Write, u.Read = true, true
		case a.cfg.OIDCReadRole:
			u.Read = true
		}
	}
	if a.cfg.OpenRead {
		u.Read = true
	}
}

// mergeRoles unions role lists, dropping duplicates and empties.
func mergeRoles(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, r := range l {
			if r == "" || seen[r] {
				continue
			}
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

func (a *Authenticator) current(r *http.Request) (User, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return User{}, false
	}
	s, ok := a.sessions.Get(c.Value)
	if !ok {
		return User{}, false
	}
	u := s.user
	u.CSRF = s.csrf
	return u, true
}

func (a *Authenticator) put(u User, idToken string) string {
	tok := token()
	a.sessions.Put(tok, &session{user: u, idToken: idToken, csrf: token(), expires: time.Now().Add(sessionTTL)}, sessionTTL)
	return tok
}

// csrfFor returns the session's independent CSRF token (empty if no session).
func (a *Authenticator) csrfFor(sessionKey string) string {
	if s, ok := a.sessions.Get(sessionKey); ok {
		return s.csrf
	}
	return ""
}

// CheckCSRF validates the request's CSRF token (sent as the X-CSRF-Token header
// for HTMX/fetch requests, or a "csrf" form field) against the caller's session,
// using a constant-time comparison.
func (a *Authenticator) CheckCSRF(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	want := a.csrfFor(c.Value)
	if want == "" {
		return false
	}
	got := r.Header.Get("X-CSRF-Token")
	if got == "" {
		got = r.FormValue("csrf")
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// FromContext returns the authenticated user attached by Middleware.
func FromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userKey).(User)
	return u, ok
}

func setCookie(w http.ResponseWriter, tok string, cfg *config.Config) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure(cfg),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func secure(cfg *config.Config) bool {
	return len(cfg.BaseURL) >= 5 && cfg.BaseURL[:5] == "https"
}

// token returns a 192-bit random hex token. A crypto/rand failure is fatal to
// the request rather than tolerated: returning a predictable (e.g. all-zero)
// session/CSRF token would be far worse than a 500. Recoverer turns the panic
// into a failed request, so we fail closed no token is ever minted weak.
func token() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
