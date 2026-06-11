package auth

import (
	"context"
	"os"
	"testing"
	"time"

	"verix-dbm/internal/config"
)

func TestMemSessionStore(t *testing.T) {
	st := newMemSessionStore()
	st.Put("k", &session{user: User{Email: "a@x"}, csrf: "tok", expires: time.Now().Add(time.Hour)}, time.Hour)

	got, ok := st.Get("k")
	if !ok || got.user.Email != "a@x" || got.csrf != "tok" {
		t.Fatalf("Get = %+v, %v", got, ok)
	}
	st.Delete("k")
	if _, ok := st.Get("k"); ok {
		t.Error("session should be gone after Delete")
	}

	// Expired sessions are not returned.
	st.Put("old", &session{expires: time.Now().Add(-time.Minute)}, time.Hour)
	if _, ok := st.Get("old"); ok {
		t.Error("expired session should not be returned")
	}
}

func TestNewSessionStoreSelection(t *testing.T) {
	ctx := context.Background()
	if _, err := newSessionStore(ctx, &config.Config{SessionBackend: "memory"}); err != nil {
		t.Errorf("memory backend: %v", err)
	}
	if _, err := newSessionStore(ctx, &config.Config{SessionBackend: ""}); err != nil {
		t.Errorf("default backend: %v", err)
	}
	if _, err := newSessionStore(ctx, &config.Config{SessionBackend: "nope"}); err == nil {
		t.Error("unknown backend should error")
	}
}

// Gated on a live Redis (set DBM_TEST_REDIS_URL, e.g. redis://127.0.0.1:6379/0).
func TestRedisSessionStoreRoundTrip(t *testing.T) {
	url := os.Getenv("DBM_TEST_REDIS_URL")
	if url == "" {
		t.Skip("DBM_TEST_REDIS_URL not set; skipping Redis session test")
	}
	st, err := newSessionStore(context.Background(), &config.Config{SessionBackend: "redis", SessionRedisURL: url})
	if err != nil {
		t.Fatalf("newSessionStore(redis): %v", err)
	}
	sess := &session{user: User{Email: "r@x", Admin: true}, csrf: "csrf1", idToken: "idtok", expires: time.Now().Add(time.Hour)}
	st.Put("rk", sess, time.Hour)

	got, ok := st.Get("rk")
	if !ok {
		t.Fatal("session not found in Redis")
	}
	if got.user.Email != "r@x" || !got.user.Admin || got.csrf != "csrf1" || got.idToken != "idtok" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	st.Delete("rk")
	if _, ok := st.Get("rk"); ok {
		t.Error("session should be gone after Delete")
	}
}
