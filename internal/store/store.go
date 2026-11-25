// Package store keeps kkids-sync's state in SQLite. Only kkids-sync-web
// opens the database; the reconciler goes through web's internal API.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store wraps the database.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (creating if needed) the database at path and applies any
// pending migrations. Use ":memory:" in tests.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if path != ":memory:" {
		dsn += "&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite serialises writes anyway, and ":memory:" is
	// per connection.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var done int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&done); err != nil {
			return err
		}
		if done > 0 {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, s.stamp()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) stamp() string { return s.now().UTC().Format(time.RFC3339) }

// Role of a member.
type Role string

const (
	RoleFamily Role = "family"
	RoleAdmin  Role = "admin"
)

// Member is a person who can sign in.
type Member struct {
	ID            int64
	Name          string
	Email         string
	Role          Role
	KidsProfileID string // empty for the admin, or until linked by name
	// KidsProfileName is the profile's name on the parent dashboard.
	KidsProfileName string
}

// ErrNotFound is returned when a lookup matches nothing.
var ErrNotFound = errors.New("not found")

// AddMember inserts a member.
func (s *Store) AddMember(ctx context.Context, m Member) (Member, error) {
	if m.Role != RoleFamily && m.Role != RoleAdmin {
		return Member{}, fmt.Errorf("invalid role %q", m.Role)
	}
	m.Email = strings.TrimSpace(m.Email)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO members (name, email, role, kids_profile_id, kids_profile_name) VALUES (?, ?, ?, ?, ?)`,
		m.Name, m.Email, string(m.Role), nullString(m.KidsProfileID), nullString(m.KidsProfileName))
	if err != nil {
		return Member{}, fmt.Errorf("add member: %w", err)
	}
	m.ID, err = res.LastInsertId()
	return m, err
}

// MemberByEmail finds a member by email, ignoring case.
func (s *Store) MemberByEmail(ctx context.Context, email string) (Member, error) {
	return s.scanMember(s.db.QueryRowContext(ctx,
		`SELECT id, name, email, role, COALESCE(kids_profile_id, ''), COALESCE(kids_profile_name, '') FROM members WHERE email = ?`,
		strings.TrimSpace(email)))
}

// Members lists everyone, admins first.
func (s *Store) Members(ctx context.Context) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, email, role, COALESCE(kids_profile_id, ''), COALESCE(kids_profile_name, '') FROM members ORDER BY role = 'family', id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		m, err := s.scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func (s *Store) scanMember(r scanner) (Member, error) {
	var m Member
	var role string
	if err := r.Scan(&m.ID, &m.Name, &m.Email, &role, &m.KidsProfileID, &m.KidsProfileName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Member{}, ErrNotFound
		}
		return Member{}, err
	}
	m.Role = Role(role)
	return m, nil
}

// execQuerier is satisfied by *sql.DB and *sql.Tx.
type execQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Profile ties a family member's dashboard profile name to its ID.
type Profile struct {
	Name string `json:"name"`
	ID   string `json:"id,omitempty"`
}

// Profiles lists the family members' profiles that have a name.
func (s *Store) Profiles(ctx context.Context) ([]Profile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kids_profile_name, COALESCE(kids_profile_id, '') FROM members WHERE role = 'family' AND kids_profile_name IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Profile
	for rows.Next() {
		var p Profile
		if err := rows.Scan(&p.Name, &p.ID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LinkProfiles fills in kids_profile_id for members registered by profile
// name. A name already linked to a different ID is an error: profiles are
// not expected to change their IDs.
func (s *Store) LinkProfiles(ctx context.Context, ids map[string]string) (linked int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for name, id := range ids {
		var cur sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT kids_profile_id FROM members WHERE kids_profile_name = ? AND role = 'family'`, name).Scan(&cur)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if cur.Valid && cur.String == id {
			continue
		}
		if cur.Valid {
			return 0, fmt.Errorf("profile %q is linked to another ID already", name)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE members SET kids_profile_id = ? WHERE kids_profile_name = ?`, id, name); err != nil {
			return 0, fmt.Errorf("link %q: %w", name, err)
		}
		linked++
	}
	return linked, tx.Commit()
}
