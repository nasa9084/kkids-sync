package store

import (
	"context"
	"testing"
)

func TestAdmin(t *testing.T) {
	ctx := context.Background()
	s, mom := newTestStore(t)
	if _, err := s.ApplySnapshot(ctx, LibrarySnapshot{
		Books:    []Book{{ASIN: "S1", Title: "港町ミステリー 1"}, {ASIN: "S2", Title: "港町ミステリー 2"}, {ASIN: "X", Title: "単発の本"}},
		Shared:   map[string][]string{"P1": {"S1", "KU"}},
		Complete: true,
	}); err != nil {
		t.Fatal(err)
	}
	yes, no := true, false

	unset, _ := s.AdminBooks(ctx, FilterUnset, "", 50, 0)
	if len(unset) != 3 {
		t.Errorf("unset = %d, want 3", len(unset))
	}
	series, _ := s.SeriesList(ctx, "")
	if len(series) != 1 || series[0].Books != 2 {
		t.Fatalf("series = %+v", series)
	}
	if err := s.SetSeriesDefault(ctx, series[0].ID, &yes); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBookVisibility(ctx, "S2", &no); err != nil {
		t.Fatal(err)
	}
	b, err := s.AdminBook(ctx, "S1")
	if err != nil || !b.Visible || b.Explicit != nil || b.SharedWith != 1 {
		t.Errorf("S1 = %+v, %v", b, err)
	}
	if b, _ := s.AdminBook(ctx, "S2"); b.Visible {
		t.Error("S2 visible despite its own setting")
	}
	if err := s.SetBookVisibility(ctx, "S2", nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.AdminBook(ctx, "S2"); !b.Visible || b.Explicit != nil {
		t.Errorf("S2 after clearing = %+v", b)
	}
	if hidden, _ := s.AdminBooks(ctx, FilterHidden, "", 50, 0); len(hidden) != 1 || hidden[0].ASIN != "X" {
		t.Errorf("hidden = %+v", hidden)
	}

	// Moving a book out of a series sticks across syncs.
	if err := s.SetBookSeries(ctx, "S2", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplySnapshot(ctx, LibrarySnapshot{Books: []Book{{ASIN: "S2", Title: "港町ミステリー 2"}}, Complete: true, Incremental: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.AdminBook(ctx, "S2"); b.SeriesID != 0 {
		t.Errorf("S2 went back into a series: %+v", b)
	}
	if err := s.SetBookSeries(ctx, "X", "別シリーズ", 3); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.AdminBook(ctx, "X"); b.SeriesName != "別シリーズ" || b.SeriesIndex != 3 {
		t.Errorf("X = %+v", b)
	}

	if _, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('bootstrapped', '1')`); err != nil {
		t.Fatal(err)
	}
	o, err := s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.Books != 3 || o.LastSync == nil || len(o.Outside) != 1 || o.Outside[0].ASIN != "KU" || o.Outside[0].Member != "Mom" {
		t.Errorf("Overview = %+v", o)
	}
	// Mom selected nothing, so S1 (shared, in the library) is planned for removal.
	if o.ToRemove != 1 {
		t.Errorf("ToRemove = %d, want 1", o.ToRemove)
	}
	_ = mom

	if err := s.SetDefaultVisible(ctx, true); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.DefaultVisible(ctx); !v {
		t.Error("default not changed")
	}
	if _, err := ParseTri("x"); err == nil {
		t.Error("ParseTri accepted garbage")
	}
}
