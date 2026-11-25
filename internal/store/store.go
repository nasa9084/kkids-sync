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
	KidsProfileID string // empty for the admin
}

// ErrNotFound is returned when a lookup matches nothing.
var ErrNotFound = errors.New("not found")

// AddMember inserts a member.
func (s *Store) AddMember(ctx context.Context, m Member) (Member, error) {
	if m.Role != RoleFamily && m.Role != RoleAdmin {
		return Member{}, fmt.Errorf("invalid role %q", m.Role)
	}
	m.Email = strings.TrimSpace(m.Email)
	var profile any
	if m.KidsProfileID != "" {
		profile = m.KidsProfileID
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO members (name, email, role, kids_profile_id) VALUES (?, ?, ?, ?)`,
		m.Name, m.Email, string(m.Role), profile)
	if err != nil {
		return Member{}, fmt.Errorf("add member: %w", err)
	}
	m.ID, err = res.LastInsertId()
	return m, err
}

// MemberByEmail finds a member by email, ignoring case.
func (s *Store) MemberByEmail(ctx context.Context, email string) (Member, error) {
	return s.scanMember(s.db.QueryRowContext(ctx,
		`SELECT id, name, email, role, COALESCE(kids_profile_id, '') FROM members WHERE email = ?`,
		strings.TrimSpace(email)))
}

// Members lists everyone, admins first.
func (s *Store) Members(ctx context.Context) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, email, role, COALESCE(kids_profile_id, '') FROM members ORDER BY role = 'family', id`)
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
	if err := r.Scan(&m.ID, &m.Name, &m.Email, &role, &m.KidsProfileID); err != nil {
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
