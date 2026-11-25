package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
}

type backoffEntry struct {
	Failures int       `json:"failures"`
	Last     time.Time `json:"last"`
}

func backoffPath(state string) string {
	return filepath.Join(filepath.Dir(state), "failures.json")
}

func loadBackoff(path string) *backoff {
	b := &backoff{path: path, Entries: map[string]backoffEntry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return b // none yet, or unreadable: start over
	}
	_ = json.Unmarshal(data, b)
	if b.Entries == nil {
		b.Entries = map[string]backoffEntry{}
	}
	return b
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
