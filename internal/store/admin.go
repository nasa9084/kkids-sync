package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// AdminBook is a row of the admin book list.
type AdminBook struct {
	ASIN          string
	Title         string
	SeriesID      int64
	SeriesName    string
	SeriesIndex   int
	Explicit      *bool // the book's own setting
	SeriesDefault *bool
	Visible       bool // what family members see
	SharedWith    int  // family profiles Amazon shares it with
}

// BookFilter narrows the admin book list.
type BookFilter string

const (
	FilterAll     BookFilter = "all"
	FilterUnset   BookFilter = "unset"   // no setting of its own and no series default
	FilterVisible BookFilter = "visible" // effectively visible
	FilterHidden  BookFilter = "hidden"  // effectively hidden
)

// AdminBooks lists books for the admin page, newest first. It returns one
// more than limit when there are more.
func (s *Store) AdminBooks(ctx context.Context, filter BookFilter, query string, limit, offset int) ([]AdminBook, error) {
	return s.adminBooks(ctx, filter, query, "", limit, offset)
}

func (s *Store) adminBooks(ctx context.Context, filter BookFilter, query, asin string, limit, offset int) ([]AdminBook, error) {
	def, err := s.defaultVisible(ctx)
	if err != nil {
		return nil, err
	}
	where := []string{"b.removed_at IS NULL"}
	args := []any{def}
	if asin != "" {
		where = append(where, "b.asin = ?")
		args = append(args, asin)
	}
	switch filter {
	case FilterUnset:
		where = append(where, "v.visible IS NULL AND sr.default_visible IS NULL")
	case FilterVisible:
		where = append(where, "COALESCE(v.visible, sr.default_visible, ?) = 1")
		args = append(args, def)
	case FilterHidden:
		where = append(where, "COALESCE(v.visible, sr.default_visible, ?) = 0")
		args = append(args, def)
	}
	if q := strings.TrimSpace(query); q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
		where = append(where, `(b.title LIKE ? ESCAPE '\' OR sr.name LIKE ? ESCAPE '\')`)
		args = append(args, like, like)
	}
	args = append(args, limit+1, offset)
	rows, err := s.db.QueryContext(ctx, `
		SELECT b.asin, b.title, COALESCE(b.series_id, 0), COALESCE(sr.name, ''), COALESCE(b.series_index, 0),
		       v.visible, sr.default_visible,
		       COALESCE(v.visible, sr.default_visible, ?),
		       (SELECT COUNT(*) FROM shared_state ss JOIN members m ON m.kids_profile_id = ss.kids_profile_id
		         WHERE ss.asin = b.asin AND m.role = 'family')
		FROM books b
		LEFT JOIN series sr ON sr.id = b.series_id
		LEFT JOIN visibility v ON v.asin = b.asin
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY b.first_seen_at DESC, b.rowid ASC
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminBook
	for rows.Next() {
		var b AdminBook
		var explicit, sdef sql.NullInt64
		if err := rows.Scan(&b.ASIN, &b.Title, &b.SeriesID, &b.SeriesName, &b.SeriesIndex, &explicit, &sdef, &b.Visible, &b.SharedWith); err != nil {
			return nil, err
		}
		b.Explicit = nullBool(explicit)
		b.SeriesDefault = nullBool(sdef)
		out = append(out, b)
	}
	return out, rows.Err()
}

// AdminBook returns one row of the admin list.
func (s *Store) AdminBook(ctx context.Context, asin string) (AdminBook, error) {
	bs, err := s.adminBooks(ctx, FilterAll, "", asin, 1, 0)
	if err != nil {
		return AdminBook{}, err
	}
	if len(bs) == 0 {
		return AdminBook{}, ErrNotFound
	}
	return bs[0], nil
}

func nullBool(n sql.NullInt64) *bool {
	if !n.Valid {
		return nil
	}
	v := n.Int64 == 1
	return &v
}

func triArg(v *bool) any {
	if v == nil {
		return nil
	}
	if *v {
		return 1
	}
	return 0
}

// SetBookVisibility sets a book's own setting; nil follows the series and
// global defaults again.
func (s *Store) SetBookVisibility(ctx context.Context, asin string, v *bool) error {
	if v == nil {
		_, err := s.db.ExecContext(ctx, `DELETE FROM visibility WHERE asin = ?`, asin)
		return err
	}
	return s.SetVisibility(ctx, asin, *v)
}

// SeriesRow is a row of the admin series list.
type SeriesRow struct {
	ID      int64
	Name    string
	Books   int
	Default *bool
}

// SeriesList lists series that have books in the library, by name.
func (s *Store) SeriesList(ctx context.Context, query string) ([]SeriesRow, error) {
	like := "%"
	if q := strings.TrimSpace(query); q != "" {
		like = "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT sr.id, sr.name, COUNT(b.asin), sr.default_visible
		FROM series sr JOIN books b ON b.series_id = sr.id AND b.removed_at IS NULL
		WHERE sr.name LIKE ? ESCAPE '\'
		GROUP BY sr.id ORDER BY sr.name`, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SeriesRow
	for rows.Next() {
		var r SeriesRow
		var def sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Name, &r.Books, &def); err != nil {
			return nil, err
		}
		r.Default = nullBool(def)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetSeriesDefault sets whether new and unset books of a series are shown.
func (s *Store) SetSeriesDefault(ctx context.Context, id int64, v *bool) error {
	r, err := s.db.ExecContext(ctx, `UPDATE series SET default_visible = ? WHERE id = ?`, triArg(v), id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetBookSeries moves a book into the named series (created if needed) at
// the given volume, or out of any series when name is empty. Books moved by
// hand keep their series on later syncs.
func (s *Store) SetBookSeries(ctx context.Context, asin, name string, index int) error {
	name = strings.TrimSpace(name)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var r sql.Result
	if name == "" {
		// series_index -1 marks "not a series" so assignSeries leaves it alone.
		r, err = tx.ExecContext(ctx, `UPDATE books SET series_id = NULL, series_index = -1 WHERE asin = ?`, asin)
	} else {
		if _, err = tx.ExecContext(ctx, `INSERT INTO series (name) VALUES (?) ON CONFLICT (name) DO NOTHING`, name); err != nil {
			return err
		}
		r, err = tx.ExecContext(ctx, `UPDATE books SET series_id = (SELECT id FROM series WHERE name = ?), series_index = ? WHERE asin = ?`, name, index, asin)
	}
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// DefaultVisible reports the global default for books with no setting.
func (s *Store) DefaultVisible(ctx context.Context) (bool, error) {
	v, err := s.defaultVisible(ctx)
	return v == 1, err
}

// SetDefaultVisible changes the global default.
func (s *Store) SetDefaultVisible(ctx context.Context, v bool) error {
	val := "0"
	if v {
		val = "1"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE settings SET value = ? WHERE key = 'default_visible'`, val)
	return err
}

// Job is one entry of the job history.
type Job struct {
	ID         int64
	Kind       string
	StartedAt  time.Time
	FinishedAt time.Time
	Result     string
	Detail     string
}

// Jobs lists the most recent jobs.
func (s *Store) Jobs(ctx context.Context, limit int) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, started_at, COALESCE(finished_at, ''), COALESCE(result, ''), detail FROM jobs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		var started, finished string
		if err := rows.Scan(&j.ID, &j.Kind, &started, &finished, &j.Result, &j.Detail); err != nil {
			return nil, err
		}
		j.StartedAt, _ = time.Parse(time.RFC3339, started)
		j.FinishedAt, _ = time.Parse(time.RFC3339, finished)
		out = append(out, j)
	}
	return out, rows.Err()
}

// Overview is the summary at the top of the admin page.
type Overview struct {
	Books        int
	Unset        int
	Series       int
	DefaultShown bool
	LastSync     *Job
	ToAdd        int
	ToRemove     int
	// Outside lists shares of titles that are not in the library (Kindle
	// Unlimited and the like); they are never removed automatically.
	Outside []OutsideShare
}

// OutsideShare is a share of a title that is not in the library.
type OutsideShare struct {
	Member string
	ASIN   string
}

// Overview gathers the numbers for the admin summary.
func (s *Store) Overview(ctx context.Context) (Overview, error) {
	var o Overview
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN v.visible IS NULL AND sr.default_visible IS NULL THEN 1 ELSE 0 END), 0),
		       COUNT(DISTINCT b.series_id)
		FROM books b LEFT JOIN visibility v ON v.asin = b.asin LEFT JOIN series sr ON sr.id = b.series_id
		WHERE b.removed_at IS NULL`).Scan(&o.Books, &o.Unset, &o.Series); err != nil {
		return o, err
	}
	var err error
	if o.DefaultShown, err = s.DefaultVisible(ctx); err != nil {
		return o, err
	}
	jobs, err := s.Jobs(ctx, 50)
	if err != nil {
		return o, err
	}
	for i := range jobs {
		if jobs[i].Kind == "sync" {
			o.LastSync = &jobs[i]
			break
		}
	}
	plan, err := s.Plan(ctx)
	if err != nil {
		return o, err
	}
	for _, d := range plan {
		o.ToAdd += len(d.Add)
		o.ToRemove += len(d.Remove)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.name, ss.asin FROM shared_state ss
		JOIN members m ON m.kids_profile_id = ss.kids_profile_id AND m.role = 'family'
		LEFT JOIN books b ON b.asin = ss.asin AND b.removed_at IS NULL
		WHERE b.asin IS NULL ORDER BY m.id, ss.asin`)
	if err != nil {
		return o, err
	}
	defer rows.Close()
	for rows.Next() {
		var x OutsideShare
		if err := rows.Scan(&x.Member, &x.ASIN); err != nil {
			return o, err
		}
		o.Outside = append(o.Outside, x)
	}
	return o, rows.Err()
}

// ParseTri reads "", "1" or "0" from a form.
func ParseTri(v string) (*bool, error) {
	switch v {
	case "":
		return nil, nil
	case "1":
		t := true
		return &t, nil
	case "0":
		f := false
		return &f, nil
	}
	return nil, errors.New("invalid value")
}
