package store

import (
	"context"
)

// assignSeries gives every book without a series one: by the owner's rules
// first, else when its title has a volume number. Series are created on
// first use.
func (s *Store) assignSeries(ctx context.Context, q execQuerier) error {
	rules, err := s.seriesRules(ctx, q)
	if err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT asin, title FROM books WHERE series_id IS NULL AND COALESCE(series_index, 0) <> -1`)
	if err != nil {
		return err
	}
	type pending struct {
		asin, name string
		vol        int
	}
	var todo []pending
	for rows.Next() {
		var asin, title string
		if err := rows.Scan(&asin, &title); err != nil {
			rows.Close()
			return err
		}
		if name, vol, ok := assignWithRules(rules, title); ok {
			todo = append(todo, pending{asin, name, vol})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range todo {
		if _, err := q.ExecContext(ctx, `INSERT INTO series (name) VALUES (?) ON CONFLICT (name) DO NOTHING`, p.name); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `
			UPDATE books SET series_id = (SELECT id FROM series WHERE name = ?), series_index = ?
			WHERE asin = ?`, p.name, p.vol, p.asin); err != nil {
			return err
		}
	}
	return nil
}
