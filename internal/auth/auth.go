// Package auth handles Keycloak OIDC login (with a DEV auto-login fallback),
// server-side sessions, role-based capabilities, and CSRF tokens.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
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
	Admin   bool // may do anything, incl. destructive ops & connection CRUD
	Write   bool // may mutate data
	CSRF    string
}

type session struct {
	user    User
	expires time.Time
	// idToken is the raw OIDC ID token, kept for RP-initiated logout (id_token_hint).
	idToken string
	// oauthState is the expected callback state for an in-flight login.
	oauthState string
}

type Authenticator struct {
	cfg      *config.Config
	mu       sync.Mutex
	sessions map[string]*session

	provider   *oidc.Provider
	verifier   *oidc.IDTokenVerifier
	oauth      *oauth2.Config
	endSession string // OIDC end_session_endpoint (from provider metadata)
}

func New(ctx context.Context, cfg *config.Config) (*Authenticator, error) {
	a := &Authenticator{cfg: cfg, sessions: map[string]*session{}}
	if !cfg.DevMode {
		p, err := oidc.NewProvider(ctx, cfg.OIDCIssuer)
		if err != nil {
			return nil, err
		}
		a.provider = p
		a.verifier = p.Verifier(&oidc.Config{ClientID: cfg.OIDCClientID})
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
	go a.reap()
	return a, nil
}

func (a *Authenticator) reap() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		a.mu.Lock()
		now := time.Now()
		for k, s := range a.sessions {
			if now.After(s.expires) {
				delete(a.sessions, k)
			}
		}
		a.mu.Unlock()
	}
}

// Middleware requires an authenticated session, redirecting to login otherwise.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := a.current(r); ok {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
			return
		}
		if a.cfg.DevMode {
			// Auto-login a local admin so the app is usable without Keycloak.
			u := User{Subject: "dev", Name: "Dev Admin", Email: "dev@localhost", Roles: []string{"dev"}, Admin: true, Write: true}
			tok := a.put(u, "")
			setCookie(w, tok, a.cfg)
			u.CSRF = a.csrfFor(tok)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
			return
		}
		http.Redirect(w, r, "/auth/login", http.StatusFound)
	})
}

// Login starts the OIDC code flow (or no-ops to "/" in dev mode).
func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request) {
	if a.cfg.DevMode {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	state := token()
	// Stash the expected state in a short-lived cookie-less session entry.
	a.mu.Lock()
	a.sessions["state:"+state] = &session{expires: time.Now().Add(10 * time.Minute), oauthState: state}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "dbm_state", Value: state, Path: "/", HttpOnly: true, Secure: secure(a.cfg), SameSite: http.SameSiteLaxMode, MaxAge: 600})
	http.Redirect(w, r, a.oauth.AuthCodeURL(state), http.StatusFound)
}

// Callback completes the OIDC code flow.
func (a *Authenticator) Callback(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie("dbm_state")
	if err != nil || r.URL.Query().Get("state") != stateCookie.Value {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	oauth2Token, err := a.oauth.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}
	rawID, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "no id_token", http.StatusBadGateway)
		return
	}
	idToken, err := a.verifier.Verify(r.Context(), rawID)
	if err != nil {
		http.Error(w, "id_token verify failed", http.StatusUnauthorized)
		return
	}
	var claims struct {
		Sub         string `json:"sub"`
		Name        string `json:"name"`
		Email       string `json:"email"`
		RealmAccess struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
	}
	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, "claims parse failed", http.StatusBadGateway)
		return
	}
	// Keycloak places realm roles in the access token by default and only in the
	// ID token if a mapper opts in — so merge roles from both to be robust.
	roles := mergeRoles(claims.RealmAccess.Roles, realmRolesFromJWT(oauth2Token.AccessToken))
	u := User{Subject: claims.Sub, Name: claims.Name, Email: claims.Email, Roles: roles}
	for _, role := range u.Roles {
		if role == a.cfg.OIDCAdminRole {
			u.Admin = true
			u.Write = true
		}
		if role == a.cfg.OIDCWriteRole {
			u.Write = true
		}
	}
	tok := a.put(u, rawID)
	setCookie(w, tok, a.cfg)
	http.Redirect(w, r, "/", http.StatusFound)
}

// Logout clears the local session and, when possible, ends the Keycloak SSO
// session too (RP-initiated logout). Without the latter the IdP would silently
// re-authenticate the user on the next /auth/login redirect.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	var idHint string
	if c, err := r.Cookie(cookieName); err == nil {
		a.mu.Lock()
		if s, ok := a.sessions[c.Value]; ok {
			idHint = s.idToken
		}
		delete(a.sessions, c.Value)
		a.mu.Unlock()
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

// realmRolesFromJWT decodes a JWT payload (no signature check — the token came
// straight from the trusted token endpoint) and returns its realm_access roles.
func realmRolesFromJWT(raw string) []string {
	parts := strings.Split(raw, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var c struct {
		RealmAccess struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil
	}
	return c.RealmAccess.Roles
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
	a.mu.Lock()
	s, ok := a.sessions[c.Value]
	a.mu.Unlock()
	if !ok || time.Now().After(s.expires) {
		return User{}, false
	}
	u := s.user
	u.CSRF = a.csrfFor(c.Value)
	return u, true
}

func (a *Authenticator) put(u User, idToken string) string {
	tok := token()
	a.mu.Lock()
	a.sessions[tok] = &session{user: u, idToken: idToken, expires: time.Now().Add(sessionTTL)}
	a.mu.Unlock()
	return tok
}

// csrfFor derives a stable per-session CSRF token (the session id reversed-hash
// would be better; here we reuse a deterministic value tied to the session key).
func (a *Authenticator) csrfFor(sessionKey string) string {
	if len(sessionKey) >= 16 {
		return sessionKey[:16]
	}
	return sessionKey
}

// CheckCSRF validates the csrf form value against the caller's session.
func (a *Authenticator) CheckCSRF(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return r.FormValue("csrf") == a.csrfFor(c.Value)
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

func token() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
