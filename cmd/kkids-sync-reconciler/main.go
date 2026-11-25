// Command kkids-sync-reconciler keeps the Amazon Kids profiles in line with
// what family members picked.
//
//	kkids-sync-reconciler login   sign in by hand and save the session
//	kkids-sync-reconciler sync    read the library and share state into a JSON snapshot,
//	                              and send it to kkids-sync-web when -web is set
//	kkids-sync-reconciler plan    show what kkids-sync-web wants changed (nothing is changed)
//	kkids-sync-reconciler apply   make those changes on the parent dashboard, then sync
//
// The internal API token is read from KKIDS_INTERNAL_TOKEN.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nasa9084/kkids-sync/internal/amazon"
	"github.com/nasa9084/kkids-sync/internal/internalapi"
)

// headless is set by -headless for every browser this command opens.
var headless bool

func main() {
	state := flag.String("state", "storage-state.json", "Amazon session file (contains cookies; keep it secret)")
	out := flag.String("out", "snapshot.json", "sync: where to write the snapshot")
	maxPages := flag.Int("max-pages", 60, "sync: give up after this many list pages")
	full := flag.Bool("full", false, "sync: scroll through the whole library instead of stopping at books already in -out")
	web := flag.String("web", "", "kkids-sync-web internal API base URL, e.g. http://kkids-sync-web:8081")
	maxChanges := flag.Int("max-changes", 50, "apply: refuse plans with more changes than this")
	flag.BoolVar(&headless, "headless", false, "run Chromium without a window (by default it needs a display, e.g. xvfb-run)")
	syncEvery := flag.Duration("sync-every", 24*time.Hour, "run: sync the library when the snapshot in -out is older than this")
	jitter := flag.Duration("jitter", 0, "run: wait a random time up to this before starting")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: kkids-sync-reconciler [flags] login|sync|link-profiles|plan|apply|run\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var client *internalapi.Client
	if *web != "" {
		token := os.Getenv("KKIDS_INTERNAL_TOKEN")
		if token == "" {
			log.Error("KKIDS_INTERNAL_TOKEN is required with -web")
			os.Exit(2)
		}
		client = &internalapi.Client{BaseURL: *web, Token: token}
	}

	var err error
	switch flag.Arg(0) {
	case "login":
		err = login(log, *state)
	case "sync":
		err = syncLibrary(ctx, log, *state, *out, *maxPages, *full, client)
	case "link-profiles":
		err = linkProfiles(ctx, log, client, *state)
	case "plan":
		err = showPlan(ctx, client)
	case "apply":
		var res applyResult
		err = apply(ctx, log, client, *state, *maxChanges, &res)
		if err == nil && res.changed > 0 {
			err = syncLibrary(ctx, log, *state, *out, *maxPages, false, client)
		}
	case "run":
		err = run(ctx, log, client, *state, *out, *maxPages, *maxChanges, *syncEvery, *jitter)
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		log.Error("failed", "err", err)
		if errors.Is(err, amazon.ErrSignedOut) {
			os.Exit(3)
		}
		if errors.Is(err, errTooManyChanges) {
			os.Exit(4)
		}
		os.Exit(1)
	}
}

// run is what the scheduled job does: sync the library first if the
// snapshot in -out is missing or older than syncEvery, apply the plan, and
// sync again if apply changed something. With nothing to do it does not start
// a browser. Syncing first means the very first run fills web even though
// the plan is then refused as too large (nobody has selected anything yet).
func run(ctx context.Context, log *slog.Logger, client *internalapi.Client, state, out string, maxPages, maxChanges int, syncEvery, jitter time.Duration) error {
	if jitter > 0 {
		if err := amazon.Sleep(ctx, 0, jitter); err != nil {
			return err
		}
	}
	if fi, err := os.Stat(out); err != nil || time.Since(fi.ModTime()) > syncEvery {
		if err := syncLibrary(ctx, log, state, out, maxPages, false, client); err != nil {
			return err
		}
	}
	var res applyResult
	if err := apply(ctx, log, client, state, maxChanges, &res); err != nil {
		return err
	}
	if res.changed == 0 {
		return nil
	}
	return syncLibrary(ctx, log, state, out, maxPages, false, client)
}

