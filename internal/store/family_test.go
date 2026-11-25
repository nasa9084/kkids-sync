package store

import (
	"context"
	"errors"
	"testing"
)

func TestFamilyViews(t *testing.T) {
	ctx := context.Background()
	s, mom := newTestStore(t)
	if _, err := s.ApplySnapshot(ctx, LibrarySnapshot{
		Books: []Book{
			{ASIN: "S1", Title: "港町ミステリー 1"},
			{ASIN: "S2", Title: "港町ミステリー 2"},
			{ASIN: "S3", Title: "港町ミステリー 3"},
			{ASIN: "X", Title: "山の上の喫茶店"},
			{ASIN: "H", Title: "隠したい本"},
		},
		Shared:   map[string][]string{"P1": {"S1"}},
		Complete: true,
	}); err != nil {
		t.Fatal(err)
	}
	// The global default hides everything; open the series and X.
	s.db.ExecContext(ctx, `UPDATE series SET default_visible = 1 WHERE name = '港町ミステリー'`)
	s.db.ExecContext(ctx, `INSERT INTO visibility (asin, visible) VALUES ('X', 1)`)

	all, err := s.SearchBooks(ctx, mom.ID, "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("visible books = %d, want 4 (H hidden)", len(all))
	}

	found, _ := s.SearchBooks(ctx, mom.ID, "港町", 50, 0)
	if len(found) != 3 {
		t.Errorf("search 港町 = %d books, want 3", len(found))
	}
	if pct, _ := s.SearchBooks(ctx, mom.ID, "%", 50, 0); len(pct) != 0 {
		t.Errorf("search %% matched %d books, want 0", len(pct))
	}

	vols, _ := s.SeriesBooks(ctx, mom.ID, found[0].SeriesID)
	if len(vols) != 3 || vols[0].SeriesIndex != 1 || vols[2].SeriesIndex != 3 {
		t.Errorf("series volumes = %+v", vols)
	}

	if err := s.Select(ctx, mom.ID, "H"); !errors.Is(err, ErrNotFound) {
		t.Errorf("selecting a hidden book: err = %v, want ErrNotFound", err)
	}
	if err := s.Select(ctx, mom.ID, "S1", "S2"); err != nil {
		t.Fatal(err)
	}
	shelf, _ := s.ShelfBooks(ctx, mom.ID, 50, 0)
	status := map[string]string{}
	for _, b := range shelf {
		status[b.ASIN] = b.Status()
	}
	if status["S1"] != "delivered" || status["S2"] != "pending" || len(shelf) != 2 {
		t.Errorf("shelf = %v", status)
	}
	if total, pending, err := s.ShelfCounts(ctx, mom.ID); err != nil || total != 2 || pending != 1 {
		t.Errorf("ShelfCounts = %d, %d, %v", total, pending, err)
	}
	if err := s.Unselect(ctx, mom.ID, "S2"); err != nil {
		t.Fatal(err)
	}
	if shelf, _ := s.ShelfBooks(ctx, mom.ID, 50, 0); len(shelf) != 1 {
		t.Errorf("after unselect: %d books", len(shelf))
	}
}
