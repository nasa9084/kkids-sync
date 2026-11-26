// Package webui serves the family pages of ほんだな: search, series and
// "my shelf". Pages are server-rendered; htmx swaps a single card when a
// book goes on or off the shelf.
package webui

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nasa9084/kkids-sync/internal/auth"
	"github.com/nasa9084/kkids-sync/internal/store"
	"github.com/nasa9084/kkids-sync/internal/titles"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

const pageSize = 50

var jst = time.FixedZone("JST", 9*60*60)

// askFinishedAfter is when the shelf asks whether a book has been read.
const askFinishedAfter = 60 * 24 * time.Hour

// Store is what the pages need from the database.
type Store interface {
	SearchBooks(ctx context.Context, memberID int64, query string, limit, offset int) ([]store.BookView, error)
	SeriesBooks(ctx context.Context, memberID, seriesID int64) ([]store.BookView, error)
	ShelfBooks(ctx context.Context, memberID int64, limit, offset int) ([]store.BookView, error)
	ShelfCounts(ctx context.Context, memberID int64) (total, pending int, err error)
	BookFor(ctx context.Context, memberID int64, asin string) (store.BookView, error)
	Select(ctx context.Context, memberID int64, asins ...string) error
	Unselect(ctx context.Context, memberID int64, asins ...string) error
	UnselectAll(ctx context.Context, memberID int64) (int, error)
	Thumbnail(ctx context.Context, asin string) (string, error)
	MemberByID(ctx context.Context, id int64) (store.Member, error)
	FamilySeries(ctx context.Context, memberID int64, query string, byNew bool, limit, offset int) ([]store.SeriesView, error)
}

// Version is set at build time (-ldflags -X) and shown on the admin pages.
var Version = "dev"

// UI serves the family pages.
type UI struct {
	db     Store
	log    *slog.Logger
	pages  map[string]*template.Template
	covers *coverCache
	now    func() time.Time
}

// New parses the templates. coverDir caches book covers on disk.
func New(db Store, coverDir string, log *slog.Logger) (*UI, error) {
	u := &UI{db: db, log: log, pages: map[string]*template.Template{}, now: time.Now}
	funcs := template.FuncMap{
		"short": titles.DisplayTitle,
		"askFinished": func(b store.BookView) bool {
			return !b.SelectedAt.IsZero() && u.now().Sub(b.SelectedAt) > askFinishedAfter
		},
	}
	for _, page := range []string{"search", "series", "shelf", "series_list"} {
		t, err := template.New(page).Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/"+page+".html")
		if err != nil {
			return nil, err
		}
		u.pages[page] = t
	}
	adminFuncs := template.FuncMap{
		"deref":   func(b *bool) bool { return b != nil && *b },
		"fmtTime": func(t time.Time) string { return t.In(jst).Format("1/2 15:04") },
		"version": func() string { return Version },
	}
	for _, page := range []string{"admin_books", "admin_series", "admin_jobs", "admin_shelves"} {
		t, err := template.New(page).Funcs(adminFuncs).ParseFS(templateFS, "templates/admin_layout.html", "templates/"+page+".html")
		if err != nil {
			return nil, err
		}
		u.pages[page] = t
	}
	if err := os.MkdirAll(coverDir, 0o700); err != nil {
		return nil, err
	}
	u.covers = &coverCache{dir: coverDir, db: db, client: &http.Client{Timeout: 15 * time.Second}}
	return u, nil
}

// Routes registers the pages on mux. Requests must already have passed
// auth.Middleware.
func (u *UI) Routes(mux *http.ServeMux) {
	family := func(h http.HandlerFunc) http.Handler { return u.familyOnly(h) }
	mux.Handle("GET /{$}", family(u.search))
	mux.Handle("GET /series/{id}", family(u.series))
	mux.Handle("GET /series", family(u.seriesList))
	mux.Handle("POST /series/{id}/shelf", family(u.seriesShelf))
	mux.Handle("POST /series/{id}/shelf-all", family(u.seriesShelfAll))
	mux.Handle("POST /series/{id}/unshelf-all", family(u.seriesUnshelfAll))
	mux.Handle("POST /shelf/clear", family(u.shelfClear))
	mux.Handle("GET /shelf", family(u.shelf))
	mux.Handle("POST /books/{asin}/shelf", family(u.shelfOne(true)))
	mux.Handle("POST /books/{asin}/unshelf", family(u.shelfOne(false)))
	// Covers are for anyone signed in, admins included.
	mux.HandleFunc("GET /cover/{asin}", u.covers.serve)
}

