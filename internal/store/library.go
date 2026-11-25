package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// Book is a title as the reconciler reports it.
type Book struct {
	ASIN      string `json:"asin"`
	Title     string `json:"title"`
	Thumbnail string `json:"thumbnail,omitempty"`
}

// LibrarySnapshot is what the reconciler read from the parent dashboard.
// Its JSON form matches the reconciler's snapshot file.
type LibrarySnapshot struct {
	Books       []Book              `json:"books"`
	Shared      map[string][]string `json:"shared"`
	Complete    bool                `json:"complete"`
	Incremental bool                `json:"incremental"`
}

// SnapshotResult summarises what ApplySnapshot changed.
type SnapshotResult struct {
	NewBooks     int `json:"new_books"`
	RemovedBooks int `json:"removed_books"`
	Profiles     int `json:"profiles"`
}

// ErrIncompleteSnapshot rejects snapshots that would lose data.
var ErrIncompleteSnapshot = errors.New("snapshot is not complete")

// ApplySnapshot records a snapshot:
//   - books are added or updated; a full (non-incremental) snapshot also
//     marks books that are gone as removed;
//   - the share state of every profile in the snapshot is replaced.
func (s *Store) ApplySnapshot(ctx context.Context, snap LibrarySnapshot) (SnapshotResult, error) {
	var res SnapshotResult
	if !snap.Complete || len(snap.Books) == 0 {
		return res, ErrIncompleteSnapshot
	}
	now := s.stamp()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	for _, b := range snap.Books {
		if b.ASIN == "" {
			continue
		}
		var existed int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM books WHERE asin = ?`, b.ASIN).Scan(&existed); err != nil {
			return res, err
		}
		if existed == 0 {
			res.NewBooks++
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO books (asin, title, thumbnail, first_seen_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (asin) DO UPDATE SET title = excluded.title, thumbnail = excluded.thumbnail, removed_at = NULL`,
			b.ASIN, b.Title, b.Thumbnail, now); err != nil {
			return res, fmt.Errorf("upsert book %s: %w", b.ASIN, err)
		}
	}

	if !snap.Incremental {
		if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS seen (asin TEXT PRIMARY KEY)`); err != nil {
			return res, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM seen`); err != nil {
			return res, err
		}
		for _, b := range snap.Books {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO seen (asin) VALUES (?)`, b.ASIN); err != nil {
				return res, err
			}
		}
		r, err := tx.ExecContext(ctx, `UPDATE books SET removed_at = ? WHERE removed_at IS NULL AND asin NOT IN (SELECT asin FROM seen)`, now)
		if err != nil {
			return res, err
		}
		n, _ := r.RowsAffected()
		res.RemovedBooks = int(n)
	}

	for profile, asins := range snap.Shared {
		if _, err := tx.ExecContext(ctx, `DELETE FROM shared_state WHERE kids_profile_id = ?`, profile); err != nil {
			return res, err
		}
		for _, a := range asins {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO shared_state (kids_profile_id, asin, observed_at) VALUES (?, ?, ?)`, profile, a, now); err != nil {
				return res, err
			}
		}
		res.Profiles++
	}

	if err := s.assignSeries(ctx, tx); err != nil {
		return res, fmt.Errorf("assign series: %w", err)
	}

	mode := "全件"
	if snap.Incremental {
		mode = "差分"
	}
	detail := fmt.Sprintf("%s同期: 新しい本 %d冊・ライブラリから消えた本 %d冊・共有状態 %d人分", mode, res.NewBooks, res.RemovedBooks, res.Profiles)
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs (kind, started_at, finished_at, result, detail) VALUES ('sync', ?, ?, 'ok', ?)`, now, now, detail); err != nil {
		return res, err
	}
	return res, tx.Commit()
}

// Diff is what must change for one kids profile.
type Diff struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

// Plan works out, for every family member with a kids profile, which books
// to add to and remove from that profile:
//
//	desired = books the member selected that are in the library and visible
//	add     = desired − shared
//	remove  = shared books in the library − desired
//
// Shares of titles outside the library (Kindle Unlimited and the like) are
// never removed.
func (s *Store) Plan(ctx context.Context) (map[string]Diff, error) {
	def, err := s.defaultVisible(ctx)
	if err != nil {
		return nil, err
	}
	profiles := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT kids_profile_id FROM members WHERE role = 'family' AND kids_profile_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		profiles[p] = true
	}
	rows.Close()

	desired, err := s.pairs(ctx, `
		SELECT m.kids_profile_id, b.asin
		FROM selections sel
		JOIN members m ON m.id = sel.member_id
		JOIN books b ON b.asin = sel.asin
		LEFT JOIN visibility v ON v.asin = b.asin
		LEFT JOIN series sr ON sr.id = b.series_id
		WHERE m.role = 'family' AND m.kids_profile_id IS NOT NULL
		  AND b.removed_at IS NULL
		  AND COALESCE(v.visible, sr.default_visible, ?) = 1`, def)
	if err != nil {
		return nil, err
	}
	sharedAll, err := s.pairs(ctx, `SELECT kids_profile_id, asin FROM shared_state`)
	if err != nil {
		return nil, err
	}
	sharedManaged, err := s.pairs(ctx, `
		SELECT ss.kids_profile_id, ss.asin
		FROM shared_state ss JOIN books b ON b.asin = ss.asin
		WHERE b.removed_at IS NULL`)
	if err != nil {
		return nil, err
	}

	plan := map[string]Diff{}
	for p := range profiles {
		var d Diff
		for a := range desired[p] {
			if !sharedAll[p][a] {
				d.Add = append(d.Add, a)
			}
		}
		for a := range sharedManaged[p] {
			if !desired[p][a] {
				d.Remove = append(d.Remove, a)
			}
		}
		sort.Strings(d.Add)
		sort.Strings(d.Remove)
		plan[p] = d
	}
	return plan, nil
}

// BootstrapFromShared turns today's share state into selections, so the
// first reconcile changes nothing. Every book that ends up selected is
// marked visible, since the global default hides new books. It refuses to
// run when there are selections already, or nothing is shared yet.
func (s *Store) BootstrapFromShared(ctx context.Context) (selected int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM selections`).Scan(&n); err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, fmt.Errorf("there are %d selections already", n)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM shared_state`).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, errors.New("no share state yet: run a sync first")
	}
	now := s.stamp()
	r, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO selections (member_id, asin, selected_at)
		SELECT m.id, ss.asin, ?
		FROM shared_state ss
		JOIN members m ON m.kids_profile_id = ss.kids_profile_id AND m.role = 'family'
		JOIN books b ON b.asin = ss.asin AND b.removed_at IS NULL`, now)
	if err != nil {
		return 0, err
	}
	added, _ := r.RowsAffected()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO visibility (asin, visible)
		SELECT DISTINCT asin, 1 FROM selections WHERE true
		ON CONFLICT (asin) DO UPDATE SET visible = 1`); err != nil {
		return 0, err
	}
	return int(added), tx.Commit()
}

func (s *Store) defaultVisible(ctx context.Context) (int, error) {
	var v string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'default_visible'`).Scan(&v); err != nil {
		return 0, err
	}
	return strconv.Atoi(v)
}

func (s *Store) pairs(ctx context.Context, query string, args ...any) (map[string]map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if out[k] == nil {
			out[k] = map[string]bool{}
		}
		out[k][v] = true
	}
	return out, rows.Err()
}
