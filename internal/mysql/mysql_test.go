package mysql

import (
	"strings"
	"testing"

	"verix-dbm/internal/dbsql"
)

func TestReturnsRows(t *testing.T) {
	rows := []string{
		"SELECT 1", "select * from t", "  SHOW TABLES", "DESCRIBE t", "EXPLAIN SELECT 1",
		"WITH x AS (SELECT 1) SELECT * FROM x", "(SELECT 1)",
	}
	for _, s := range rows {
		if !returnsRows(s) {
			t.Errorf("expected returnsRows true: %q", s)
		}
	}
	noRows := []string{"INSERT INTO t VALUES (1)", "UPDATE t SET x=1", "DELETE FROM t", "CREATE TABLE t (a int)", "DROP TABLE t"}
	for _, s := range noRows {
		if returnsRows(s) {
			t.Errorf("expected returnsRows false: %q", s)
		}
	}
}

func TestWithMaxExecHint(t *testing.T) {
	got := withMaxExecHint("SELECT * FROM t")
	if !strings.Contains(got, "MAX_EXECUTION_TIME(30000)") || !strings.HasPrefix(got, "SELECT /*+") {
		t.Errorf("hint not injected: %q", got)
	}
	// Non-SELECT statements are returned unchanged (the hint is SELECT-only).
	if got := withMaxExecHint("SHOW TABLES"); got != "SHOW TABLES" {
		t.Errorf("SHOW should be unchanged: %q", got)
	}
	if got := withMaxExecHint("UPDATE t SET x=1"); got != "UPDATE t SET x=1" {
		t.Errorf("UPDATE should be unchanged: %q", got)
	}
}

func TestReconstructIndexDef(t *testing.T) {
	pk := reconstructIndexDef("users", dbsql.Index{Name: "PRIMARY", Primary: true, Cols: "id"})
	if pk != "PRIMARY KEY (id)" {
		t.Errorf("pk def = %q", pk)
	}
	uniq := reconstructIndexDef("users", dbsql.Index{Name: "idx_email", Unique: true, Cols: "email"})
	if uniq != "CREATE UNIQUE INDEX `idx_email` ON `users` (email)" {
		t.Errorf("unique def = %q", uniq)
	}
}

func TestColumnCatAndTypeText(t *testing.T) {
	// dbsql.Column heuristics must classify MySQL type spellings correctly.
	if c := (dbsql.Column{Type: "int(10) unsigned"}); c.Cat() != "num" {
		t.Errorf("int cat = %q", c.Cat())
	}
	if c := (dbsql.Column{Type: "varchar(255)"}); c.Cat() != "text" {
		t.Errorf("varchar cat = %q", c.Cat())
	}
	if c := (dbsql.Column{Type: "datetime"}); c.Cat() != "time" {
		t.Errorf("datetime cat = %q", c.Cat())
	}
	// AutoInc suffix comes from the flag, not the type string.
	c := dbsql.Column{Type: "bigint", AutoInc: true}
	if !strings.Contains(c.TypeText(), "auto increment") {
		t.Errorf("type text = %q", c.TypeText())
	}
}
