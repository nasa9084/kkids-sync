package store

import (
	"context"
	"strings"
)

// ShelfState is where a book stands for one member.
type ShelfState string

const (
	ShelfShared   ShelfState = "shared" // on the shelf and shared on Amazon
	ShelfToAdd    ShelfState = "add"    // on the shelf, not shared yet
	ShelfToRemove ShelfState = "remove" // shared, but taken off the shelf
	ShelfHeld     ShelfState = "held"   // on the shelf but hidden, so not shared
	ShelfAny      ShelfState = ""       // filter: all of the above
)

// ShelfSummary counts one family member's books by state.
type ShelfSummary struct {
	Member                        Member
	Selected                      int
	Shared, ToAdd, ToRemove, Held int
}

// ShelfRow is one book of a member's shelf on the admin page.
type ShelfRow struct {
	ASIN, Title, SeriesName string
	SeriesIndex             int
	State                   ShelfState
}

// shelfRows is every library book a member selected or has shared, with its
// state. It mirrors Plan: hidden selections are not shared.
const shelfRows = `
	WITH x AS (
		SELECT b.asin, b.title, COALESCE(sr.name, '') AS series, COALESCE(b.series_index, 0) AS idx,
		       sel.asin IS NOT NULL AS selected,
		       ss.asin IS NOT NULL AS shared,
		       COALESCE(v.visible, sr.default_visible, ?) = 1 AS visible
		FROM books b
		LEFT JOIN selections sel ON sel.asin = b.asin AND sel.member_id = ?
		LEFT JOIN shared_state ss ON ss.asin = b.asin AND ss.kids_profile_id = ?
		LEFT JOIN visibility v ON v.asin = b.asin
		LEFT JOIN series sr ON sr.id = b.series_id
		WHERE b.removed_at IS NULL AND (sel.asin IS NOT NULL OR ss.asin IS NOT NULL)
	)
	SELECT asin, title, series, idx,
	       CASE WHEN selected AND visible AND shared THEN 'shared'
	            WHEN selected AND visible THEN 'add'
	            WHEN shared THEN 'remove'
	            ELSE 'held' END AS state
	FROM x`

// ShelfSummaries counts the shelves of every family member.
func (s *Store) ShelfSummaries(ctx context.Context) ([]ShelfSummary, error) {
	ms, err := s.Members(ctx)
	if err != nil {
		return nil, err
	}
	def, err := s.defaultVisible(ctx)
	if err != nil {
		return nil, err
	}
	var out []ShelfSummary
	for _, m := range ms {
		if m.Role != RoleFamily {
			continue
		}
		sum := ShelfSummary{Member: m}
		rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM (`+shelfRows+`) GROUP BY state`, def, m.ID, m.KidsProfileID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var st ShelfState
			var n int
			if err := rows.Scan(&st, &n); err != nil {
				rows.Close()
				return nil, err
			}
			switch st {
			case ShelfShared:
				sum.Shared = n
			case ShelfToAdd:
				sum.ToAdd = n
			case ShelfToRemove:
				sum.ToRemove = n
			case ShelfHeld:
				sum.Held = n
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		sum.Selected = sum.Shared + sum.ToAdd + sum.Held
		out = append(out, sum)
	}
	return out, nil
}

// MemberShelf lists one member's books in a state (ShelfAny for all),
// pending changes first, then by series and title. It returns one more than
// limit when there are more.
func (s *Store) MemberShelf(ctx context.Context, memberID int64, state ShelfState, query string, limit, offset int) ([]ShelfRow, error) {
	m, err := s.MemberByID(ctx, memberID)
	if err != nil {
		return nil, err
	}
	def, err := s.defaultVisible(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT * FROM (`+shelfRows+`)
		WHERE (? = '' OR state = ?) AND (title LIKE ? ESCAPE '\' OR series LIKE ? ESCAPE '\')
		ORDER BY CASE state WHEN 'add' THEN 0 WHEN 'remove' THEN 1 WHEN 'held' THEN 2 ELSE 3 END,
		         series = '', series, idx, title
		LIMIT ? OFFSET ?`,
		def, m.ID, m.KidsProfileID, string(state), string(state), likeArg(query), likeArg(query), limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShelfRow
	for rows.Next() {
		var r ShelfRow
		if err := rows.Scan(&r.ASIN, &r.Title, &r.SeriesName, &r.SeriesIndex, &r.State); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// likeArg turns a search box value into a LIKE pattern ("%" for empty).
func likeArg(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return "%"
	}
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
}
