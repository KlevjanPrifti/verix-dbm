package crypto

import (
	"encoding/hex"
	"strings"
	"testing"
)

func hexKey(b byte) string { return strings.Repeat(hex.EncodeToString([]byte{b}), 32) }

func TestRoundTripSingleKey(t *testing.T) {
	b, err := New(hexKey(0x11))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := b.Encrypt("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "1$") {
		t.Errorf("want '1$' prefix, got %q", enc)
	}
	pt, err := b.Decrypt(enc)
	if err != nil || pt != "s3cret" {
		t.Fatalf("decrypt = %q, %v", pt, err)
	}
}

// Legacy (unprefixed) ciphertext written by an older build must still decrypt:
// the keyring tries each key for prefix-less input.
func TestDecryptLegacyUnprefixed(t *testing.T) {
	key := hexKey(0x22)
	b, _ := New(key)

	// Simulate old format: base64(nonce||ct) with no "id$" prefix.
	legacy, err := seal(b.keys["1"], "old-secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(legacy, "$") {
		t.Fatal("legacy payload should have no prefix")
	}
	pt, err := b.Decrypt(legacy)
	if err != nil || pt != "old-secret" {
		t.Fatalf("legacy decrypt = %q, %v", pt, err)
	}
}

func TestRotationDecryptsOldAndWritesNew(t *testing.T) {
	old := hexKey(0xAA)
	// Old deployment encrypts under single key (id "1").
	oldBox, _ := New(old)
	oldEnc, _ := oldBox.Encrypt("pw")

	// Rotate: new primary v2, old key retained as v1.
	ring, err := ParseKeyring("", "v2:"+hexKey(0xBB)+",1:"+old)
	if err != nil {
		t.Fatal(err)
	}
	if ring.PrimaryID() != "v2" {
		t.Fatalf("primary = %q, want v2", ring.PrimaryID())
	}
	// Old ciphertext (id "1") still decrypts.
	if pt, err := ring.Decrypt(oldEnc); err != nil || pt != "pw" {
		t.Fatalf("decrypt old under rotated ring = %q, %v", pt, err)
	}
	// New writes use the primary.
	newEnc, _ := ring.Encrypt("pw2")
	if !strings.HasPrefix(newEnc, "v2$") {
		t.Errorf("new write prefix = %q, want v2$", newEnc)
	}
}

func TestReencryptMovesToPrimary(t *testing.T) {
	old := hexKey(0xAA)
	oldBox, _ := New(old)
	oldEnc, _ := oldBox.Encrypt("pw") // "1$..."

	ring, _ := ParseKeyring("", "v2:"+hexKey(0xBB)+",1:"+old)

	newEnc, changed, err := ring.Reencrypt(oldEnc)
	if err != nil || !changed {
		t.Fatalf("reencrypt changed=%v err=%v", changed, err)
	}
	if !strings.HasPrefix(newEnc, "v2$") {
		t.Errorf("reencrypted prefix = %q, want v2$", newEnc)
	}
	if pt, _ := ring.Decrypt(newEnc); pt != "pw" {
		t.Errorf("reencrypted plaintext changed: %q", pt)
	}
	// Re-running is a no-op once already on the primary.
	if _, changed, _ := ring.Reencrypt(newEnc); changed {
		t.Error("second reencrypt should report changed=false")
	}
}

func TestDecryptUnknownKeyID(t *testing.T) {
	b, _ := New(hexKey(0x33))
	if _, err := b.Decrypt("v9$AAAA"); err == nil {
		t.Error("decrypt with unknown key id should fail")
	}
}

func TestParseMultiKeysErrors(t *testing.T) {
	for _, bad := range []string{"", "noColon", "id:", "a:" + hexKey(1) + ",a:" + hexKey(2)} {
		if _, err := ParseKeyring("", bad); err == nil && bad != "" {
			t.Errorf("ParseKeyring(%q) should error", bad)
		}
	}
}
