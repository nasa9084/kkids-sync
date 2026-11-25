// Package auth identifies the person behind a request. Cloudflare Access
// signs users in in front of the app; the app still verifies the token
// Access attaches, so a request that reaches it some other way is refused.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/nasa9084/kkids-sync/internal/store"
)

// HeaderAssertion is the header Cloudflare Access sets on proxied requests.
const HeaderAssertion = "Cf-Access-Jwt-Assertion"

// EmailVerifier turns a request into a verified email address.
type EmailVerifier interface {
	VerifyEmail(r *http.Request) (string, error)
}

// AccessVerifier verifies Cloudflare Access application tokens.
type AccessVerifier struct {
	v *oidc.IDTokenVerifier
}

// NewAccessVerifier checks tokens issued by https://<teamDomain> for the
// Access application whose audience tag is aud.
func NewAccessVerifier(ctx context.Context, teamDomain, aud string) (*AccessVerifier, error) {
	teamDomain = strings.TrimSuffix(strings.TrimPrefix(teamDomain, "https://"), "/")
	if teamDomain == "" || aud == "" {
		return nil, errors.New("access team domain and audience are required")
	}
	issuer := "https://" + teamDomain
	keys := oidc.NewRemoteKeySet(ctx, issuer+"/cdn-cgi/access/certs")
	return &AccessVerifier{v: oidc.NewVerifier(issuer, keys, &oidc.Config{ClientID: aud})}, nil
}

// VerifyEmail returns the email in a valid Access token.
func (a *AccessVerifier) VerifyEmail(r *http.Request) (string, error) {
	raw := r.Header.Get(HeaderAssertion)
	if raw == "" {
		return "", errors.New("no access token")
	}
	tok, err := a.v.Verify(r.Context(), raw)
	if err != nil {
		return "", fmt.Errorf("verify access token: %w", err)
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := tok.Claims(&claims); err != nil {
		return "", err
	}
	if claims.Email == "" {
		return "", errors.New("access token has no email")
	}
	return claims.Email, nil
}

// FixedEmail treats every request as coming from one address. For local
// development only.
type FixedEmail string

// VerifyEmail implements EmailVerifier.
func (f FixedEmail) VerifyEmail(*http.Request) (string, error) { return string(f), nil }

// MemberLookup finds a member by email.
type MemberLookup interface {
	MemberByEmail(ctx context.Context, email string) (store.Member, error)
}

type ctxKey struct{}

// Middleware resolves the signed-in member and stores it in the request
// context. Unknown or unverifiable callers get 403.
func Middleware(v EmailVerifier, members MemberLookup, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			email, err := v.VerifyEmail(r)
			if err != nil {
				log.Warn("rejected request", "path", r.URL.Path, "err", err)
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			m, err := members.MemberByEmail(r.Context(), email)
			if errors.Is(err, store.ErrNotFound) {
				log.Warn("unknown member", "path", r.URL.Path)
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if err != nil {
				log.Error("look up member", "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, m)))
		})
	}
}

// WithMember returns ctx with m as the signed-in member. The admin's
// "view as" uses it to show a family member's pages.
func WithMember(ctx context.Context, m store.Member) context.Context {
	return context.WithValue(ctx, ctxKey{}, m)
}

// MemberFrom returns the member Middleware stored.
func MemberFrom(ctx context.Context) (store.Member, bool) {
	m, ok := ctx.Value(ctxKey{}).(store.Member)
	return m, ok
}

// RequireAdmin lets only admins through.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m, ok := MemberFrom(r.Context()); !ok || m.Role != store.RoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
