package main

import (
	"reflect"
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
	if q := query("鬼灯さん家のアネキ(2) (角川コミックス・エース・エクストラ)"); q != "鬼灯さん家のアネキ(2)" {
		t.Errorf("query = %q", q)
	}
}
