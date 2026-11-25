package store

import (
	"context"
	"testing"
)

func TestShelvesRulesAndRename(t *testing.T) {
	ctx := context.Background()
	s, mom := newTestStore(t)
	if _, err := s.ApplySnapshot(ctx, LibrarySnapshot{
		Books: []Book{
			{ASIN: "A1", Title: "横浜駅SF(1) (角川コミックス・エース)"},
			{ASIN: "A2", Title: "横浜駅SF 全国版(2) (角川コミックス・エース)"},
			{ASIN: "B", Title: "単発の本"},
			{ASIN: "C", Title: "共有だけの本"},
		},
		Shared:   map[string][]string{"P1": {"A1", "C"}},
		Complete: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultVisible(ctx, true); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"A1", "A2", "B"} {
		if err := s.Select(ctx, mom.ID, a); err != nil {
			t.Fatal(err)
		}
	}
	hidden := false
	if err := s.SetBookVisibility(ctx, "B", &hidden); err != nil {
		t.Fatal(err)
	}

	sums, err := s.ShelfSummaries(ctx)
	if err != nil || len(sums) != 1 {
		t.Fatalf("summaries = %+v, %v", sums, err)
	}
	if got := sums[0]; got.Selected != 3 || got.Shared != 1 || got.ToAdd != 1 || got.ToRemove != 1 || got.Held != 1 {
		t.Errorf("summary = %+v", got)
	}
	rows, err := s.MemberShelf(ctx, mom.ID, ShelfToRemove, "", 10, 0)
	if err != nil || len(rows) != 1 || rows[0].ASIN != "C" {
		t.Errorf("to remove = %+v, %v", rows, err)
	}

	// The built-in guess splits 横浜駅SF and 横浜駅SF 全国版; a rule joins them.
	r, err := CompileRule(`^横浜駅SF`, "横浜駅SF")
	if err != nil {
		t.Fatal(err)
	}
	prev, err := s.PreviewRule(ctx, r)
	if err != nil || len(prev) != 1 || prev[0].ASIN != "A2" || prev[0].FromSeries != "横浜駅SF 全国版" || prev[0].ToIndex != 2 {
		t.Fatalf("preview = %+v, %v", prev, err)
	}
	if n, err := s.AddRule(ctx, r); err != nil || n != 1 {
		t.Fatalf("add rule = %d, %v", n, err)
	}
	b, _ := s.AdminBook(ctx, "A2")
	if b.SeriesName != "横浜駅SF" || b.SeriesIndex != 2 {
		t.Errorf("A2 after rule = %q %d", b.SeriesName, b.SeriesIndex)
	}
	if _, err := CompileRule(`(`, "x"); err == nil {
		t.Error("bad pattern accepted")
	}

	// New books follow the rule too.
	if _, err := s.ApplySnapshot(ctx, LibrarySnapshot{Books: []Book{{ASIN: "A3", Title: "横浜駅SF 全国版(3)"}}, Shared: map[string][]string{}, Complete: true, Incremental: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.AdminBook(ctx, "A3"); b.SeriesName != "横浜駅SF" || b.SeriesIndex != 3 {
		t.Errorf("A3 = %q %d", b.SeriesName, b.SeriesIndex)
	}

	// Renaming onto an existing name merges.
	series, _ := s.SeriesList(ctx, "")
	var ids = map[string]int64{}
	for _, sr := range series {
		ids[sr.Name] = sr.ID
	}
	if err := s.SetBookSeries(ctx, "B", "よこはま", 9); err != nil {
		t.Fatal(err)
	}
	series, _ = s.SeriesList(ctx, "よこはま")
	if err := s.RenameSeries(ctx, series[0].ID, "横浜駅SF"); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.AdminBook(ctx, "B"); b.SeriesID != ids["横浜駅SF"] {
		t.Errorf("B after merge = %+v", b)
	}
}
