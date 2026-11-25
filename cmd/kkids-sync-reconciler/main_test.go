package main

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

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
	wantQ := []string{"横浜駅SF(3)", "横浜駅SF", "横浜駅SF(3) (角川コミックス・エース)", "横浜駅SF 3"}
	if !slices.Equal(qs, wantQ) {
		t.Errorf("queries = %q, want %q", qs, wantQ)
	}
	if got := queries("O/A(3) (角川コミックス・エース)"); got[len(got)-1] != "O A 3" {
		t.Errorf("words-only query = %q", got)
	}
	if got := queries("単発の本"); !slices.Equal(got, []string{"単発の本"}) {
		t.Errorf("queries = %q", got)
	}
}

func TestLimitPlan(t *testing.T) {
	plan := map[string]store.Diff{
		"P1": {Add: []string{"a1", "a2", "a3", "a4"}, Remove: []string{"r1", "r2"}},
		"P2": {Add: []string{"b1"}, Remove: []string{"s1"}},
	}
	out, total, deferred := limitPlan(plan, 4)
	if total != 4 || deferred != 4 {
		t.Fatalf("total=%d deferred=%d", total, deferred)
	}
	// Removals first, in turns: r1, s1, r2; then one addition: a1.
	if got := out["P1"].Remove; len(got) != 2 {
		t.Errorf("P1 removes = %v", got)
	}
	if got := out["P2"].Remove; len(got) != 1 {
		t.Errorf("P2 removes = %v", got)
	}
	if got := out["P1"].Add; len(got) != 1 || got[0] != "a1" {
		t.Errorf("P1 adds = %v", got)
	}
	if len(out["P2"].Add) != 0 {
		t.Errorf("P2 adds = %v", out["P2"].Add)
	}
	if _, total, deferred := limitPlan(plan, 100); total != 8 || deferred != 0 {
		t.Errorf("all fit: total=%d deferred=%d", total, deferred)
	}
}

func TestBackoff(t *testing.T) {
	b := loadBackoff(filepath.Join(t.TempDir(), "failures.json"))
	now := time.Date(2026, 11, 25, 20, 0, 0, 0, time.UTC)
	b.failed("P", "A", now)
	plan := map[string]store.Diff{"P": {Add: []string{"A", "B"}}}
	out, held := b.filter(plan, now.Add(30*time.Minute))
	if held != 1 || len(out["P"].Add) != 1 || out["P"].Add[0] != "B" {
		t.Errorf("within an hour: held=%d out=%v", held, out)
	}
	if _, held := b.filter(plan, now.Add(61*time.Minute)); held != 0 {
		t.Errorf("after an hour: held=%d", held)
	}
	b.failed("P", "A", now)
	if _, held := b.filter(plan, now.Add(90*time.Minute)); held != 1 {
		t.Errorf("second failure waits two hours: held=%d", held)
	}
	if err := b.save(); err != nil {
		t.Fatal(err)
	}
	if again := loadBackoff(b.path); again.Entries[backoffKey("P", "A")].Failures != 2 {
		t.Errorf("not saved: %+v", again.Entries)
	}
	// No longer planned: forgotten.
	b.filter(map[string]store.Diff{}, now)
	if len(b.Entries) != 0 {
		t.Errorf("stale entries kept: %v", b.Entries)
	}
}

func TestLimitUnsearchable(t *testing.T) {
	b := loadBackoff(filepath.Join(t.TempDir(), "failures.json"))
	b.Unsearchable["U1"], b.Unsearchable["U2"], b.Unsearchable["U3"] = true, true, true
	plan := map[string]store.Diff{"P": {Remove: []string{"U1", "A", "U2"}, Add: []string{"U3", "B"}}}
	out, left := b.limitUnsearchable(plan, 2)
	if left != 1 {
		t.Errorf("left = %d", left)
	}
	if got := out["P"]; !slices.Equal(got.Remove, []string{"U1", "A", "U2"}) || !slices.Equal(got.Add, []string{"B"}) {
		t.Errorf("plan = %+v", got)
	}
	if err := b.save(); err != nil {
		t.Fatal(err)
	}
	if again := loadBackoff(b.path); !again.Unsearchable["U2"] {
		t.Error("unsearchable not saved")
	}
}
