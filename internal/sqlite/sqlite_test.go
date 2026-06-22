package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"

	"verix-dbm/internal/dbsql"
)

// newTestEngine creates a temp SQLite file with a small schema and returns a
// ready Engine. MaxOpenConns(1) forces connection reuse so the read-only guard's
// reset (PRAGMA query_only=OFF) is actually exercised across calls.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	schema := []string{
		`CREATE TABLE authors (
			id    INTEGER PRIMARY KEY,
			name  TEXT NOT NULL,
			email TEXT UNIQUE
		)`,
		`CREATE TABLE books (
			id        INTEGER PRIMARY KEY,
			author_id INTEGER NOT NULL REFERENCES authors(id),
			title     TEXT NOT NULL
		)`,
		`CREATE INDEX idx_books_title ON books(title)`,
		`CREATE VIEW author_names AS SELECT name FROM authors`,
		`INSERT INTO authors (id, name, email) VALUES (1, 'Ursula', 'u@example.com')`,
		`INSERT INTO books (id, author_id, title) VALUES (1, 1, 'A Wizard of Earthsea')`,
	}
	for _, s := range schema {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	return New(db)
}

func TestSchemas(t *testing.T) {
	e := newTestEngine(t)
	ss, err := e.Schemas(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].Name != "main" {
		t.Fatalf("expected single 'main' schema, got %+v", ss)
	}
	got := map[string]string{}
	for _, tb := range ss[0].Tables {
		got[tb.Name] = tb.Kind
	}
	if got["authors"] != "table" || got["books"] != "table" {
		t.Errorf("tables = %v", got)
	}
	if got["author_names"] != "view" {
		t.Errorf("expected author_names view, got %v", got)
	}
	if _, ok := got["sqlite_sequence"]; ok {
		t.Errorf("internal sqlite_ tables must be hidden: %v", got)
	}
}

func TestColumns(t *testing.T) {
	e := newTestEngine(t)
	cols, err := e.Columns(context.Background(), "main", "authors")
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 3 {
		t.Fatalf("expected 3 columns, got %d: %+v", len(cols), cols)
	}
	id := cols[0]
	if id.Name != "id" || !id.PK || !id.AutoInc {
		t.Errorf("id column = %+v (want pk+autoinc)", id)
	}
	name := cols[1]
	if name.Name != "name" || !name.NotNull {
		t.Errorf("name column = %+v (want not null)", name)
	}
}

func TestKeys(t *testing.T) {
	e := newTestEngine(t)
	authorKeys, err := e.Keys(context.Background(), "main", "authors")
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]string{}
	for _, k := range authorKeys {
		types[k.Type] = k.Cols
	}
	if types["primary"] != "id" {
		t.Errorf("authors primary key cols = %q", types["primary"])
	}
	if types["unique"] != "email" {
		t.Errorf("authors unique cols = %q", types["unique"])
	}

	bookKeys, err := e.Keys(context.Background(), "main", "books")
	if err != nil {
		t.Fatal(err)
	}
	var fk *dbsql.Key
	for i := range bookKeys {
		if bookKeys[i].Type == "foreign" {
			fk = &bookKeys[i]
		}
	}
	if fk == nil {
		t.Fatalf("books should have a foreign key: %+v", bookKeys)
	}
	if fk.Cols != "author_id" || !strings.Contains(fk.Def, `REFERENCES "authors" (id)`) {
		t.Errorf("books FK = %+v", fk)
	}
}

func TestIndexes(t *testing.T) {
	e := newTestEngine(t)
	ix, err := e.Indexes(context.Background(), "main", "books")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, i := range ix {
		if i.Name == "idx_books_title" {
			found = true
			if i.Cols != "title" {
				t.Errorf("idx cols = %q", i.Cols)
			}
			if !strings.Contains(i.Def, "CREATE INDEX") {
				t.Errorf("idx def should be the stored CREATE INDEX: %q", i.Def)
			}
		}
	}
	if !found {
		t.Errorf("idx_books_title not found in %+v", ix)
	}
}

func TestFindUsages(t *testing.T) {
	e := newTestEngine(t)
	u, err := e.FindUsages(context.Background(), "main", "authors")
	if err != nil {
		t.Fatal(err)
	}
	if len(u) != 1 || u[0].Table != "books" || !strings.Contains(u[0].Def, "author_id") {
		t.Fatalf("expected books->authors usage, got %+v", u)
	}
}

func TestCreateTableDDL(t *testing.T) {
	e := newTestEngine(t)
	ddl, err := e.CreateTableDDL(context.Background(), "main", "authors")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ddl, "CREATE TABLE authors") || !strings.HasSuffix(ddl, ";") {
		t.Errorf("ddl = %q", ddl)
	}
}

func TestQuerySelect(t *testing.T) {
	e := newTestEngine(t)
	res, err := e.Query(context.Background(), "SELECT id, name FROM authors ORDER BY id", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsSelect || len(res.Rows) != 1 || res.Rows[0][1] != "Ursula" {
		t.Errorf("unexpected result: %+v", res)
	}
}

// TestReadOnlyGuard verifies a read-only call blocks writes and that the pooled
// connection is reset afterward, so a later write succeeds on the same conn.
func TestReadOnlyGuard(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	if _, err := e.Query(ctx, "INSERT INTO authors (id, name) VALUES (2, 'blocked')", true, ""); err == nil {
		t.Fatal("expected read-only call to reject the INSERT")
	}
	// The same single pooled connection must be writable again (query_only reset).
	res, err := e.Query(ctx, "INSERT INTO authors (id, name) VALUES (3, 'allowed')", false, "")
	if err != nil {
		t.Fatalf("write after read-only call failed (query_only leaked?): %v", err)
	}
	if res.RowsAffected != 1 {
		t.Errorf("rows affected = %d", res.RowsAffected)
	}
}

func TestBrowseWhere(t *testing.T) {
	e := newTestEngine(t)
	res, err := e.BrowseWhere(context.Background(), "main", "authors", "name = 'Ursula'", "id", 100, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(res.Rows))
	}
}

// TestExecScriptAtomic confirms a failed batch rolls back fully (SQLite DDL is
// transactional, unlike MySQL).
func TestExecScriptAtomic(t *testing.T) {
	e := newTestEngine(t)
	err := e.ExecScript(context.Background(), []string{
		`CREATE TABLE t1 (a int)`,
		`CREATE TABLE t1 (a int)`, // duplicate => fails, must roll back the first
	})
	if err == nil {
		t.Fatal("expected the batch to fail")
	}
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='t1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("t1 should not exist after rollback, count=%d", n)
	}
}

func TestRolesEmpty(t *testing.T) {
	e := newTestEngine(t)
	roles, err := e.Roles(context.Background())
	if err != nil || roles != nil {
		t.Errorf("Roles = %v, %v (want nil, nil)", roles, err)
	}
}

func TestDatabaseName(t *testing.T) {
	e := newTestEngine(t)
	name, err := e.DatabaseName(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if name != "test.db" {
		t.Errorf("DatabaseName = %q, want test.db", name)
	}
}
