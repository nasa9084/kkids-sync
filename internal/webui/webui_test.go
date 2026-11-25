package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nasa9084/kkids-sync/internal/auth"
	"github.com/nasa9084/kkids-sync/internal/store"
)

func setup(t *testing.T) (http.Handler, func(email string) http.Handler) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.AddMember(ctx, store.Member{Name: "母", Email: "mom@example.com", Role: store.RoleFamily, KidsProfileID: "P1"})
	db.AddMember(ctx, store.Member{Name: "自分", Email: "owner@example.com", Role: store.RoleAdmin})
	if _, err := db.ApplySnapshot(ctx, store.LibrarySnapshot{
		Books:    []store.Book{{ASIN: "B000000001", Title: "港町ミステリー 1"}, {ASIN: "B000000002", Title: "港町ミステリー 2"}},
		Shared:   map[string][]string{"P1": {"B000000001"}},
		Complete: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BootstrapFromShared(ctx); err != nil {
		t.Fatal(err)
	}
	// Make volume 2 visible too.
	if err := setVisible(db, "B000000002"); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ui, err := New(db, t.TempDir(), log)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ui.Routes(mux)
	ui.AdminRoutes(mux, db)
	as := func(email string) http.Handler {
		return auth.Middleware(auth.FixedEmail(email), db, log)(mux)
	}
	return mux, as
}

// setVisible marks a book visible for the family.
func setVisible(db *store.Store, asin string) error {
	return db.SetVisibility(context.Background(), asin, true)
}

func do(h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPages(t *testing.T) {
	_, as := setup(t)
	mom := as("mom@example.com")

	rec := do(mom, "GET", "/", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "港町ミステリー 2") {
		t.Fatalf("search page: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(mom, "GET", "/shelf", nil)
	if !strings.Contains(rec.Body.String(), "港町ミステリー 1") || strings.Contains(rec.Body.String(), "港町ミステリー 2") {
		t.Errorf("shelf page shows the wrong books")
	}

	// Cross-site post is refused; same-origin htmx post works.
	if rec := do(mom, "POST", "/books/B000000002/shelf", map[string]string{"Origin": "https://evil.example"}); rec.Code != 403 {
		t.Errorf("cross-site post: %d", rec.Code)
	}
	rec = do(mom, "POST", "/books/B000000002/shelf", map[string]string{"Origin": "http://example.com"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "準備中") {
		t.Errorf("shelf post: %d %s", rec.Code, rec.Body.String())
	}

	if rec := do(as("owner@example.com"), "GET", "/", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("admin on family page: %d, want redirect", rec.Code)
	}
	if rec := do(mom, "GET", "/cover/not-an-asin", nil); rec.Code != 404 {
		t.Errorf("bad cover asin: %d", rec.Code)
	}
}

func TestAdminPages(t *testing.T) {
	_, as := setup(t)
	owner := as("owner@example.com")
	mom := as("mom@example.com")

	if rec := do(mom, "GET", "/admin/", nil); rec.Code != 403 {
		t.Errorf("family on admin page: %d", rec.Code)
	}
	rec := do(owner, "GET", "/admin/?f=all", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "港町ミステリー 2") {
		t.Fatalf("admin books: %d %s", rec.Code, rec.Body.String())
	}
	for _, p := range []string{"/admin/series", "/admin/jobs", "/admin/", "/admin/shelves", "/admin/shelves?s=add"} {
		if rec := do(owner, "GET", p, nil); !strings.Contains(rec.Body.String(), "ほんだな dev") {
			t.Errorf("%s: no version", p)
		}
		if rec := do(owner, "GET", p, nil); rec.Code != 200 {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}

	req := httptest.NewRequest("POST", "/admin/books/B000000002/visibility", strings.NewReader("v=0"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	rec = httptest.NewRecorder()
	owner.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `class="off"`) {
		t.Errorf("hide book: %d %s", rec.Code, rec.Body.String())
	}
	// Mom can no longer see it.
	if rec := do(mom, "GET", "/?q=港町", nil); strings.Contains(rec.Body.String(), "港町ミステリー 2") {
		t.Error("hidden book still visible to family")
	}

	req = httptest.NewRequest("POST", "/admin/books/B000000002/visibility", strings.NewReader("v=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	owner.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("cross-site admin post: %d", rec.Code)
	}
}

func TestAdminSeriesRules(t *testing.T) {
	_, as := setup(t)
	owner := as("owner@example.com")
	post := func(target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://example.com")
		rec := httptest.NewRecorder()
		owner.ServeHTTP(rec, req)
		return rec
	}
	rec := post("/admin/series/rules", "pattern=%5E%E6%B8%AF%E7%94%BA&series=%E6%B8%AF%E7%94%BA&preview=1") // ^港町 → 港町
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ルール適用後") {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/admin/series/rules", "pattern=(&series=x&preview=1"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "パターンが正しくありません") {
		t.Errorf("bad pattern: %d", rec.Code)
	}
	if rec := post("/admin/series/rules", "pattern=%5E%E6%B8%AF%E7%94%BA&series=%E6%B8%AF%E7%94%BA&preview=0"); rec.Code != 303 {
		t.Errorf("save rule: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/admin/series/reclassify", ""); rec.Code != 303 {
		t.Errorf("reclassify: %d", rec.Code)
	}
	if rec := do(owner, "GET", "/admin/series", nil); !strings.Contains(rec.Body.String(), "<code>^港町</code>") {
		t.Errorf("rule not listed: %s", rec.Body.String())
	}
}

func TestAdminViewAs(t *testing.T) {
	_, as := setup(t)
	owner := as("owner@example.com")
	mom := as("mom@example.com")

	// Without view-as the admin is sent to the admin page.
	if rec := do(owner, "GET", "/shelf", nil); rec.Code != 303 {
		t.Fatalf("admin on family page: %d", rec.Code)
	}
	rec := do(owner, "POST", "/admin/view-as/1", map[string]string{"Origin": "http://example.com"})
	if rec.Code != 303 {
		t.Fatalf("view-as: %d", rec.Code)
	}
	cookie := rec.Result().Cookies()[0]
	hdr := map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}

	rec = do(owner, "GET", "/shelf", hdr)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "母さんの画面を見ています") || !strings.Contains(rec.Body.String(), "港町ミステリー 1") {
		t.Fatalf("view-as shelf: %d %s", rec.Code, rec.Body.String())
	}
	hdr["Origin"] = "http://example.com"
	if rec := do(owner, "POST", "/books/B000000002/shelf", hdr); rec.Code != 403 {
		t.Errorf("view-as post: %d, want 403", rec.Code)
	}
	// The cookie means nothing to a family member, and the admin page still works.
	if rec := do(mom, "GET", "/shelf", hdr); strings.Contains(rec.Body.String(), "画面を見ています") {
		t.Error("family member got the view-as banner")
	}
	if rec := do(owner, "GET", "/admin/view-as/end", hdr); rec.Code != 303 {
		t.Errorf("end view-as: %d", rec.Code)
	}
}

func TestSeriesShelfAll(t *testing.T) {
	_, as := setup(t)
	mom := as("mom@example.com")
	rec := do(mom, "GET", "/?q=港町", nil)
	if !strings.Contains(rec.Body.String(), "/shelf-all") {
		t.Fatalf("no whole-series button on the book card: %s", rec.Body.String())
	}
	// Volume 1 is on the shelf (bootstrap), volume 2 is not.
	rec = do(mom, "GET", "/series/1", nil)
	if !strings.Contains(rec.Body.String(), "あと1冊") {
		t.Fatalf("series page: %s", rec.Body.String())
	}
	if rec := do(mom, "POST", "/series/1/shelf-all", map[string]string{"Origin": "http://example.com"}); rec.Code != 303 {
		t.Fatalf("shelf-all: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(mom, "GET", "/shelf", nil)
	if !strings.Contains(rec.Body.String(), "港町ミステリー 2") {
		t.Errorf("volume 2 not on the shelf: %s", rec.Body.String())
	}
}

func TestUnshelfSeriesAndClear(t *testing.T) {
	_, as := setup(t)
	mom := as("mom@example.com")
	post := map[string]string{"Origin": "http://example.com"}
	do(mom, "POST", "/series/1/shelf-all", post)
	if rec := do(mom, "GET", "/series/1", nil); !strings.Contains(rec.Body.String(), "全部本棚から外す(2冊)") {
		t.Fatalf("series page: %s", rec.Body.String())
	}
	if rec := do(mom, "POST", "/series/1/unshelf-all", post); rec.Code != 303 {
		t.Fatalf("unshelf-all: %d", rec.Code)
	}
	if rec := do(mom, "GET", "/shelf", nil); !strings.Contains(rec.Body.String(), "本棚の本 0冊") {
		t.Errorf("shelf after unshelf-all: %s", rec.Body.String())
	}
	do(mom, "POST", "/series/1/shelf-all", post)
	if rec := do(mom, "GET", "/shelf", nil); !strings.Contains(rec.Body.String(), "本棚を全部空にする") {
		t.Fatalf("no clear button: %s", rec.Body.String())
	}
	if rec := do(mom, "POST", "/shelf/clear", post); rec.Code != 303 {
		t.Fatalf("clear: %d", rec.Code)
	}
	if rec := do(mom, "GET", "/shelf", nil); !strings.Contains(rec.Body.String(), "本棚の本 0冊") {
		t.Errorf("shelf after clear: %s", rec.Body.String())
	}
}

func TestSeriesList(t *testing.T) {
	_, as := setup(t)
	mom := as("mom@example.com")
	rec := do(mom, "GET", "/series", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `href="/series/1"`) || !strings.Contains(rec.Body.String(), "全2巻") {
		t.Fatalf("series list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(mom, "GET", "/series?q=存在しない", nil); !strings.Contains(rec.Body.String(), "見つかりませんでした") {
		t.Errorf("search miss: %s", rec.Body.String())
	}
	if rec := do(mom, "GET", "/series?sort=new", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `href="/series/1"`) {
		t.Errorf("sort=new: %d", rec.Code)
	}
}