// StaticHandler serves CSS and htmx. They need no login.
func StaticHandler() http.Handler {
	return http.FileServerFS(staticFS)
}

// viewAsCookie holds the family member an admin is looking through.
const viewAsCookie = "hondana_view_as"

type viewingKey struct{}

// viewMode is how an admin is using a family member's pages.
type viewMode int

const (
	notViewing viewMode = iota
	viewOnly            // look only
	actAs               // change their shelf for them
)

func modeOf(ctx context.Context) viewMode {
	v, _ := ctx.Value(viewingKey{}).(viewMode)
	return v
}

// viewing reports whether an admin is looking at a family member's pages.
func viewing(ctx context.Context) bool { return modeOf(ctx) != notViewing }

// acting reports whether the admin may change that member's shelf.
func acting(ctx context.Context) bool { return modeOf(ctx) == actAs }

// familyOnly sends admins to their own page and stops cross-site posts. An
// admin who chose "view as" on the admin page sees that member's pages,
// read-only.
func (u *UI) familyOnly(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, ok := auth.MemberFrom(r.Context())
		if !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if m.Role != store.RoleFamily {
			as, mode, ok := u.viewAs(r)
			if !ok {
				http.Redirect(w, r, "/admin/", http.StatusSeeOther)
				return
			}
			if r.Method != http.MethodGet && mode != actAs {
				http.Error(w, "見るだけのモードなので、本棚は変えられません", http.StatusForbidden)
				return
			}
			ctx := context.WithValue(auth.WithMember(r.Context(), as), viewingKey{}, mode)
			next(w, r.WithContext(ctx))
			return
		}
		if r.Method == http.MethodPost && !sameOrigin(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

// viewAs returns the family member named by the admin's view-as cookie
// ("<id>" to look, "<id>:act" to change their shelf).
func (u *UI) viewAs(r *http.Request) (store.Member, viewMode, bool) {
	c, err := r.Cookie(viewAsCookie)
	if err != nil {
		return store.Member{}, notViewing, false
	}
	v, mode := c.Value, viewOnly
	if before, ok := strings.CutSuffix(v, ":act"); ok {
		v, mode = before, actAs
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return store.Member{}, notViewing, false
	}
	m, err := u.db.MemberByID(r.Context(), id)
	if err != nil || m.Role != store.RoleFamily {
		return store.Member{}, notViewing, false
	}
	return m, mode, true
}

// sameOrigin rejects posts from other sites. Browsers send Origin on POST.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return r.Header.Get("X-Requested-By") == "hondana"
	}
	ou, err := url.Parse(o)
	return err == nil && ou.Host == r.Host
}

type pageData struct {
	Title, Nav, Query string
	Viewing           bool // an admin looking through "view as"
	Acting            bool // ... and allowed to change the shelf
	Member            store.Member
	Books             []store.BookView
	PrevURL, NextURL  string
	// series page
	SeriesID     int64
	OnShelfCount int
	NextThree    []string
	Rest         []string
	Missing      int // volumes not on the shelf yet
	SeriesList   []store.SeriesView
	ByNew        bool
	// shelf page
	Total, Pending int
}

func (u *UI) render(w http.ResponseWriter, page string, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := u.pages[page].ExecuteTemplate(w, "layout", d); err != nil {
		u.log.Error("render", "page", page, "err", err)
	}
}

func (u *UI) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, nil)
		return
	}
	u.log.Error("request failed", "err", err)
	http.Error(w, "エラーが起きました。少し待ってからやり直してください。", http.StatusInternalServerError)
}

