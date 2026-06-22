package sqlite

import (
	"strings"
	"testing"

	"verix-dbm/internal/dbsql"
)

func TestQuoteIdent(t *testing.T) {
	cases := map[string]string{
		"users":      `"users"`,
		`weird"name`: `"weird""name"`,
		"a.b":        `"a.b"`, // a dot inside one identifier is escaped, not split
	}
	for in, want := range cases {
		if got := quoteIdent(in); got != want {
			t.Errorf("quoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
	if got := qualified("main", "users"); got != `"users"` {
		t.Errorf("qualified(main) should drop the default schema: %q", got)
	}
	if got := qualified("other", "users"); got != `"other"."users"` {
		t.Errorf("qualified = %q", got)
	}
}

func TestQuoteLiteral(t *testing.T) {
	cases := map[string]string{
		"plain":   "'plain'",
		"O'Brien": "'O''Brien'",
		// SQLite does not treat backslash as an escape, so it passes through.
		`back\slash`: `'back\slash'`,
	}
	for in, want := range cases {
		if got := quoteLiteral(in); got != want {
			t.Errorf("quoteLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateIsDelete(t *testing.T) {
	e := &Engine{}
	if got := e.TruncateSQL("main", "users"); got != `DELETE FROM "users"` {
		t.Errorf("TruncateSQL = %q (SQLite has no TRUNCATE)", got)
	}
}

func TestIsServerSideExec(t *testing.T) {
	blocked := []string{
		`ATTACH DATABASE '/etc/x.db' AS evil`,
		`SELECT load_extension('/tmp/evil.so')`,
		`VACUUM main INTO '/tmp/dump.db'`,
		`SELECT writefile('/tmp/x', 'data')`,
		`select readfile ('/etc/passwd')`,
	}
	for _, s := range blocked {
		if !IsServerSideExec(s) {
			t.Errorf("expected blocked: %q", s)
		}
	}
	safe := []string{
		`SELECT * FROM users`,
		`UPDATE t SET x = 1 WHERE id = 2`,
		`SELECT 'attachment' AS note`, // word inside a literal, not ATTACH
		`SELECT pg_read_file('/etc/passwd')`,
	}
	for _, s := range safe {
		if IsServerSideExec(s) {
			t.Errorf("expected safe: %q", s)
		}
	}
}

func TestFormSQLSupported(t *testing.T) {
	e := &Engine{}
	add, action, err := e.FormSQL(dbsql.FormSpec{
		Kind: "add-column", Schema: "main", Table: "users", Name: "age", Type: "INTEGER", Nullable: false, Default: "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if action != "sqlite_ddl_add_column" || len(add) != 1 {
		t.Fatalf("action=%q stmts=%v", action, add)
	}
	want := `ALTER TABLE "users" ADD COLUMN "age" INTEGER NOT NULL DEFAULT 0`
	if add[0] != want {
		t.Errorf("got %q, want %q", add[0], want)
	}

	idx, _, err := e.FormSQL(dbsql.FormSpec{Kind: "new-index", Schema: "main", Table: "users", Name: "idx_age", Columns: "age", Unique: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(idx[0], `CREATE UNIQUE INDEX "idx_age" ON "users" (age)`) {
		t.Errorf("new-index = %q", idx[0])
	}
}

func TestFormSQLUnsupported(t *testing.T) {
	e := &Engine{}
	for _, kind := range []string{"modify-column", "new-schema", "create-user"} {
		if _, _, err := e.FormSQL(dbsql.FormSpec{Kind: kind, Schema: "main", Table: "users", Type: "TEXT", Name: "x"}); err == nil {
			t.Errorf("kind %q should be rejected on SQLite", kind)
		}
	}
}
