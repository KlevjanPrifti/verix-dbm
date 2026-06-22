package dbsql

import "testing"

func TestNeedsConfirm(t *testing.T) {
	confirm := []string{
		"DROP TABLE users",
		"  truncate table t",
		"DELETE FROM t",            // no WHERE
		"UPDATE t SET x = 1",       // no WHERE
		"/* note */ DROP TABLE t",  // leading block comment
		"-- note\nDROP TABLE t",    // leading line comment
		"SELECT 1; DROP TABLE t",   // destructive hidden after a harmless stmt
		"SELECT 1; DELETE FROM t",  // unguarded delete in second stmt
		"DELETE FROM t -- where x", // trailing comment must not fake a WHERE
	}
	for _, s := range confirm {
		if !NeedsConfirm(s) {
			t.Errorf("NeedsConfirm(%q) = false, want true", s)
		}
	}

	noConfirm := []string{
		"SELECT * FROM t",
		"DELETE FROM t WHERE id = 1",
		"UPDATE t SET x = 1 WHERE id = 2",
		"INSERT INTO t VALUES (1)",
		"",
	}
	for _, s := range noConfirm {
		if NeedsConfirm(s) {
			t.Errorf("NeedsConfirm(%q) = true, want false", s)
		}
	}
}