func login(log *slog.Logger, state string) (err error) {
	s, err := amazon.Open(amazon.Options{StatePath: state, ReadOnly: true, Logger: log, Headless: headless})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	return s.Login(func() {
		fmt.Fprintln(os.Stderr, "ブラウザでサインインし、保護者ダッシュボードが表示されたら Enter を押してください")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	})
}

func syncLibrary(ctx context.Context, log *slog.Logger, state, out string, maxPages int, full bool, client *internalapi.Client) (err error) {
	if _, err := os.Stat(state); err != nil {
		return fmt.Errorf("%w: no session file at %s (run login first)", amazon.ErrSignedOut, state)
	}
	s, err := amazon.Open(amazon.Options{StatePath: state, ReadOnly: true, Logger: log, Headless: headless})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()

	var prev *amazon.Snapshot
	if !full {
		prev, err = readSnapshot(out)
		if err != nil {
			return err
		}
		switch {
		case prev == nil:
			log.Info("no earlier snapshot: reading the whole library", "out", out)
		case !prev.Complete:
			// An incomplete snapshot misses books; trusting it would make
			// incremental sync stop too early forever.
			log.Info("earlier snapshot is incomplete: reading the whole library", "out", out)
			prev = nil
		}
	}
	opts := amazon.ReadLibraryOptions{MaxPages: maxPages}
	if prev != nil {
		opts.Known = map[string]bool{}
		for _, b := range prev.Books {
			opts.Known[b.ASIN] = true
		}
	}

	snap, readErr := s.ReadLibrary(ctx, opts)
	if readErr != nil {
		// A failed run sees only part of the library; writing it would
		// throw away the earlier snapshot.
		return readErr
	}
	if snap.Incremental && prev != nil {
		snap = merge(snap, prev)
	}
	if len(snap.Books) > 0 {
		if err := writeJSON(out, snap); err != nil {
			return errors.Join(readErr, err)
		}
		attrs := []any{"books", len(snap.Books), "complete", snap.Complete, "incremental", snap.Incremental, "out", out}
		for child, asins := range snap.Shared {
			attrs = append(attrs, "shared["+shortID(child)+"]", len(asins))
		}
		log.Info("snapshot written", attrs...)
	}
	if client != nil {
		res, err := client.PostSnapshot(ctx, snap)
		if err != nil {
			return fmt.Errorf("send snapshot to web: %w", err)
		}
		log.Info("snapshot sent to web", "new", res.NewBooks, "removed", res.RemovedBooks, "profiles", res.Profiles)
	}
	return nil
}

func showPlan(ctx context.Context, client *internalapi.Client) error {
	if client == nil {
		return errors.New("plan needs -web")
	}
	plan, err := client.Plan(ctx)
	if err != nil {
		return err
	}
	for profile, d := range plan {
		fmt.Printf("%s: add %d, remove %d\n", shortID(profile), len(d.Add), len(d.Remove))
		for _, a := range d.Add {
			fmt.Printf("  + %s\n", a)
		}
		for _, a := range d.Remove {
			fmt.Printf("  - %s\n", a)
		}
	}
	return nil
}

// readSnapshot loads an earlier snapshot; a missing file is not an error.
func readSnapshot(path string) (*amazon.Snapshot, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s amazon.Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(s.Books) == 0 {
		return nil, nil
	}
	return &s, nil
}

// merge puts the newly seen books in front of the earlier list. Books that
// left the library stay until the next full sync, which is the only way to
// notice them.
func merge(cur, prev *amazon.Snapshot) *amazon.Snapshot {
	out := *cur
	seen := map[string]bool{}
	out.Books = nil
	for _, b := range cur.Books {
		seen[b.ASIN] = true
		out.Books = append(out.Books, b)
	}
	for _, b := range prev.Books {
		if !seen[b.ASIN] {
			out.Books = append(out.Books, b)
		}
	}
	return &out
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// shortID keeps logs readable without printing whole profile IDs.
func shortID(id string) string {
	if len(id) <= 4 {
		return id
	}
	return "…" + id[len(id)-4:]
}
