package conn

import (
	"testing"

	"verix-dbm/internal/crypto"
	"verix-dbm/internal/store"
)

// The credential-access hook must fire exactly when a stored password is
// decrypted (and not for credential-less connections).
func TestOnCredentialAccess(t *testing.T) {
	box, err := crypto.New("")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := box.Encrypt("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	r := &Registry{box: box, pg: map[int64]*pgEntry{}, redis: map[int64]*redisEntry{}}

	var fired []int64
	r.OnCredentialAccess(func(c store.Connection) { fired = append(fired, c.ID) })

	// Connection with a password: hook fires, plaintext recovered.
	pw, err := r.password(store.Connection{ID: 7, PasswordEnc: enc})
	if err != nil || pw != "s3cret" {
		t.Fatalf("password = %q, %v", pw, err)
	}
	if len(fired) != 1 || fired[0] != 7 {
		t.Fatalf("hook fired = %v, want [7]", fired)
	}

	// Connection without a password: hook must not fire.
	if _, err := r.password(store.Connection{ID: 9}); err != nil {
		t.Fatal(err)
	}
	if len(fired) != 1 {
		t.Errorf("hook should not fire for credential-less connection, fired=%v", fired)
	}
}
