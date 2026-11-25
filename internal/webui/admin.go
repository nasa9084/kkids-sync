package webui

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/nasa9084/kkids-sync/internal/auth"
	"github.com/nasa9084/kkids-sync/internal/store"
)

// AdminStore is what the admin pages need from the database.
type AdminStore interface {
	Overview(ctx context.Context) (store.Overview, error)
	AdminBooks(ctx context.Context, filter store.BookFilter, query string, limit, offset int) ([]store.AdminBook, error)
	AdminBook(ctx context.Context, asin string) (store.AdminBook, error)
	SetBookVisibility(ctx context.Context, asin string, v *bool) error
	SetBookSeries(ctx context.Context, asin, name string, index int) error
	SeriesList(ctx context.Context, query string) ([]store.SeriesRow, error)
	SetSeriesDefault(ctx context.Context, id int64, v *bool) error
	SetDefaultVisible(ctx context.Context, v bool) error
	Jobs(ctx context.Context, limit int) ([]store.Job, error)
}

type filterLink struct {
	Label, URL string
	Current    bool
}

type adminData struct {
	Title, Nav, Query string
	Member            store.Member
	Overview          store.Overview
	Filter            store.BookFilter
	Filters           []filterLink
	Books             []store.AdminBook
	Series            []store.SeriesRow
	Jobs              []store.Job
	PrevURL, NextURL  string
}

// AdminRoutes registers the admin pages on mux. Requests must already have
// passed auth.Middleware; only admins get through.
func (u *UI) AdminRoutes(mux *http.ServeMux, db AdminStore) {
	a := &admin{ui: u, db: db}
	guard := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && !sameOrigin(r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			h(w, r)
		}))
	}
	mux.Handle("GET /admin/{$}", guard(a.books))
	mux.Handle("POST /admin/books/{asin}/visibility", guard(a.bookVisibility))
	mux.Handle("POST /admin/books/{asin}/series", guard(a.bookSeries))
	mux.Handle("GET /admin/series", guard(a.series))
	mux.Handle("POST /admin/series/{id}/default", guard(a.seriesDefault))
	mux.Handle("POST /admin/settings/default", guard(a.globalDefault))
	mux.Handle("GET /admin/jobs", guard(a.jobs))
}

type admin struct {
	ui *UI
	db AdminStore
}

func (a *admin) render(w http.ResponseWriter, page string, d adminData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.ui.pages[page].ExecuteTemplate(w, "layout", d); err != nil {
		a.ui.log.Error("render", "page", page, "err", err)
	}
}

func (a *admin) fragment(w http.ResponseWriter, name string, v any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.ui.pages["admin_books"].ExecuteTemplate(w, name, v); err != nil {
		a.ui.log.Error("render fragment", "name", name, "err", err)
	}
}

var filters = []struct {
	f     store.BookFilter
	label string
}{
	{store.FilterUnset, "未設定"},
	{store.FilterAll, "すべて"},
	{store.FilterVisible, "公開"},
	{store.FilterHidden, "非公開"},
}

func adminURL(f store.BookFilter, q string, page int) string {
	v := url.Values{}
	v.Set("f", string(f))
	if q != "" {
		v.Set("q", q)
	}
	if page > 0 {
		v.Set("page", strconv.Itoa(page))
	}
	return "/admin/?" + v.Encode()
}

func (a *admin) books(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MemberFrom(r.Context())
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	f := store.BookFilter(r.URL.Query().Get("f"))
	switch f {
	case store.FilterAll, store.FilterUnset, store.FilterVisible, store.FilterHidden:
	default:
		f = store.FilterAll
		if q == "" {
			f = store.FilterUnset
		}
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 0)

	o, err := a.db.Overview(r.Context())
	if err != nil {
		a.ui.fail(w, err)
		return
	}
	books, err := a.db.AdminBooks(r.Context(), f, q, pageSize, page*pageSize)
	if err != nil {
		a.ui.fail(w, err)
		return
	}
	d := adminData{Title: "本", Nav: "books", Query: q, Member: m, Overview: o, Filter: f}
	for _, x := range filters {
		d.Filters = append(d.Filters, filterLink{Label: x.label, URL: adminURL(x.f, q, 0), Current: x.f == f})
	}
	if len(books) > pageSize {
		books = books[:pageSize]
		d.NextURL = adminURL(f, q, page+1)
	}
	if page > 0 {
		d.PrevURL = adminURL(f, q, page-1)
	}
	d.Books = books
	a.render(w, "admin_books", d)
}

func (a *admin) bookVisibility(w http.ResponseWriter, r *http.Request) {
	v, err := store.ParseTri(r.FormValue("v"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	asin := r.PathValue("asin")
	if err := a.db.SetBookVisibility(r.Context(), asin, v); err != nil {
		a.ui.fail(w, err)
		return
	}
	a.bookRow(w, r, asin)
}

func (a *admin) bookSeries(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	index := 0
	if s := strings.TrimSpace(r.FormValue("index")); s != "" && name != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			http.Error(w, "巻は0以上の数で入れてください", http.StatusBadRequest)
			return
		}
		index = n
	}
	asin := r.PathValue("asin")
	if err := a.db.SetBookSeries(r.Context(), asin, name, index); err != nil {
		a.ui.fail(w, err)
		return
	}
	a.bookRow(w, r, asin)
}

func (a *admin) bookRow(w http.ResponseWriter, r *http.Request, asin string) {
	b, err := a.db.AdminBook(r.Context(), asin)
	if err != nil {
		a.ui.fail(w, err)
		return
	}
	a.fragment(w, "book-row", b)
}

func (a *admin) series(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MemberFrom(r.Context())
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := a.db.SeriesList(r.Context(), q)
	if err != nil {
		a.ui.fail(w, err)
		return
	}
	a.render(w, "admin_series", adminData{Title: "シリーズ", Nav: "series", Query: q, Member: m, Series: rows})
}

func (a *admin) seriesDefault(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, err := store.ParseTri(r.FormValue("v"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := a.db.SetSeriesDefault(r.Context(), id, v); err != nil {
		a.ui.fail(w, err)
		return
	}
	rows, err := a.db.SeriesList(r.Context(), "")
	if err != nil {
		a.ui.fail(w, err)
		return
	}
	for _, row := range rows {
		if row.ID == id {
			a.fragment(w, "series-row", row)
			return
		}
	}
	a.ui.fail(w, store.ErrNotFound)
}

func (a *admin) globalDefault(w http.ResponseWriter, r *http.Request) {
	var v bool
	switch r.FormValue("v") {
	case "1":
		v = true
	case "0":
	default:
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := a.db.SetDefaultVisible(r.Context(), v); err != nil {
		a.ui.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (a *admin) jobs(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MemberFrom(r.Context())
	jobs, err := a.db.Jobs(r.Context(), 100)
	if err != nil {
		a.ui.fail(w, err)
		return
	}
	a.render(w, "admin_jobs", adminData{Title: "ジョブ履歴", Nav: "jobs", Member: m, Jobs: jobs})
}
