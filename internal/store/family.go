package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// BookView is a book as one family member sees it.
type BookView struct {
	ASIN        string
	Title       string
	SeriesID    int64 // 0 when not part of a series
	SeriesName  string
	SeriesIndex int
	OnShelf     bool
	Shared      bool // Amazon already shares it with the member's profile
	SelectedAt  time.Time
}

// Status of a book on a member's shelf.
func (b BookView) Status() string {
	switch {
	case b.OnShelf && b.Shared:
		return "delivered"
	case b.OnShelf:
		return "pending"
	default:
		return ""
	}
}

// visibleBooks selects books a family member may see. Bind args: global
// default, member id, then whatever the caller appends.
const visibleBooks = `
SELECT b.asin, b.title,
       COALESCE(b.series_id, 0), COALESCE(sr.name, ''), COALESCE(b.series_index, 0),
       CASE WHEN sel.asin IS NULL THEN 0 ELSE 1 END,
       CASE WHEN ss.asin IS NULL THEN 0 ELSE 1 END,
       COALESCE(sel.selected_at, '')
FROM books b
LEFT JOIN series sr ON sr.id = b.series_id
LEFT JOIN visibility v ON v.asin = b.asin
JOIN members m ON m.id = ?
LEFT JOIN selections sel ON sel.asin = b.asin AND sel.member_id = m.id
LEFT JOIN shared_state ss ON ss.asin = b.asin AND ss.kids_profile_id = m.kids_profile_id
WHERE b.removed_at IS NULL
  AND COALESCE(v.visible, sr.default_visible, ?) = 1
`

func (s *Store) queryBooks(ctx context.Context, memberID int64, extra string, args ...any) ([]BookView, error) {
	def, err := s.defaultVisible(ctx)
	if err != nil {
		return nil, err
	}
	all := append([]any{memberID, def}, args...)
	rows, err := s.db.QueryContext(ctx, visibleBooks+extra, all...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BookView
	for rows.Next() {
		var b BookView
		var selectedAt string
		if err := rows.Scan(&b.ASIN, &b.Title, &b.SeriesID, &b.SeriesName, &b.SeriesIndex, &b.OnShelf, &b.Shared, &selectedAt); err != nil {
			return nil, err
		}
		if selectedAt != "" {
			b.SelectedAt, _ = time.Parse(time.RFC3339, selectedAt)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SearchBooks lists visible books, newest first, optionally filtered by a
// title substring. It returns one more than limit when there are more.
func (s *Store) SearchBooks(ctx context.Context, memberID int64, query string, limit, offset int) ([]BookView, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return s.queryBooks(ctx, memberID, `ORDER BY b.first_seen_at DESC, b.rowid ASC LIMIT ? OFFSET ?`, limit+1, offset)
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query) + "%"
	return s.queryBooks(ctx, memberID,
		`AND (b.title LIKE ? ESCAPE '\' OR sr.name LIKE ? ESCAPE '\') ORDER BY b.first_seen_at DESC, b.rowid ASC LIMIT ? OFFSET ?`,
		like, like, limit+1, offset)
}

// SeriesBooks lists the visible volumes of a series in order.
func (s *Store) SeriesBooks(ctx context.Context, memberID, seriesID int64) ([]BookView, error) {
	return s.queryBooks(ctx, memberID, `AND b.series_id = ? ORDER BY b.series_index, b.title`, seriesID)
}

// ShelfBooks lists the member's shelf, most recently added first. It
// returns one more than limit when there are more.
func (s *Store) ShelfBooks(ctx context.Context, memberID int64, limit, offset int) ([]BookView, error) {
	return s.queryBooks(ctx, memberID, `AND sel.asin IS NOT NULL ORDER BY sel.selected_at DESC, b.title LIMIT ? OFFSET ?`, limit+1, offset)
}

// ShelfCounts returns how many books are on the shelf and how many of them
// Amazon does not share yet.
func (s *Store) ShelfCounts(ctx context.Context, memberID int64) (total, pending int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN ss.asin IS NULL THEN 1 ELSE 0 END), 0)
		FROM selections sel
		JOIN members m ON m.id = sel.member_id
		JOIN books b ON b.asin = sel.asin AND b.removed_at IS NULL
		LEFT JOIN shared_state ss ON ss.asin = sel.asin AND ss.kids_profile_id = m.kids_profile_id
		WHERE sel.member_id = ?`, memberID).Scan(&total, &pending)
	return total, pending, err
}

// BookFor returns one visible book.
func (s *Store) BookFor(ctx context.Context, memberID int64, asin string) (BookView, error) {
	bs, err := s.queryBooks(ctx, memberID, `AND b.asin = ?`, asin)
	if err != nil {
		return BookView{}, err
	}
	if len(bs) == 0 {
		return BookView{}, ErrNotFound
	}
	return bs[0], nil
}

// Select puts books on a member's shelf. Books the member cannot see are
// refused, so a forged request cannot share a hidden book.
func (s *Store) Select(ctx context.Context, memberID int64, asins ...string) error {
	if len(asins) == 0 {
		return nil
	}
	now := s.stamp()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, a := range asins {
		if _, err := s.visibleTo(ctx, tx, memberID, a); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO selections (member_id, asin, selected_at) VALUES (?, ?, ?)`, memberID, a, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Unselect takes books off a member's shelf.
func (s *Store) Unselect(ctx context.Context, memberID int64, asins ...string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, a := range asins {
		if _, err := tx.ExecContext(ctx, `DELETE FROM selections WHERE member_id = ? AND asin = ?`, memberID, a); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) visibleTo(ctx context.Context, tx *sql.Tx, memberID int64, asin string) (bool, error) {
	// An integer: the setting is stored as text, and '1' = 1 is false in SQLite.
	var def int
	if err := tx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'default_visible'`).Scan(&def); err != nil {
		return false, err
	}
	var ok int
	err := tx.QueryRowContext(ctx, `
		SELECT 1 FROM books b
		LEFT JOIN series sr ON sr.id = b.series_id
		LEFT JOIN visibility v ON v.asin = b.asin
		JOIN members m ON m.id = ? AND m.role = 'family'
		WHERE b.asin = ? AND b.removed_at IS NULL AND COALESCE(v.visible, sr.default_visible, ?) = 1`,
		memberID, asin, def).Scan(&ok)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("book %s: %w", asin, ErrNotFound)
	}
	return err == nil, err
}

// Thumbnail returns the cover URL stored for a book.
func (s *Store) Thumbnail(ctx context.Context, asin string) (string, error) {
	var u string
	err := s.db.QueryRowContext(ctx, `SELECT thumbnail FROM books WHERE asin = ?`, asin).Scan(&u)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return u, err
}

// SetVisibility shows or hides one book for every family member.
func (s *Store) SetVisibility(ctx context.Context, asin string, visible bool) error {
	v := 0
	if visible {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO visibility (asin, visible) VALUES (?, ?) ON CONFLICT (asin) DO UPDATE SET visible = excluded.visible`, asin, v)
	return err
}
