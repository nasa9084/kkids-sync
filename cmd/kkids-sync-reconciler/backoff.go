package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nasa9084/kkids-sync/internal/store"
)

// backoff remembers changes that failed, so that a book the dashboard's
// search cannot find does not take a place in every run and hold up the
// rest. A change is held for an hour after its first failure, doubling up
// to a day.
type backoff struct {
	path    string
	Entries map[string]backoffEntry `json:"entries"`
	// Unsearchable holds ASINs the dashboard's search never returned (e.g.
	// 【連載版】 single episodes). They go straight to the list fallback.
	Unsearchable map[string]bool `json:"unsearchable"`
	// Removed holds when books were removed, per profile, for the
	// removal pace (-max-removals-per-day).
	Removed map[string][]time.Time `json:"removed,omitempty"`
}

// removed records that a book was removed from profile.
func (b *backoff) removed(profile string, now time.Time) {
	if b.Removed == nil {
		b.Removed = map[string][]time.Time{}
	}
	b.Removed[profile] = append(b.Removed[profile], now)
}

// paceRemovals trims every profile's removals to perRun, and to what is
// left of perDay in the last 24 hours (0 means no limit). It returns the
// trimmed plan and how many removals wait for later runs.
func (b *backoff) paceRemovals(plan map[string]store.Diff, perRun, perDay int, now time.Time) (map[string]store.Diff, int) {
	for p, ts := range b.Removed {
		var keep []time.Time
		for _, t := range ts {
			if now.Sub(t) < 24*time.Hour {
				keep = append(keep, t)
			}
		}
		if len(keep) == 0 {
			delete(b.Removed, p)
		} else {
			b.Removed[p] = keep
		}
	}
	waiting := 0
	out := make(map[string]store.Diff, len(plan))
	for p, d := range plan {
		allowed := len(d.Remove)
		if perRun > 0 {
			allowed = min(allowed, perRun)
		}
		if perDay > 0 {
			allowed = min(allowed, max(0, perDay-len(b.Removed[p])))
		}
		waiting += len(d.Remove) - allowed
		d.Remove = d.Remove[:allowed:allowed]
		out[p] = d
	}
	return out, waiting
}

type backoffEntry struct {
	Failures int       `json:"failures"`
	Last     time.Time `json:"last"`
}

func backoffPath(state string) string {
	return filepath.Join(filepath.Dir(state), "failures.json")
}

func loadBackoff(path string) *backoff {
	b := &backoff{path: path, Entries: map[string]backoffEntry{}, Unsearchable: map[string]bool{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return b // none yet, or unreadable: start over
	}
	_ = json.Unmarshal(data, b)
	if b.Entries == nil {
		b.Entries = map[string]backoffEntry{}
	}
	if b.Unsearchable == nil {
		b.Unsearchable = map[string]bool{}
	}
	return b
}

// limitUnsearchable keeps at most max changes of unsearchable books (each
// needs the slow list fallback, which runs at most max times per run) and
// leaves the rest for later runs without spending a place on them. It
// returns the plan and how many it left.
func (b *backoff) limitUnsearchable(plan map[string]store.Diff, max int) (map[string]store.Diff, int) {
	profiles := make([]string, 0, len(plan))
	for p := range plan {
		profiles = append(profiles, p)
	}
	sort.Strings(profiles)
	out := map[string]store.Diff{}
	kept, left := 0, 0
	keep := func(a string) bool {
		if !b.Unsearchable[a] {
			return true
		}
		if kept < max {
			kept++
			return true
		}
		left++
		return false
	}
	for _, p := range profiles {
		d := plan[p]
		nd := store.Diff{Titles: d.Titles}
		for _, a := range d.Remove {
			if keep(a) {
				nd.Remove = append(nd.Remove, a)
			}
		}
		for _, a := range d.Add {
			if keep(a) {
				nd.Add = append(nd.Add, a)
			}
		}
		out[p] = nd
	}
	return out, left
}

func backoffKey(profile, asin string) string { return profile + "/" + asin }

func (b *backoff) held(profile, asin string, now time.Time) bool {
	e, ok := b.Entries[backoffKey(profile, asin)]
	if !ok {
		return false
	}
	wait := time.Hour << min(e.Failures-1, 5) // 1h, 2h, ... 32h
	return now.Sub(e.Last) < min(wait, 24*time.Hour)
}

func (b *backoff) failed(profile, asin string, now time.Time) {
	k := backoffKey(profile, asin)
	e := b.Entries[k]
	e.Failures++
	e.Last = now
	b.Entries[k] = e
}

func (b *backoff) succeeded(profile, asin string) {
	delete(b.Entries, backoffKey(profile, asin))
}

// filter drops held changes from the plan and returns how many it dropped.
// Entries for changes no longer planned are forgotten.
func (b *backoff) filter(plan map[string]store.Diff, now time.Time) (map[string]store.Diff, int) {
	out := map[string]store.Diff{}
	planned := map[string]bool{}
	held := 0
	for p, d := range plan {
		nd := store.Diff{Titles: d.Titles}
		for _, a := range d.Remove {
			planned[backoffKey(p, a)] = true
			if b.held(p, a, now) {
				held++
			} else {
				nd.Remove = append(nd.Remove, a)
			}
		}
		for _, a := range d.Add {
			planned[backoffKey(p, a)] = true
			if b.held(p, a, now) {
				held++
			} else {
				nd.Add = append(nd.Add, a)
			}
		}
		out[p] = nd
	}
	for k := range b.Entries {
		if !planned[k] {
			delete(b.Entries, k)
		}
	}
	return out, held
}

func (b *backoff) save() error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // no directory (e.g. a dry run elsewhere)
		}
		return err
	}
	return os.Rename(tmp, b.path)
}
