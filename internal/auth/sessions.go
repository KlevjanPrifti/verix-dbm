package auth

// Session storage is pluggable so the app can run highly available. The default
// in-memory store keeps sessions on one node (lost on restart, not shared); the
// Redis store keeps them in a shared keyspace so any replica behind a load
// balancer can serve any session, and sessions survive a restart.

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"verix-dbm/internal/config"
)

// sessionStore is the minimal surface the authenticator needs. Implementations
// handle their own expiry (the in-memory one reaps; Redis uses key TTL).
type sessionStore interface {
	Get(key string) (*session, bool)
	Put(key string, s *session, ttl time.Duration)
	Delete(key string)
}

// newSessionStore builds the configured store. For Redis it dials and pings up
// front so a misconfiguration fails the process at startup rather than at first
// login.
func newSessionStore(ctx context.Context, cfg *config.Config) (sessionStore, error) {
	switch cfg.SessionBackend {
	case "", "memory":
		return newMemSessionStore(), nil
	case "redis":
		opt, err := redis.ParseURL(cfg.SessionRedisURL)
		if err != nil {
			return nil, err
		}
		c := redis.NewClient(opt)
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := c.Ping(pctx).Err(); err != nil {
			return nil, err
		}
		return &redisSessionStore{c: c, prefix: "dbm:sess:"}, nil
	default:
		return nil, &configError{"DBM_SESSION_BACKEND must be 'memory' or 'redis', got " + cfg.SessionBackend}
	}
}

type configError struct{ msg string }

func (e *configError) Error() string { return e.msg }

// --- in-memory (single-node default) ---------------------------------------

type memSessionStore struct {
	mu sync.Mutex
	m  map[string]*session
}

func newMemSessionStore() *memSessionStore {
	s := &memSessionStore{m: map[string]*session{}}
	go s.reap()
	return s
}

func (s *memSessionStore) Get(key string) (*session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[key]
	if !ok || time.Now().After(sess.expires) {
		return nil, false
	}
	return sess, true
}

func (s *memSessionStore) Put(key string, sess *session, _ time.Duration) {
	s.mu.Lock()
	s.m[key] = sess
	s.mu.Unlock()
}

func (s *memSessionStore) Delete(key string) {
	s.mu.Lock()
	delete(s.m, key)
	s.mu.Unlock()
}

func (s *memSessionStore) reap() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		now := time.Now()
		for k, sess := range s.m {
			if now.After(sess.expires) {
				delete(s.m, k)
			}
		}
		s.mu.Unlock()
	}
}

// --- Redis (shared, HA) -----------------------------------------------------

type redisSessionStore struct {
	c      *redis.Client
	prefix string
}

// sessionWire is the JSON shape persisted in Redis (the session struct's fields
// are unexported, so it can't be marshalled directly).
type sessionWire struct {
	User       User      `json:"user"`
	Expires    time.Time `json:"expires"`
	CSRF       string    `json:"csrf"`
	IDToken    string    `json:"idToken"`
	OAuthState string    `json:"oauthState"`
}

func (r *redisSessionStore) Get(key string) (*session, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, err := r.c.Get(ctx, r.prefix+key).Bytes()
	if err != nil {
		return nil, false
	}
	var w sessionWire
	if json.Unmarshal(b, &w) != nil {
		return nil, false
	}
	if time.Now().After(w.Expires) {
		return nil, false
	}
	return &session{user: w.User, expires: w.Expires, csrf: w.CSRF, idToken: w.IDToken, oauthState: w.OAuthState}, true
}

func (r *redisSessionStore) Put(key string, s *session, ttl time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, err := json.Marshal(sessionWire{
		User: s.user, Expires: s.expires, CSRF: s.csrf, IDToken: s.idToken, OAuthState: s.oauthState,
	})
	if err != nil {
		return
	}
	_ = r.c.Set(ctx, r.prefix+key, b, ttl).Err()
}

func (r *redisSessionStore) Delete(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.c.Del(ctx, r.prefix+key).Err()
}
