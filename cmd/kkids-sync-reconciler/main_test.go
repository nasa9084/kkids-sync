package main

import (
	"reflect"
	"slices"
	"testing"

	"github.com/nasa9084/kkids-sync/internal/amazon"
	"github.com/nasa9084/kkids-sync/internal/store"
)

func TestMerge(t *testing.T) {
	prev := &amazon.Snapshot{Books: []amazon.Book{
		{ASIN: "B2", Title: "two"},
		{ASIN: "B1", Title: "one"},
	}}
	cur := &amazon.Snapshot{
		Books:       []amazon.Book{{ASIN: "B3", Title: "three"}, {ASIN: "B2", Title: "two (new title)"}},
		Shared:      map[string][]string{"c": {"B3"}},
		Complete:    true,
		Incremental: true,
	}
	got := merge(cur, prev)
	want := []amazon.Book{
		{ASIN: "B3", Title: "three"},
		{ASIN: "B2", Title: "two (new title)"},
		{ASIN: "B1", Title: "one"},
	}
	if !reflect.DeepEqual(got.Books, want) {
		t.Errorf("Books = %+v, want %+v", got.Books, want)
	}
	if !reflect.DeepEqual(got.Shared, cur.Shared) || !got.Complete || !got.Incremental {
		t.Errorf("merge changed the share state or flags: %+v", got)
	}
	if len(cur.Books) != 2 {
		t.Error("merge modified its input")
	}
}

func TestStepsAndQuery(t *testing.T) {
	got := steps(store.Diff{Add: []string{"A1"}, Remove: []string{"R1", "R2"}})
	want := []step{{"R1", false}, {"R2", false}, {"A1", true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("steps = %v, want %v", got, want)
	}
	qs := queries("横浜駅SF(3) (角川コミックス・エース)")
	wantQ := []string{"横浜駅SF(3)", "横浜駅SF", "横浜駅SF(3) (角川コミックス・エース)"}
	if !slices.Equal(qs, wantQ) {
		t.Errorf("queries = %q, want %q", qs, wantQ)
	}
	if got := queries("単発の本"); !slices.Equal(got, []string{"単発の本"}) {
		t.Errorf("queries = %q", got)
	}
}
