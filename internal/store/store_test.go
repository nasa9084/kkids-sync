package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestMembers(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.AddMember(ctx, Member{Name: "Owner", Email: "owner@example.com", Role: RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	mom, err := s.AddMember(ctx, Member{Name: "Mom", Email: " Mom@Example.com ", Role: RoleFamily, KidsProfileID: "amzn1.account.A"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMember(ctx, Member{Name: "Dup", Email: "mom@example.com", Role: RoleFamily}); err == nil {
		t.Error("duplicate email (other case) was accepted")
	}
	if _, err := s.AddMember(ctx, Member{Name: "X", Email: "x@example.com", Role: "root"}); err == nil {
		t.Error("invalid role was accepted")
	}

	got, err := s.MemberByEmail(ctx, "MOM@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != mom.ID || got.Role != RoleFamily || got.KidsProfileID != "amzn1.account.A" || got.Email != "Mom@Example.com" {
		t.Errorf("MemberByEmail = %+v", got)
	}
	if _, err := s.MemberByEmail(ctx, "nobody@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown email: err = %v, want ErrNotFound", err)
	}

	all, err := s.Members(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Role != RoleAdmin {
		t.Errorf("Members = %+v", all)
	}
}

func TestMigrateTwice(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	for i := 0; i < 2; i++ {
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		var v string
		if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'default_visible'`).Scan(&v); err != nil || v != "0" {
			t.Fatalf("default_visible = %q, %v", v, err)
		}
		s.Close()
	}
}
