package store

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/nasa9084/kkids-sync/internal/titles"
)

// SeriesRule puts titles matching Pattern into Series. Rules are tried in
// order of ID before the built-in guess.
type SeriesRule struct {
	ID      int64
	Pattern string
	Series  string
	re      *regexp.Regexp
}

// CompileRule checks a rule. Pattern is a Go regexp matched against the
// NFKC-normalized title; an optional group named vol gives the volume.
func CompileRule(pattern, series string) (SeriesRule, error) {
	pattern, series = strings.TrimSpace(pattern), strings.TrimSpace(series)
	if pattern == "" || series == "" {
		return SeriesRule{}, fmt.Errorf("パターンとシリーズ名を入れてください")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return SeriesRule{}, fmt.Errorf("パターンが正しくありません: %w", err)
	}
	return SeriesRule{Pattern: pattern, Series: series, re: re}, nil
}

// match reports whether the rule takes title, and the volume it gives:
// the vol group if the pattern has one, else the built-in guess, else 0.
func (r SeriesRule) match(title string) (vol int, ok bool) {
	t := strings.TrimSpace(norm.NFKC.String(title))
	m := r.re.FindStringSubmatch(t)
	if m == nil {
		return 0, false
	}
	if i := r.re.SubexpIndex("vol"); i > 0 && m[i] != "" {
		if v, err := strconv.Atoi(m[i]); err == nil {
			return v, true
		}
	}
	if _, v, ok := titles.ParseSeries(title); ok {
		return v, true
	}
	return 0, true
}

// SeriesRules lists the rules in the order they are tried.
func (s *Store) SeriesRules(ctx context.Context) ([]SeriesRule, error) {
	return s.seriesRules(ctx, s.db)
}

func (s *Store) seriesRules(ctx context.Context, q execQuerier) ([]SeriesRule, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, pattern, series FROM series_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SeriesRule
	for rows.Next() {
		var id int64
		var p, name string
		if err := rows.Scan(&id, &p, &name); err != nil {
			return nil, err
		}
		r, err := CompileRule(p, name)
		if err != nil {
			continue // stored rules were checked when saved
		}
		r.ID = id
		out = append(out, r)
	}
	return out, rows.Err()
}

// RuleMatch is a book a rule would change, for the preview.
type RuleMatch struct {
	ASIN, Title          string
	FromSeries, ToSeries string
	FromIndex, ToIndex   int
}

// PreviewRule lists the library books the rule matches and what would
// change. Books already in that series at that volume are left out.
func (s *Store) PreviewRule(ctx context.Context, r SeriesRule) ([]RuleMatch, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT b.asin, b.title, COALESCE(sr.name, ''), COALESCE(b.series_index, 0)
		FROM books b LEFT JOIN series sr ON sr.id = b.series_id
		WHERE b.removed_at IS NULL ORDER BY b.title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RuleMatch
	for rows.Next() {
		var m RuleMatch
		if err := rows.Scan(&m.ASIN, &m.Title, &m.FromSeries, &m.FromIndex); err != nil {
			return nil, err
		}
		vol, ok := r.match(m.Title)
		if !ok {
			continue
		}
		m.ToSeries, m.ToIndex = r.Series, vol
		if m.FromSeries == m.ToSeries && m.FromIndex == m.ToIndex {
			continue
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddRule saves a rule and moves every library book it matches into its
// series, including books set by hand. It returns how many books moved.
func (s *Store) AddRule(ctx context.Context, r SeriesRule) (int, error) {
	matches, err := s.PreviewRule(ctx, r)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO series_rules (pattern, series, created_at) VALUES (?, ?, ?)`, r.Pattern, r.Series, s.stamp()); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO series (name) VALUES (?) ON CONFLICT (name) DO NOTHING`, r.Series); err != nil {
		return 0, err
	}
	for _, m := range matches {
		if _, err := tx.ExecContext(ctx, `UPDATE books SET series_id = (SELECT id FROM series WHERE name = ?), series_index = ?, series_source = 'rule' WHERE asin = ?`, m.ToSeries, m.ToIndex, m.ASIN); err != nil {
			return 0, err
		}
	}
	return len(matches), tx.Commit()
}

// DeleteRule removes a rule. Books keep the series it gave them.
func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM series_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RenameSeries renames a series. If another series already has the new
// name, the books move there and the old series (with its default) goes.
func (s *Store) RenameSeries(ctx context.Context, id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("シリーズ名を入れてください")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var other int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM series WHERE name = ?`, name).Scan(&other)
	switch {
	case err == nil && other == id:
		return nil
	case err == nil:
		if _, err := tx.ExecContext(ctx, `UPDATE books SET series_id = ? WHERE series_id = ?`, other, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM series WHERE id = ?`, id); err != nil {
			return err
		}
	default:
		res, err := tx.ExecContext(ctx, `UPDATE series SET name = ? WHERE id = ?`, name, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
	}
	return tx.Commit()
}

// assignWithRules returns the series and volume for a book: the first
// matching rule (source "rule"), else the built-in guess (source "").
func assignWithRules(rules []SeriesRule, title string) (name string, vol int, source string, ok bool) {
	for _, r := range rules {
		if v, ok := r.match(title); ok {
			return r.Series, v, "rule", true
		}
	}
	name, vol, ok = titles.ParseSeries(title)
	return name, vol, "", ok
}

// Reclassify runs the rules and the built-in guess again over every book
// whose series was not set by hand, e.g. after the guess has improved. It
// returns how many books changed series or volume.
func (s *Store) Reclassify(ctx context.Context) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rules, err := s.seriesRules(ctx, tx)
	if err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT b.asin, b.title, COALESCE(sr.name, ''), COALESCE(b.series_index, 0)
		FROM books b LEFT JOIN series sr ON sr.id = b.series_id
		WHERE COALESCE(b.series_source, '') <> 'manual'`)
	if err != nil {
		return 0, err
	}
	type change struct {
		asin, name, source string
		vol                int
		ok                 bool
	}
	var todo []change
	for rows.Next() {
		var asin, title, cur string
		var curVol int
		if err := rows.Scan(&asin, &title, &cur, &curVol); err != nil {
			rows.Close()
			return 0, err
		}
		name, vol, source, ok := assignWithRules(rules, title)
		if !ok {
			name, vol = "", 0
		}
		if name != cur || vol != curVol {
			todo = append(todo, change{asin, name, source, vol, ok})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, c := range todo {
		if !c.ok {
			if _, err := tx.ExecContext(ctx, `UPDATE books SET series_id = NULL, series_index = NULL, series_source = NULL WHERE asin = ?`, c.asin); err != nil {
				return 0, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO series (name) VALUES (?) ON CONFLICT (name) DO NOTHING`, c.name); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE books SET series_id = (SELECT id FROM series WHERE name = ?), series_index = ?, series_source = NULLIF(?, '') WHERE asin = ?`, c.name, c.vol, c.source, c.asin); err != nil {
			return 0, err
		}
	}
	return len(todo), tx.Commit()
}
