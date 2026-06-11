package store

import (
	"context"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mkConn(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	id, err := s.CreateConnection(context.Background(), Connection{Name: name, Kind: "postgres", Host: "h", Port: 5432})
	if err != nil {
		t.Fatalf("create conn: %v", err)
	}
	return id
}

func TestGrantSetUpsertAndList(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	id := mkConn(t, s, "c1")

	if err := s.SetGrant(ctx, Grant{ConnID: id, Subject: "/team-a", Level: GrantRead}); err != nil {
		t.Fatal(err)
	}
	// Re-setting the same subject updates the level, not a second row.
	if err := s.SetGrant(ctx, Grant{ConnID: id, Subject: "/team-a", Level: GrantWrite}); err != nil {
		t.Fatal(err)
	}
	grants, err := s.ListGrants(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 {
		t.Fatalf("want 1 grant after upsert, got %d", len(grants))
	}
	if grants[0].Level != GrantWrite {
		t.Errorf("want level write after upsert, got %q", grants[0].Level)
	}
}

func TestGrantForSubjectsPicksHighest(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	id := mkConn(t, s, "c1")
	_ = s.SetGrant(ctx, Grant{ConnID: id, Subject: "/team-a", Level: GrantRead})
	_ = s.SetGrant(ctx, Grant{ConnID: id, Subject: "dbm-write", Level: GrantWrite})

	// A user in both subjects gets the highest (write).
	g, err := s.GrantForSubjects(ctx, id, []string{"/team-a", "dbm-write"})
	if err != nil {
		t.Fatal(err)
	}
	if g == nil || g.Level != GrantWrite {
		t.Fatalf("want write grant, got %+v", g)
	}
	// A user matching only the read subject gets read.
	g, _ = s.GrantForSubjects(ctx, id, []string{"/team-a"})
	if g == nil || g.Level != GrantRead {
		t.Fatalf("want read grant, got %+v", g)
	}
	// No matching subject -> nil.
	g, _ = s.GrantForSubjects(ctx, id, []string{"/nobody"})
	if g != nil {
		t.Fatalf("want nil grant, got %+v", g)
	}
	// Empty subject set -> nil, no query.
	if g, _ = s.GrantForSubjects(ctx, id, nil); g != nil {
		t.Fatalf("want nil for empty subjects, got %+v", g)
	}
}

func TestListConnectionsForSubjects(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	a := mkConn(t, s, "a")
	b := mkConn(t, s, "b")
	mkConn(t, s, "c") // ungranted, must not appear

	_ = s.SetGrant(ctx, Grant{ConnID: a, Subject: "/team-a", Level: GrantRead})
	_ = s.SetGrant(ctx, Grant{ConnID: b, Subject: "/team-a", Level: GrantWrite})

	conns, err := s.ListConnectionsForSubjects(ctx, []string{"/team-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 2 {
		t.Fatalf("want 2 visible connections, got %d", len(conns))
	}
	for _, c := range conns {
		if c.ID != a && c.ID != b {
			t.Errorf("unexpected connection %d (%s) visible", c.ID, c.Name)
		}
	}
}

func TestDeleteConnectionCascadesGrants(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	id := mkConn(t, s, "c1")
	_ = s.SetGrant(ctx, Grant{ConnID: id, Subject: "/team-a", Level: GrantRead})

	if err := s.DeleteConnection(ctx, id); err != nil {
		t.Fatal(err)
	}
	grants, err := s.ListGrants(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 0 {
		t.Fatalf("grants should cascade-delete with the connection, got %d", len(grants))
	}
}

func TestDeleteGrantScopedToConnection(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	a := mkConn(t, s, "a")
	b := mkConn(t, s, "b")
	_ = s.SetGrant(ctx, Grant{ConnID: a, Subject: "/team-a", Level: GrantRead})
	_ = s.SetGrant(ctx, Grant{ConnID: b, Subject: "/team-a", Level: GrantRead})
	ga, _ := s.ListGrants(ctx, a)

	// Deleting grant a's id under the wrong connection b must be a no-op.
	if err := s.DeleteGrant(ctx, b, ga[0].ID); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.ListGrants(ctx, a); len(g) != 1 {
		t.Fatalf("grant on a should survive mismatched delete, got %d", len(g))
	}
	// Correct (conn, id) pair removes it.
	if err := s.DeleteGrant(ctx, a, ga[0].ID); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.ListGrants(ctx, a); len(g) != 0 {
		t.Fatalf("grant on a should be deleted, got %d", len(g))
	}
}