func (u *UI) search(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MemberFrom(r.Context())
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 0)
	books, err := u.db.SearchBooks(r.Context(), m.ID, q, pageSize, page*pageSize)
	if err != nil {
		u.fail(w, err)
		return
	}
	d := pageData{Title: "本をさがす", Nav: "search", Query: q, Member: m, Viewing: viewing(r.Context()), Acting: acting(r.Context())}
	if len(books) > pageSize {
		books = books[:pageSize]
		d.NextURL = pageURL(q, page+1)
	}
	if page > 0 {
		d.PrevURL = pageURL(q, page-1)
	}
	d.Books = books
	u.render(w, "search", d)
}

func pageURL(q string, page int) string {
	v := url.Values{}
	if q != "" {
		v.Set("q", q)
	}
	if page > 0 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return "/"
	}
	return "/?" + v.Encode()
}

func (u *UI) series(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MemberFrom(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	books, err := u.db.SeriesBooks(r.Context(), m.ID, id)
	if err != nil {
		u.fail(w, err)
		return
	}
	if len(books) == 0 {
		http.NotFound(w, r)
		return
	}
	d := pageData{Title: books[0].SeriesName, Nav: "series", Member: m, Books: books, SeriesID: id, Viewing: viewing(r.Context()), Acting: acting(r.Context())}
	// "Next" means the volumes after the last one on the shelf.
	last := -1
	for i, b := range books {
		if b.OnShelf {
			d.OnShelfCount++
			last = i
		}
	}
	for _, b := range books[last+1:] {
		if !b.OnShelf {
			d.Rest = append(d.Rest, b.ASIN)
		}
	}
	for _, b := range books {
		if !b.OnShelf {
			d.Missing++
		}
	}
	d.NextThree = d.Rest[:min(3, len(d.Rest))]
	u.render(w, "series", d)
}

func (u *UI) seriesShelf(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MemberFrom(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	asins := r.PostForm["asin"]
	if len(asins) == 0 || len(asins) > 200 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := u.db.Select(r.Context(), m.ID, asins...); err != nil {
		u.fail(w, err)
		return
	}
	http.Redirect(w, r, "/series/"+r.PathValue("id"), http.StatusSeeOther)
}

// seriesList lists the series the member can see.
func (u *UI) seriesList(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MemberFrom(r.Context())
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	byNew := r.URL.Query().Get("sort") == "new"
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 0)
	list, err := u.db.FamilySeries(r.Context(), m.ID, q, byNew, pageSize, page*pageSize)
	if err != nil {
		u.fail(w, err)
		return
	}
	d := pageData{Title: "シリーズ", Nav: "series", Query: q, Member: m, ByNew: byNew, Viewing: viewing(r.Context()), Acting: acting(r.Context())}
	link := func(p int) string {
		v := url.Values{}
		if q != "" {
			v.Set("q", q)
		}
		if byNew {
			v.Set("sort", "new")
		}
		if p > 0 {
			v.Set("page", strconv.Itoa(p))
		}
		if len(v) == 0 {
			return "/series"
		}
		return "/series?" + v.Encode()
	}
	if len(list) > pageSize {
		list = list[:pageSize]
		d.NextURL = link(page + 1)
	}
	if page > 0 {
		d.PrevURL = link(page - 1)
	}
	d.SeriesList = list
	u.render(w, "series_list", d)
}

// seriesShelfAll puts every volume of a series the member can see on their
// shelf. The server picks the books, so the form carries no list.
func (u *UI) seriesShelfAll(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MemberFrom(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	books, err := u.db.SeriesBooks(r.Context(), m.ID, id)
	if err != nil {
		u.fail(w, err)
		return
	}
	var asins []string
	for _, b := range books {
		if !b.OnShelf {
			asins = append(asins, b.ASIN)
		}
	}
	if err := u.db.Select(r.Context(), m.ID, asins...); err != nil {
		u.fail(w, err)
		return
	}
	http.Redirect(w, r, "/series/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// seriesUnshelfAll takes every volume of a series off the member's shelf.
func (u *UI) seriesUnshelfAll(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MemberFrom(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	books, err := u.db.SeriesBooks(r.Context(), m.ID, id)
	if err != nil {
		u.fail(w, err)
		return
	}
	var asins []string
	for _, b := range books {
		if b.OnShelf {
			asins = append(asins, b.ASIN)
		}
	}
	if err := u.db.Unselect(r.Context(), m.ID, asins...); err != nil {
		u.fail(w, err)
		return
	}
	http.Redirect(w, r, "/series/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// shelfClear empties the member's shelf.
func (u *UI) shelfClear(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MemberFrom(r.Context())
	if _, err := u.db.UnselectAll(r.Context(), m.ID); err != nil {
		u.fail(w, err)
		return
	}
	http.Redirect(w, r, "/shelf", http.StatusSeeOther)
}

func (u *UI) shelf(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MemberFrom(r.Context())
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 0)
	books, err := u.db.ShelfBooks(r.Context(), m.ID, pageSize, page*pageSize)
	if err != nil {
		u.fail(w, err)
		return
	}
	d := pageData{Title: "わたしの本棚", Nav: "shelf", Member: m, Viewing: viewing(r.Context()), Acting: acting(r.Context())}
	if d.Total, d.Pending, err = u.db.ShelfCounts(r.Context(), m.ID); err != nil {
		u.fail(w, err)
		return
	}
	if len(books) > pageSize {
		books = books[:pageSize]
		d.NextURL = "/shelf?page=" + strconv.Itoa(page+1)
	}
	if page > 0 {
		d.PrevURL = "/shelf?page=" + strconv.Itoa(page-1)
	}
	d.Books = books
	u.render(w, "shelf", d)
}

// shelfOne puts one book on or off the shelf and returns its updated card.
func (u *UI) shelfOne(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, _ := auth.MemberFrom(r.Context())
		asin := r.PathValue("asin")
		var err error
		if on {
			err = u.db.Select(r.Context(), m.ID, asin)
		} else {
			err = u.db.Unselect(r.Context(), m.ID, asin)
		}
		if err != nil {
			u.fail(w, err)
			return
		}
		b, err := u.db.BookFor(r.Context(), m.ID, asin)
		if errors.Is(err, store.ErrNotFound) && !on {
			// Taken off and no longer visible: nothing to show.
			return
		}
		if err != nil {
			u.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := u.pages["search"].ExecuteTemplate(w, "book", b); err != nil {
			u.log.Error("render card", "err", err)
		}
	}
}

// coverCache serves covers from disk, fetching each from Amazon's image CDN
// once, so family members' browsers never talk to Amazon.
type coverCache struct {
	dir    string
	db     Store
	client *http.Client
}

var (
	asinPattern = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	coverHosts  = map[string]bool{"images-na.ssl-images-amazon.com": true, "m.media-amazon.com": true}
)

func (c *coverCache) serve(w http.ResponseWriter, r *http.Request) {
	asin := r.PathValue("asin")
	if !asinPattern.MatchString(asin) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(c.dir, asin+".jpg")
	if _, err := os.Stat(path); err != nil {
		if err := c.fetch(r.Context(), asin, path); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("Cache-Control", "private, max-age=604800")
	http.ServeFile(w, r, path)
}

func (c *coverCache) fetch(ctx context.Context, asin, path string) error {
	base, err := c.db.Thumbnail(ctx, asin)
	if err != nil || base == "" {
		return fmt.Errorf("no thumbnail for %s", asin)
	}
	u, err := url.Parse(base + "._SY200_.jpg")
	if err != nil || u.Scheme != "https" || !coverHosts[u.Host] {
		return fmt.Errorf("unexpected thumbnail URL for %s", asin)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
		return fmt.Errorf("cover %s: %s", asin, resp.Status)
	}
	tmp, err := os.CreateTemp(c.dir, asin+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, io.LimitReader(resp.Body, 2<<20)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
