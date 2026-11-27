package main

import (
	"testing"
	"time"

	"github.com/nasa9084/kkids-sync/internal/store"
)

func TestPaceRemovals(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	b := &backoff{}
	for i := 0; i < 98; i++ {
		b.removed("mom", now.Add(-time.Hour))
	}
	b.removed("mom", now.Add(-25*time.Hour)) // older than a day: not counted
	plan := map[string]store.Diff{
		"mom":    {Remove: []string{"a", "b", "c", "d", "e", "f"}},
		"sister": {Remove: []string{"x", "y", "z", "w", "v", "u"}, Add: []string{"n"}},
	}
	out, waiting := b.paceRemovals(plan, 5, 100, now)
	if got := len(out["mom"].Remove); got != 2 {
		t.Errorf("mom: %d removals, want 2 (day budget)", got)
	}
	if got := len(out["sister"].Remove); got != 5 {
		t.Errorf("sister: %d removals, want 5 (run limit)", got)
	}
	if len(out["sister"].Add) != 1 {
		t.Error("adds are not paced")
	}
	if waiting != 5 {
		t.Errorf("waiting %d, want 5", waiting)
	}
	if len(b.Removed["mom"]) != 98 {
		t.Errorf("old entries kept: %d", len(b.Removed["mom"]))
	}
	if out, w := b.paceRemovals(plan, 0, 0, now); len(out["mom"].Remove) != 6 || w != 0 {
		t.Error("no limits should keep everything")
	}
}
