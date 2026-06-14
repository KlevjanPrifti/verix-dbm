package mysql

import (
	"strings"
	"testing"

	"verix-dbm/internal/dbsql"
)

func TestQuoteIdent(t *testing.T) {
	cases := map[string]string{
		"users":      "`users`",
		"weird`name": "`weird``name`",
		"a.b":        "`a.b`", // a dot inside one identifier is escaped, not split
	}
	for in, want := range cases {
		if got := quoteIdent(in); got != want {
			t.Errorf("quoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
	if got := qualified("app", "users"); got != "`app`.`users`" {
		t.Errorf("qualified = %q", got)
	}
	if got := qualified("", "users"); got != "`users`" {
		t.Errorf("qualified(no schema) = %q", got)
	}
}

func TestQuoteLiteral(t *testing.T) {
	cases := map[string]string{
		"plain":      "'plain'",
		"O'Brien":    "'O''Brien'",
		`back\slash`: `'back\\slash'`,
	}
	for in, want := range cases {
		if got := quoteLiteral(in); got != want {
			t.Errorf("quoteLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsServerSideExec(t *testing.T) {
	blocked := []string{
		`LOAD DATA INFILE '/etc/passwd' INTO TABLE t`,
		`LOAD DATA LOCAL INFILE '/etc/passwd' INTO TABLE t`,
		`SELECT * FROM t INTO OUTFILE '/tmp/x'`,
		`SELECT a FROM t INTO DUMPFILE '/tmp/x'`,
		`SELECT load_file('/etc/shadow')`,
		`select LOAD_FILE ('/etc/shadow')`,
	}
	for _, s := range blocked {
		if !IsServerSideExec(s) {
			t.Errorf("expected blocked: %q", s)
		}
	}
	safe := []string{
		`SELECT * FROM users`,
		`UPDATE t SET x = 1 WHERE id = 2`,
		`SELECT 'infile' AS note`,            // word in a literal, not LOAD DATA ... INFILE
		`SELECT pg_read_file('/etc/passwd')`, // a Postgres primitive, not a MySQL one
	}
	for _, s := range safe {
		if IsServerSideExec(s) {
			t.Errorf("expected safe: %q", s)
		}
	}
}

func TestFormSQLAddColumn(t *testing.T) {
	e := &Engine{}
	stmts, action, err := e.FormSQL(dbsql.FormSpec{
		Kind: "add-column", Schema: "app", Table: "users", Name: "age", Type: "int", Nullable: false, Default: "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if action != "mysql_ddl_add_column" || len(stmts) != 1 {
		t.Fatalf("action=%q stmts=%v", action, stmts)
	}
	want := "ALTER TABLE `app`.`users` ADD COLUMN `age` int NOT NULL DEFAULT 0"
	if stmts[0] != want {
		t.Errorf("got %q, want %q", stmts[0], want)
	}
}

func TestFormSQLModifyColumnUsesMODIFY(t *testing.T) {
	e := &Engine{}
	stmts, _, err := e.FormSQL(dbsql.FormSpec{
		Kind: "modify-column", Schema: "app", Table: "users", Column: "name", Type: "varchar(255)", Nullable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// MySQL uses MODIFY COLUMN, not Postgres's ALTER COLUMN ... TYPE.
	if !strings.Contains(stmts[0], "MODIFY COLUMN `name` varchar(255)") || strings.Contains(stmts[0], "TYPE") {
		t.Errorf("unexpected modify-column SQL: %q", stmts[0])
	}
}

func TestFormSQLCreateUserIsMultiStatement(t *testing.T) {
	e := &Engine{}
	stmts, action, err := e.FormSQL(dbsql.FormSpec{
		Kind: "create-user", Name: "app",
		Role: dbsql.RoleAttrs{Password: "pw", Host: "10.0.0.%", Login: false, Super: true, CreateDB: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if action != "mysql_ddl_create_user" {
		t.Fatalf("action=%q", action)
	}
	// CREATE USER + ACCOUNT LOCK (login=false) + GRANT SUPER + GRANT CREATE = 4.
	if len(stmts) != 4 {
		t.Fatalf("expected 4 statements, got %d: %v", len(stmts), stmts)
	}
	if !strings.HasPrefix(stmts[0], "CREATE USER 'app'@'10.0.0.%' IDENTIFIED BY 'pw'") {
		t.Errorf("create stmt: %q", stmts[0])
	}
	if !strings.Contains(stmts[1], "ACCOUNT LOCK") {
		t.Errorf("expected ACCOUNT LOCK: %q", stmts[1])
	}
	if !strings.Contains(strings.Join(stmts, "\n"), "GRANT SUPER ON *.* TO 'app'@'10.0.0.%'") {
		t.Errorf("missing SUPER grant: %v", stmts)
	}
}

func TestAlterUserSQLAdditiveAndHost(t *testing.T) {
	e := &Engine{}
	stmts := e.AlterUserSQL("app", "", dbsql.RoleAttrs{Login: true, Password: "new", Host: ""})
	joined := strings.Join(stmts, "\n")
	// Default host is %, password change emitted, account unlocked (login=true).
	if !strings.Contains(joined, "ALTER USER 'app'@'%' IDENTIFIED BY 'new'") {
		t.Errorf("missing password change: %v", stmts)
	}
	if !strings.Contains(joined, "ACCOUNT UNLOCK") {
		t.Errorf("missing unlock: %v", stmts)
	}
	// No REVOKE: the editor is additive so it never strips unrelated grants.
	if strings.Contains(strings.ToUpper(joined), "REVOKE") {
		t.Errorf("alter must not REVOKE: %v", stmts)
	}
}

func TestDropIndexIsTableQualified(t *testing.T) {
	e := &Engine{}
	if got := e.DropIndexSQL("app", "users", "idx_email"); got != "ALTER TABLE `app`.`users` DROP INDEX `idx_email`" {
		t.Errorf("DropIndexSQL = %q", got)
	}
}
