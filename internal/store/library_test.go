package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func newTestStore(t *testing.T) (*Store, Member) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	mom, err := s.AddMember(ctx, Member{Name: "Mom", Email: "mom@example.com", Role: RoleFamily, KidsProfileID: "P1"})
	if err != nil {
		t.Fatal(err)
	}
	return s, mom
}

func books(asins ...string) []Book {
	var out []Book
	for _, a := range asins {
		out = append(out, Book{ASIN: a, Title: "title " + a})
	}
	return out
}

func TestApplySnapshot(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)

	if _, err := s.ApplySnapshot(ctx, LibrarySnapshot{Books: books("A")}); !errors.Is(err, ErrIncompleteSnapshot) {
		t.Fatalf("incomplete snapshot: err = %v", err)
	}

	res, err := s.ApplySnapshot(ctx, LibrarySnapshot{
		Books:    books("A", "B", "C"),
		Shared:   map[string][]string{"P1": {"A", "KU"}},
		Complete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res != (SnapshotResult{NewBooks: 3, Profiles: 1}) {
		t.Errorf("first apply = %+v", res)
	}

	// Incremental: new book D, nothing removed.
	res, err = s.ApplySnapshot(ctx, LibrarySnapshot{Books: books("D", "A"), Shared: map[string][]string{"P1": {"A"}}, Complete: true, Incremental: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.NewBooks != 1 || res.RemovedBooks != 0 {
		t.Errorf("incremental apply = %+v", res)
	}

	// Full: C is gone.
	res, err = s.ApplySnapshot(ctx, LibrarySnapshot{Books: books("A", "B", "D"), Shared: map[string][]string{"P1": {"A"}}, Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.RemovedBooks != 1 {
		t.Errorf("full apply = %+v", res)
	}
	var removed int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM books WHERE removed_at IS NOT NULL AND asin = 'C'`).Scan(&removed)
	if removed != 1 {
		t.Error("C not marked removed")
	}
}

func TestPlanAndBootstrap(t *testing.T) {
	ctx := context.Background()
	s, mom := newTestStore(t)
	if _, err := s.ApplySnapshot(ctx, LibrarySnapshot{
		Books:    books("A", "B", "C"),
		Shared:   map[string][]string{"P1": {"A", "B", "KU"}},
		Complete: true,
	}); err != nil {
		t.Fatal(err)
	}

	// No selections yet: everything shared in the library would be removed,
	// but never the Kindle Unlimited title outside it.
	plan, err := s.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (map[string]Diff{"P1": {Remove: []string{"A", "B"}, Titles: map[string]string{"A": "title A", "B": "title B"}}}); !reflect.DeepEqual(plan, want) {
		t.Errorf("plan before bootstrap = %+v, want %+v", plan, want)
	}

	n, err := s.BootstrapFromShared(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("bootstrap selected %d, want 2", n)
	}
	if _, err := s.BootstrapFromShared(ctx); err == nil {
		t.Error("second bootstrap was allowed")
	}
	plan, _ = s.Plan(ctx)
	if want := (map[string]Diff{"P1": {}}); !reflect.DeepEqual(plan, want) {
		t.Errorf("plan after bootstrap = %+v, want no changes", plan)
	}

	// Mom picks C, which is hidden by the global default: nothing to add yet.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO selections (member_id, asin, selected_at) VALUES (?, 'C', 'now')`, mom.ID); err != nil {
		t.Fatal(err)
	}
	plan, _ = s.Plan(ctx)
	if len(plan["P1"].Add) != 0 {
		t.Errorf("hidden book planned for adding: %+v", plan)
	}

	// A series default of visible makes it count.
	s.db.ExecContext(ctx, `INSERT INTO series (id, name, default_visible) VALUES (1, 'S', 1)`)
	s.db.ExecContext(ctx, `UPDATE books SET series_id = 1 WHERE asin = 'C'`)
	plan, _ = s.Plan(ctx)
	if want := []string{"C"}; !reflect.DeepEqual(plan["P1"].Add, want) {
		t.Errorf("Add = %v, want %v", plan["P1"].Add, want)
	}

	// Hiding a selected book explicitly removes it.
	s.db.ExecContext(ctx, `UPDATE visibility SET visible = 0 WHERE asin = 'A'`)
	plan, _ = s.Plan(ctx)
	if want := []string{"A"}; !reflect.DeepEqual(plan["P1"].Remove, want) {
		t.Errorf("Remove = %v, want %v", plan["P1"].Remove, want)
	}
}

func TestBootstrapNeedsShareState(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.BootstrapFromShared(context.Background()); err == nil {
		t.Error("bootstrap without share state was allowed")
	}
}
