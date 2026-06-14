package web

import (
	"strings"
	"testing"

	"verix-dbm/internal/auth"
)

func TestAuditDetailRedactsSecrets(t *testing.T) {
	cases := []struct{ in, mustNotContain, mustContain string }{
		{`CREATE ROLE app WITH LOGIN PASSWORD 'hunter2'`, "hunter2", "'***'"},
		{`ALTER ROLE app WITH PASSWORD 'p''wd with quote'`, "p''wd", "'***'"},
		{`CREATE USER x IDENTIFIED BY 'topsecret'`, "topsecret", "'***'"},
		{`config set requirepass s3cr3t`, "s3cr3t", "***"},
		{`AUTH myredispass`, "myredispass", "***"},
		// A column literally named password must NOT trigger over-redaction.
		{`SELECT password FROM users WHERE id = 1`, "", "FROM users"},
	}
	for _, c := range cases {
		got := auditDetail(c.in)
		if c.mustNotContain != "" && strings.Contains(got, c.mustNotContain) {
			t.Errorf("auditDetail(%q) = %q, still contains secret %q", c.in, got, c.mustNotContain)
		}
		if !strings.Contains(got, c.mustContain) {
			t.Errorf("auditDetail(%q) = %q, expected to contain %q", c.in, got, c.mustContain)
		}
	}
}

func TestServerSideBlocked(t *testing.T) {
	admin := auth.User{Admin: true}
	reader := auth.User{Read: true}
	writer := auth.User{Write: true, Read: true}

	// Each statement is dangerous on the named engine; the screen is dispatched
	// per engine, so the Postgres primitives and the MySQL primitives differ.
	dangerous := []struct{ kind, sql string }{
		{"postgres", `COPY (SELECT 1) TO PROGRAM 'curl evil'`},
		{"postgres", `SELECT pg_read_file('/etc/passwd')`},
		{"postgres", `select lo_import('/etc/shadow')`},
		{"postgres", `SELECT pg_ls_dir('/')`},
		{"mysql", `LOAD DATA INFILE '/etc/passwd' INTO TABLE t`},
		{"mysql", `LOAD DATA LOCAL INFILE '/etc/passwd' INTO TABLE t`},
		{"mariadb", `SELECT * FROM t INTO OUTFILE '/tmp/x'`},
		{"mysql", `SELECT load_file('/etc/shadow')`},
	}
	for _, d := range dangerous {
		if serverSideBlocked(admin, d.kind, d.sql) {
			t.Errorf("admin should NOT be blocked: %q", d.sql)
		}
		if !serverSideBlocked(reader, d.kind, d.sql) {
			t.Errorf("reader SHOULD be blocked: %q", d.sql)
		}
		if !serverSideBlocked(writer, d.kind, d.sql) {
			t.Errorf("writer SHOULD be blocked: %q", d.sql)
		}
	}

	safe := []struct{ kind, sql string }{
		{"postgres", `SELECT * FROM users`},
		{"postgres", `UPDATE t SET x = 1 WHERE id = 2`},
		{"postgres", `copy_status = 'done'`}, // not a COPY…PROGRAM statement
		{"mysql", `SELECT * FROM users`},
		// pg primitives are not MySQL primitives and vice versa: each is safe on
		// the other engine (the screen is engine-specific).
		{"mysql", `SELECT pg_read_file('/etc/passwd')`},
		{"postgres", `LOAD DATA INFILE '/etc/passwd' INTO TABLE t`},
	}
	for _, sc := range safe {
		if serverSideBlocked(reader, sc.kind, sc.sql) {
			t.Errorf("safe statement wrongly blocked on %s: %q", sc.kind, sc.sql)
		}
	}
}
