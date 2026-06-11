package store

import (
	"context"
	"testing"
	"time"
)

func TestPing(t *testing.T) {
	s := testStore(t)
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestAuditSinkFires(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	var got []Audit
	s.OnAudit(func(a Audit) { got = append(got, a) })

	s.AddAudit(ctx, Audit{User: "u@x", Action: "pg_query", Detail: "select 1", Success: true})
	if len(got) != 1 {
		t.Fatalf("want sink called once, got %d", len(got))
	}
	if got[0].Action != "pg_query" || got[0].TS.IsZero() {
		t.Errorf("sink got unexpected audit: %+v", got[0])
	}
}

func TestPurgeAuditOlderThan(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	// Two rows: one old (backdated directly), one fresh via AddAudit.
	old := time.Now().UTC().Add(-48 * time.Hour)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit (ts,user,action,detail,success) VALUES (?,?,?,?,1)`,
		old.Format(time.RFC3339), "old@x", "pg_query", "stale")
	if err != nil {
		t.Fatal(err)
	}
	s.AddAudit(ctx, Audit{User: "new@x", Action: "pg_query", Detail: "fresh", Success: true})

	// Purge anything older than 24h: removes only the backdated row.
	n, err := s.PurgeAuditOlderThan(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 purged, got %d", n)
	}
	rows, err := s.ListAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].User != "new@x" {
		t.Fatalf("want only the fresh row to survive, got %+v", rows)
	}
}

func TestIterAuditOrder(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	s.AddAudit(ctx, Audit{User: "a", Action: "one", Success: true})
	s.AddAudit(ctx, Audit{User: "b", Action: "two", Success: true})

	var actions []string
	if err := s.IterAudit(ctx, func(a Audit) error { actions = append(actions, a.Action); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || actions[0] != "one" || actions[1] != "two" {
		t.Fatalf("want [one two] in insertion order, got %v", actions)
	}
}
