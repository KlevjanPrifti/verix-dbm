package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSQLitePathContainment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A file inside the allow-dir resolves to its absolute path.
	got, err := ResolveSQLitePath(dir, filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if !strings.HasSuffix(got, "app.db") {
		t.Errorf("resolved = %q", got)
	}

	// A not-yet-created file inside the allow-dir is still allowed (it can be created).
	if _, err := ResolveSQLitePath(dir, filepath.Join(dir, "new.db")); err != nil {
		t.Errorf("new file inside dir should be allowed: %v", err)
	}
}

func TestResolveSQLitePathRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	bad := []string{
		"/etc/passwd",
		filepath.Join(dir, "..", "escape.db"),
		filepath.Join(dir, "..", filepath.Base(dir)+"extra", "x.db"), // sibling prefix, must not pass
	}
	for _, p := range bad {
		if _, err := ResolveSQLitePath(dir, p); err == nil {
			t.Errorf("path %q should be rejected", p)
		}
	}
}

func TestResolveSQLitePathDisabledWhenNoDir(t *testing.T) {
	if _, err := ResolveSQLitePath("", "/tmp/x.db"); err == nil {
		t.Error("empty allow-dir must disable sqlite (fail closed)")
	}
	if _, err := ResolveSQLitePath("   ", "/tmp/x.db"); err == nil {
		t.Error("blank allow-dir must disable sqlite (fail closed)")
	}
}

func TestResolveSQLitePathRejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.db")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	// The symlink lives inside the allow-dir but points outside it; resolving the
	// link must catch the escape.
	if _, err := ResolveSQLitePath(dir, link); err == nil {
		t.Error("symlink escaping the allow-dir must be rejected")
	}
}

// A symlink planted on an intermediate directory must be caught even when the
// final target file does not exist yet (SQLite would create it on first open).
func TestResolveSQLitePathRejectsIntermediateSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(dir, "link") // dir/link -> /some/outside/dir
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	// dir/link/new.db does not exist; it would resolve to outside/new.db.
	if _, err := ResolveSQLitePath(dir, filepath.Join(link, "new.db")); err == nil {
		t.Error("non-existent path through an escaping intermediate symlink must be rejected")
	}
}

func TestSQLiteDSN(t *testing.T) {
	dsn := SQLiteDSN("/data/app.db")
	if !strings.HasPrefix(dsn, "/data/app.db?") || !strings.Contains(dsn, "foreign_keys(1)") {
		t.Errorf("dsn = %q", dsn)
	}
}

func TestDSNMongo(t *testing.T) {
	// Credentials are URL-encoded so special characters can't break the URI.
	c := Connection{Kind: "mongodb", Host: "db.example.com", Port: 27017, DBName: "app", Username: "svc", Options: "replicaSet=rs0"}
	got := c.DSNMongo("p@ss/word")
	if !strings.HasPrefix(got, "mongodb://svc:") || !strings.Contains(got, "@db.example.com:27017/app") {
		t.Errorf("DSNMongo = %q", got)
	}
	if !strings.Contains(got, "p%40ss%2Fword") {
		t.Errorf("password must be URL-encoded: %q", got)
	}
	if !strings.HasSuffix(got, "?replicaSet=rs0") {
		t.Errorf("options should be the query string: %q", got)
	}
	// No credentials => no userinfo "@".
	anon := Connection{Kind: "mongodb", Host: "localhost", Port: 27017, DBName: ""}
	if got := anon.DSNMongo(""); got != "mongodb://localhost:27017/" {
		t.Errorf("anonymous DSNMongo = %q", got)
	}
}
