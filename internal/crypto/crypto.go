// Package crypto encrypts connection credentials at rest with AES-256-GCM.
//
// Keys are versioned so they can be rotated without downtime. A ciphertext is
// stored as "<keyID>$<base64(nonce||ct)>"; the keyID names which key sealed it.
// The Box (a keyring) holds a primary key used for new writes plus any number of
// older keys retained only to decrypt data not yet re-encrypted. Ciphertext
// written before versioning has no "<id>$" prefix; it is decrypted by trying
// each key in turn (GCM authentication makes a wrong-key attempt fail cleanly),
// so upgrading is seamless and rotation is non-destructive.
package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// KeySpec is one named 32-byte key.
type KeySpec struct {
	ID       string
	Material []byte // must be 32 bytes
}

// Provider supplies the keyring's material. The built-in StaticProvider reads
// keys from configuration; an external provider (HashiCorp Vault, AWS KMS, ...)
// can implement this to fetch or unwrap keys at startup without the rest of the
// app changing.
type Provider interface {
	Keys(ctx context.Context) (primaryID string, specs []KeySpec, err error)
}

// StaticProvider serves a fixed set of keys (the env-configured default).
type StaticProvider struct {
	Primary string
	Specs   []KeySpec
}

func (p StaticProvider) Keys(context.Context) (string, []KeySpec, error) {
	return p.Primary, p.Specs, nil
}

// Box is a keyring: a primary key for encryption plus all keys for decryption.
type Box struct {
	primaryID string
	keys      map[string]cipher.AEAD
	order     []string // primary first; the try order for legacy unprefixed data
}

// New builds a single-key Box from a 32-byte key supplied as 64-char hex or
// base64. An empty key generates an ephemeral one (credentials won't survive a
// restart). The key is given the id "1", so new writes are "1$...". This is the
// backward-compatible path: data written by older builds (unprefixed) still
// decrypts, because the lone key is tried for legacy ciphertext.
func New(key string) (*Box, error) {
	var raw []byte
	if key == "" {
		raw = make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		slog.Warn("crypto: DBM_ENC_KEY not set; using an EPHEMERAL key, stored credentials will be unreadable after restart")
	} else {
		b, err := decodeKey(key)
		if err != nil {
			return nil, fmt.Errorf("DBM_ENC_KEY: %w", err)
		}
		raw = b
	}
	return NewFromProvider(context.Background(), StaticProvider{Primary: "1", Specs: []KeySpec{{ID: "1", Material: raw}}})
}

// ParseKeyring builds a Box from the two env forms. multi (DBM_ENC_KEYS), when
// set, defines the whole ring as "id:key,id:key,..." with the first entry the
// primary (used for new writes); the rest are kept for decryption during
// rotation. When multi is empty it falls back to the single-key form.
func ParseKeyring(single, multi string) (*Box, error) {
	if strings.TrimSpace(multi) == "" {
		return New(single)
	}
	primary, specs, err := parseMultiKeys(multi)
	if err != nil {
		return nil, err
	}
	return NewFromProvider(context.Background(), StaticProvider{Primary: primary, Specs: specs})
}

// NewFromProvider assembles a Box from any key Provider.
func NewFromProvider(ctx context.Context, p Provider) (*Box, error) {
	primaryID, specs, err := p.Keys(ctx)
	if err != nil {
		return nil, err
	}
	if len(specs) == 0 {
		return nil, errors.New("crypto: no keys provided")
	}
	b := &Box{primaryID: primaryID, keys: make(map[string]cipher.AEAD, len(specs))}
	for _, s := range specs {
		if s.ID == "" || strings.ContainsRune(s.ID, '$') {
			return nil, fmt.Errorf("crypto: invalid key id %q (must be non-empty and contain no '$')", s.ID)
		}
		if len(s.Material) != 32 {
			return nil, fmt.Errorf("crypto: key %q must be 32 bytes, got %d", s.ID, len(s.Material))
		}
		if _, dup := b.keys[s.ID]; dup {
			return nil, fmt.Errorf("crypto: duplicate key id %q", s.ID)
		}
		aead, err := newAEAD(s.Material)
		if err != nil {
			return nil, err
		}
		b.keys[s.ID] = aead
	}
	if _, ok := b.keys[primaryID]; !ok {
		return nil, fmt.Errorf("crypto: primary key id %q not among provided keys", primaryID)
	}
	// Deterministic try order for legacy decryption: primary first, then the rest.
	b.order = append(b.order, primaryID)
	for _, s := range specs {
		if s.ID != primaryID {
			b.order = append(b.order, s.ID)
		}
	}
	return b, nil
}

