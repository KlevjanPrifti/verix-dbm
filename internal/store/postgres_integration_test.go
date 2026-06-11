package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// These exercise the Postgres backend against a real server. They run only when
// DBM_TEST_PG_DSN is set (the rest of the suite uses SQLite), so CI without a
// Postgres stays green. Each run uses a unique schema-less fresh database is not
// required because the tables are created IF NOT EXISTS and the test cleans up
// what it creates.
func pgStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("DBM_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("DBM_TEST_PG_DSN not set; skipping Postgres integration test")
	}
	s, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	// Start clean so repeated runs are deterministic.
	ctx := context.Background()
	for _, tbl := range []string{"connection_grants", "audit", "connections"} {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM "+tbl); err != nil {
			t.Fatalf("clean %s: %v", tbl, err)
		}
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPostgresConnectionsCRUD(t *testing.T) {
	ctx := context.Background()
	s := pgStore(t)

	id, err := s.CreateConnection(ctx, Connection{Name: "pg1", Kind: "postgres", Host: "h", Port: 5432, Username: "u", PasswordEnc: "1$abc", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("CreateConnection returned id 0 (RETURNING id failed)")
	}
	got, err := s.GetConnection(ctx, id)
	if err != nil || got.Name != "pg1" || !got.ReadOnly || got.PasswordEnc != "1$abc" {
		t.Fatalf("GetConnection = %+v, %v", got, err)
	}
	if err := s.UpdatePasswordEnc(ctx, id, "v2$xyz"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetConnection(ctx, id); got.PasswordEnc != "v2$xyz" {
		t.Fatalf("UpdatePasswordEnc not applied: %q", got.PasswordEnc)
	}
	list, err := s.ListConnections(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListConnections = %d rows, %v", len(list), err)
	}
}

func TestPostgresGrantsAndCascade(t *testing.T) {
	ctx := context.Background()
	s := pgStore(t)
	id, _ := s.CreateConnection(ctx, Connection{Name: "pg", Kind: "postgres", Host: "h", Port: 5432})

	if err := s.SetGrant(ctx, Grant{ConnID: id, Subject: "/team", Level: GrantRead}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGrant(ctx, Grant{ConnID: id, Subject: "/team", Level: GrantWrite}); err != nil { // upsert
		t.Fatal(err)
	}
	g, err := s.GrantForSubjects(ctx, id, []string{"/team"})
	if err != nil || g == nil || g.Level != GrantWrite {
		t.Fatalf("GrantForSubjects = %+v, %v (want write after upsert)", g, err)
	}
	vis, err := s.ListConnectionsForSubjects(ctx, []string{"/team"})
	if err != nil || len(vis) != 1 {
		t.Fatalf("ListConnectionsForSubjects = %d, %v", len(vis), err)
	}
	// ON DELETE CASCADE removes grants with the connection.
	if err := s.DeleteConnection(ctx, id); err != nil {
		t.Fatal(err)
	}
	if gs, _ := s.ListGrants(ctx, id); len(gs) != 0 {
		t.Fatalf("grants should cascade, got %d", len(gs))
	}
}

func TestPostgresAudit(t *testing.T) {
	ctx := context.Background()
	s := pgStore(t)

	// "user" is a reserved word in Postgres: this insert/select proves the
	// quoting works on the real engine.
	s.AddAudit(ctx, Audit{User: "alice@x", Action: "pg_query", Detail: "select 1", Success: true})
	rows, err := s.ListAudit(ctx, 10)
	if err != nil || len(rows) != 1 || rows[0].User != "alice@x" {
		t.Fatalf("ListAudit = %+v, %v", rows, err)
	}

	var iterated int
	if err := s.IterAudit(ctx, func(Audit) error { iterated++; return nil }); err != nil {
		t.Fatal(err)
	}
	if iterated != 1 {
		t.Fatalf("IterAudit saw %d rows, want 1", iterated)
	}
	// Purge in the future removes it; RowsAffected must be supported by pgx.
	n, err := s.PurgeAuditOlderThan(ctx, time.Now().Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PurgeAuditOlderThan = %d, %v", n, err)
	}
}
