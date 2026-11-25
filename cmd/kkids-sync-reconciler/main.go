// Command kkids-sync-reconciler keeps the Amazon Kids profiles in line with
// what family members picked.
//
// So far it can only read:
//
//	kkids-sync-reconciler login   sign in by hand and save the session
//	kkids-sync-reconciler sync    read the library and share state into a JSON snapshot
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

	"github.com/nasa9084/kkids-sync/internal/amazon"
)

func main() {
	state := flag.String("state", "storage-state.json", "Amazon session file (contains cookies; keep it secret)")
	out := flag.String("out", "snapshot.json", "sync: where to write the snapshot")
	maxPages := flag.Int("max-pages", 60, "sync: give up after this many list pages")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: kkids-sync-reconciler [flags] login|sync\n")
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

	var err error
	switch flag.Arg(0) {
	case "login":
		err = login(log, *state)
	case "sync":
		err = syncLibrary(ctx, log, *state, *out, *maxPages)
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		log.Error("failed", "err", err)
		if errors.Is(err, amazon.ErrSignedOut) {
			os.Exit(3)
		}
		os.Exit(1)
	}
}

func login(log *slog.Logger, state string) (err error) {
	s, err := amazon.Open(amazon.Options{StatePath: state, ReadOnly: true, Logger: log})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	return s.Login(func() {
		fmt.Fprintln(os.Stderr, "ブラウザでサインインし、保護者ダッシュボードが表示されたら Enter を押してください")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	})
}

func syncLibrary(ctx context.Context, log *slog.Logger, state, out string, maxPages int) (err error) {
	if _, err := os.Stat(state); err != nil {
		return fmt.Errorf("%w: no session file at %s (run login first)", amazon.ErrSignedOut, state)
	}
	s, err := amazon.Open(amazon.Options{StatePath: state, ReadOnly: true, Logger: log})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()

	snap, readErr := s.ReadLibrary(ctx, amazon.ReadLibraryOptions{MaxPages: maxPages})
	if snap != nil && len(snap.Books) > 0 {
		if err := writeJSON(out, snap); err != nil {
			return errors.Join(readErr, err)
		}
		attrs := []any{"books", len(snap.Books), "complete", snap.Complete, "out", out}
		for child, asins := range snap.Shared {
			attrs = append(attrs, "shared["+shortID(child)+"]", len(asins))
		}
		log.Info("snapshot written", attrs...)
	}
	return readErr
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
