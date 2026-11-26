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
	ShelfSummaries(ctx context.Context) ([]store.ShelfSummary, error)
	MemberShelf(ctx context.Context, memberID int64, state store.ShelfState, query string, limit, offset int) ([]store.ShelfRow, error)
	SeriesRules(ctx context.Context) ([]store.SeriesRule, error)
	PreviewRule(ctx context.Context, r store.SeriesRule) ([]store.RuleMatch, error)
	AddRule(ctx context.Context, r store.SeriesRule) (int, error)
	DeleteRule(ctx context.Context, id int64) error
	RenameSeries(ctx context.Context, id int64, name string) error
	Reclassify(ctx context.Context) (int, error)
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
	// shelves page
	Shelves []store.ShelfSummary
	Shelf   []store.ShelfRow
	ShelfOf store.ShelfSummary
	States  []filterLink
	// series page
	Rules            []store.SeriesRule
	RulePattern      string
	RuleSeries       string
	RuleError        string
	Preview          []store.RuleMatch
	Previewed, Moved bool
	Reclassified     bool
	MovedCount       int
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
	mux.Handle("GET /admin/shelves", guard(a.shelves))
	mux.Handle("POST /admin/view-as/{id}", guard(a.viewAsStart))
	mux.Handle("GET /admin/view-as/end", guard(a.viewAsEnd))
	mux.Handle("POST /admin/series/{id}/name", guard(a.seriesRename))
	mux.Handle("POST /admin/series/rules", guard(a.ruleAdd))
	mux.Handle("POST /admin/series/rules/{id}/delete", guard(a.ruleDelete))
	mux.Handle("POST /admin/series/reclassify", guard(a.reclassify))
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
	rules, err := a.db.SeriesRules(r.Context())
	if err != nil {
		a.ui.fail(w, err)
		return
	}
	d := adminData{Title: "シリーズ", Nav: "series", Query: q, Member: m, Series: rows, Rules: rules}
	if n := r.URL.Query().Get("reclassified"); n != "" {
		d.Reclassified = true
		d.MovedCount, _ = strconv.Atoi(n)
	}
	if n := r.URL.Query().Get("moved"); n != "" {
		d.Moved = true
		d.MovedCount, _ = strconv.Atoi(n)
	}
	a.render(w, "admin_series", d)
}

// ruleAdd previews a rule (preview=1) or saves and applies it.
func (a *admin) ruleAdd(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MemberFrom(r.Context())
	d := adminData{Title: "シリーズ", Nav: "series", Member: m,
		RulePattern: r.FormValue("pattern"), RuleSeries: r.FormValue("series")}
	fill := func() bool {
		var err error
		if d.Series, err = a.db.SeriesList(r.Context(), ""); err != nil {
			a.ui.fail(w, err)
			return false
		}
		if d.Rules, err = a.db.SeriesRules(r.Context()); err != nil {
			a.ui.fail(w, err)
			return false
		}
		return true
	}
	rule, err := store.CompileRule(d.RulePattern, d.RuleSeries)
	if err != nil {
		d.RuleError = err.Error()
		if fill() {
			a.render(w, "admin_series", d)
		}
		return
	}
	if r.FormValue("preview") == "1" {
		if d.Preview, err = a.db.PreviewRule(r.Context(), rule); err != nil {
			a.ui.fail(w, err)
			return
		}
		d.Previewed = true
		if fill() {
			a.render(w, "admin_series", d)
		}
		return
	}
	n, err := a.db.AddRule(r.Context(), rule)
	if err != nil {
		a.ui.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/series?moved="+strconv.Itoa(n)+"#rules", http.StatusSeeOther)
}

func (a *admin) reclassify(w http.ResponseWriter, r *http.Request) {
	n, err := a.db.Reclassify(r.Context())
	if err != nil {
		a.ui.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/series?reclassified="+strconv.Itoa(n)+"#rules", http.StatusSeeOther)
}

func (a *admin) ruleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := a.db.DeleteRule(r.Context(), id); err != nil {
		a.ui.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/series#rules", http.StatusSeeOther)
}

func (a *admin) seriesRename(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "シリーズ名を入れてください", http.StatusBadRequest)
		return
	}
	if err := a.db.RenameSeries(r.Context(), id, name); err != nil {
		a.ui.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/series?q="+url.QueryEscape(name), http.StatusSeeOther)
}

var shelfStates = []struct {
	s     store.ShelfState
	label string
}{
	{store.ShelfAny, "すべて"},
	{store.ShelfToAdd, "追加待ち"},
	{store.ShelfToRemove, "削除待ち"},
	{store.ShelfHeld, "非公開で保留"},
	{store.ShelfShared, "共有中"},
}

// viewAsStart lets the admin look at a family member's pages, read-only or
// (act=1) changing their shelf for them.
func (a *admin) viewAsStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		http.NotFound(w, r)
		return
	}
	value := id
	if r.FormValue("act") == "1" {
		value += ":act"
	}
	http.SetCookie(w, &http.Cookie{Name: viewAsCookie, Value: value, Path: "/", HttpOnly: true,
		Secure: r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *admin) viewAsEnd(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: viewAsCookie, Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/admin/shelves", http.StatusSeeOther)
}

func shelfURL(member int64, st store.ShelfState, q string, page int) string {
	v := url.Values{}
	v.Set("m", strconv.FormatInt(member, 10))
	if st != store.ShelfAny {
		v.Set("s", string(st))
	}
	if q != "" {
		v.Set("q", q)
	}
	if page > 0 {
		v.Set("page", strconv.Itoa(page))
	}
	return "/admin/shelves?" + v.Encode()
}

func (a *admin) shelves(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MemberFrom(r.Context())
	sums, err := a.db.ShelfSummaries(r.Context())
	if err != nil {
		a.ui.fail(w, err)
		return
	}
	d := adminData{Title: "本棚", Nav: "shelves", Member: m, Shelves: sums}
	if len(sums) == 0 {
		a.render(w, "admin_shelves", d)
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("m"), 10, 64)
	d.ShelfOf = sums[0]
	for _, s := range sums {
		if s.Member.ID == id {
			d.ShelfOf = s
		}
	}
	st := store.ShelfState(r.URL.Query().Get("s"))
	valid := false
	for _, x := range shelfStates {
		valid = valid || x.s == st
	}
	if !valid {
		st = store.ShelfAny
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	d.Query = q
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 0)
	for _, x := range shelfStates {
		d.States = append(d.States, filterLink{Label: x.label, URL: shelfURL(d.ShelfOf.Member.ID, x.s, q, 0), Current: x.s == st})
	}
	rows, err := a.db.MemberShelf(r.Context(), d.ShelfOf.Member.ID, st, q, pageSize, page*pageSize)
	if err != nil {
		a.ui.fail(w, err)
		return
	}
	if len(rows) > pageSize {
		rows = rows[:pageSize]
		d.NextURL = shelfURL(d.ShelfOf.Member.ID, st, q, page+1)
	}
	if page > 0 {
		d.PrevURL = shelfURL(d.ShelfOf.Member.ID, st, q, page-1)
	}
	d.Shelf = rows
	a.render(w, "admin_shelves", d)
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