// PrimaryID is the id of the key new ciphertext is written under.
func (b *Box) PrimaryID() string { return b.primaryID }

// Encrypt seals plaintext under the primary key and returns "<primaryID>$<b64>".
func (b *Box) Encrypt(plaintext string) (string, error) {
	payload, err := seal(b.keys[b.primaryID], plaintext)
	if err != nil {
		return "", err
	}
	return b.primaryID + "$" + payload, nil
}

// Decrypt reverses Encrypt. Prefixed ciphertext is opened with the named key;
// unprefixed (pre-versioning) ciphertext is tried against every key.
func (b *Box) Decrypt(enc string) (string, error) {
	if i := strings.IndexByte(enc, '$'); i >= 0 {
		id, payload := enc[:i], enc[i+1:]
		aead, ok := b.keys[id]
		if !ok {
			return "", fmt.Errorf("crypto: no key %q for ciphertext (rotated out?)", id)
		}
		return open(aead, payload)
	}
	for _, id := range b.order {
		if pt, err := open(b.keys[id], enc); err == nil {
			return pt, nil
		}
	}
	return "", errors.New("crypto: no key could decrypt legacy ciphertext")
}

// Reencrypt decrypts enc and re-seals it under the primary key. changed is false
// when enc is already sealed under the primary key (no rewrite needed), letting
// callers skip a no-op store write.
func (b *Box) Reencrypt(enc string) (newEnc string, changed bool, err error) {
	if i := strings.IndexByte(enc, '$'); i >= 0 && enc[:i] == b.primaryID {
		return enc, false, nil
	}
	pt, err := b.Decrypt(enc)
	if err != nil {
		return "", false, err
	}
	n, err := b.Encrypt(pt)
	if err != nil {
		return "", false, err
	}
	return n, true, nil
}

// --- helpers ----------------------------------------------------------------

func newAEAD(raw []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func seal(aead cipher.AEAD, plaintext string) (string, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

func open(aead cipher.AEAD, payload string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", err
	}
	ns := aead.NonceSize()
	if len(data) < ns {
		return "", errors.New("ciphertext too short")
	}
	pt, err := aead.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// decodeKey accepts a 32-byte key as 64-char hex or base64. Decoding is by
// content, not length: a 64-char string is no longer assumed to be hex (some
// 64-char base64 strings are also valid hex and would silently decode to the
// wrong 32 bytes). We try hex first, then base64, and accept the first that
// yields exactly 32 bytes.
func decodeKey(key string) ([]byte, error) {
	if b, err := hex.DecodeString(key); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(key); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, errors.New("encryption key must be 64-char hex or base64 of exactly 32 bytes")
}

// parseMultiKeys parses "id:key,id:key,..." into specs (preserving order) and
// returns the first id as the primary.
func parseMultiKeys(multi string) (primary string, specs []KeySpec, err error) {
	seen := map[string]bool{}
	for _, entry := range strings.Split(multi, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, key, ok := strings.Cut(entry, ":")
		id = strings.TrimSpace(id)
		if !ok || id == "" {
			return "", nil, fmt.Errorf("DBM_ENC_KEYS: entry %q must be id:key", entry)
		}
		if seen[id] {
			return "", nil, fmt.Errorf("DBM_ENC_KEYS: duplicate key id %q", id)
		}
		raw, err := decodeKey(strings.TrimSpace(key))
		if err != nil {
			return "", nil, fmt.Errorf("DBM_ENC_KEYS key %q: %w", id, err)
		}
		seen[id] = true
		specs = append(specs, KeySpec{ID: id, Material: raw})
	}
	if len(specs) == 0 {
		return "", nil, errors.New("DBM_ENC_KEYS: no valid entries")
	}
	return specs[0].ID, specs, nil
}
