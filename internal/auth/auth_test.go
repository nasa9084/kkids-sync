package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nasa9084/kkids-sync/internal/store"
)

type fakeMembers map[string]store.Member

func (f fakeMembers) MemberByEmail(_ context.Context, email string) (store.Member, error) {
	if m, ok := f[email]; ok {
		return m, nil
	}
	return store.Member{}, store.ErrNotFound
}

type failingVerifier struct{}

func (failingVerifier) VerifyEmail(*http.Request) (string, error) { return "", errors.New("bad token") }

func TestMiddleware(t *testing.T) {
	members := fakeMembers{
		"mom@example.com":   {ID: 1, Name: "Mom", Role: store.RoleFamily},
		"owner@example.com": {ID: 2, Name: "Owner", Role: store.RoleAdmin},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	whoami := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, _ := MemberFrom(r.Context())
		io.WriteString(w, m.Name)
	})

	tests := []struct {
		name     string
		v        EmailVerifier
		admin    bool
		wantCode int
		wantBody string
	}{
		{"family", FixedEmail("mom@example.com"), false, 200, "Mom"},
		{"unknown", FixedEmail("stranger@example.com"), false, 403, ""},
		{"bad token", failingVerifier{}, false, 403, ""},
		{"family on admin page", FixedEmail("mom@example.com"), true, 403, ""},
		{"admin on admin page", FixedEmail("owner@example.com"), true, 200, "Owner"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h http.Handler = whoami
			if tt.admin {
				h = RequireAdmin(h)
			}
			h = Middleware(tt.v, members, log)(h)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantCode == 200 && rec.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestNewAccessVerifierNeedsConfig(t *testing.T) {
	if _, err := NewAccessVerifier(context.Background(), "", "aud"); err == nil {
		t.Error("want error without team domain")
	}
	if _, err := NewAccessVerifier(context.Background(), "team.cloudflareaccess.com", ""); err == nil {
		t.Error("want error without audience")
	}
}
