// Package amazon drives the Amazon Kids parent dashboard through a real,
// headful Chromium. It only reads what the page itself loads; it never calls
// the dashboard's internal API on its own.
package amazon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/nasa9084/kkids-sync/internal/guard"
)

const (
	dashboardURL  = "https://parents.amazon.co.jp/"
	addContentURL = "https://parents.amazon.co.jp/settings/add-content?isChildSelected=true&selectedContentType=EBOOK"
)

// ErrSignedOut means the saved session no longer works and someone has to
// sign in by hand. Callers must stop, not retry.
var ErrSignedOut = errors.New("amazon session is signed out")

var (
	listEndpoint   = regexp.MustCompile(`^/ajax/get-additional-content$`)
	searchEndpoint = regexp.MustCompile(`^/ajax/search-additional-content$`)
)

// searchFieldSelector is the dashboard's search box on the add-content page.
// It is hidden until the search button next to the filter button is pressed;
// once open, that button is labelled "検索を終了".
const (
	searchFieldSelector   = "#content-management-search-field"
	searchOpenSelector    = `button[aria-label^="検索"]:not([aria-label="検索を終了"])`
	searchOpenWaitTimeout = 10 * time.Second
)

// Options configures a Session.
type Options struct {
	// StatePath is the Playwright storage state (cookies). Read on open,
	// rewritten on close.
	StatePath string
	// ReadOnly blocks state-changing requests to the dashboard.
	ReadOnly bool
	// Headless runs without a window. Off by default: the page should see
	// an ordinary browser.
	Headless bool
	Logger   *slog.Logger
}

// Session is one browser with the owner's Amazon session loaded.
type Session struct {
	opts    Options
	log     *slog.Logger
	pw      *playwright.Playwright
	browser playwright.Browser
	ctx     playwright.BrowserContext
	page    playwright.Page
}

// Open starts Chromium. A missing StatePath is allowed only for Login.
func Open(opts Options) (*Session, error) {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	pw, err := playwright.Run()
	if err != nil {
		return nil, fmt.Errorf("start playwright: %w", err)
	}
	s := &Session{opts: opts, log: log, pw: pw}
	s.browser, err = pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(opts.Headless),
	})
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("launch chromium: %w", err)
	}
	ctxOpts := playwright.BrowserNewContextOptions{
		Locale:     playwright.String("ja-JP"),
		TimezoneId: playwright.String("Asia/Tokyo"),
	}
	if _, err := os.Stat(opts.StatePath); err == nil {
		ctxOpts.StorageStatePath = playwright.String(opts.StatePath)
	}
	s.ctx, err = s.browser.NewContext(ctxOpts)
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("new context: %w", err)
	}
	policy := guard.Policy{ReadOnly: opts.ReadOnly}
	if err := s.ctx.Route("**/*", func(route playwright.Route) {
		req := route.Request()
		if d, why := policy.Check(req.URL(), req.Method(), req.IsNavigationRequest()); d == guard.Block {
			log.Warn("blocked request", "method", req.Method(), "url", stripQuery(req.URL()), "reason", why)
			_ = route.Abort("blockedbyclient")
			return
		}
		_ = route.Continue()
	}); err != nil {
		s.Close()
		return nil, fmt.Errorf("route: %w", err)
	}
	s.page, err = s.ctx.NewPage()
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("new page: %w", err)
	}
	return s, nil
}

// Close saves the (possibly refreshed) cookies and shuts the browser down.
func (s *Session) Close() error {
	var errs []error
	if s.ctx != nil && s.opts.StatePath != "" {
		if _, err := s.ctx.StorageState(playwright.BrowserContextStorageStateOptions{Path: playwright.String(s.opts.StatePath)}); err != nil {
			errs = append(errs, fmt.Errorf("save session: %w", err))
		} else if err := os.Chmod(s.opts.StatePath, 0o600); err != nil {
			errs = append(errs, err)
		}
	}
	if s.browser != nil {
		errs = append(errs, s.browser.Close())
	}
	if s.pw != nil {
		errs = append(errs, s.pw.Stop())
	}
	return errors.Join(errs...)
}

// Login opens the dashboard and waits until wait returns, giving a person
// time to sign in. The session is saved by Close.
func (s *Session) Login(wait func()) error {
	if _, err := s.page.Goto(dashboardURL); err != nil {
		s.log.Warn("open dashboard", "err", err)
	}
	wait()
	if !s.onDashboard() {
		return errors.New("not on the parent dashboard yet: sign in and try again")
	}
	return nil
}

// ReadLibraryOptions tunes ReadLibrary.
type ReadLibraryOptions struct {
	// MaxPages stops paging after this many list pages (0 = 60).
	MaxPages int
	// PageTimeout is how long to wait for the next page after scrolling
	// (0 = 20s).
	PageTimeout time.Duration
	// Known holds ASINs from an earlier sync. When set, paging stops as soon
	// as the tail of a page is all known, so a normal run reads one page
	// instead of scrolling through the whole library. Nil reads everything.
	Known map[string]bool
}

// knownTail is how many books at the end of a page must be known to stop.
const knownTail = 10

// ReadLibrary opens the add-content page and scrolls until every book has
// been loaded, reading the list responses the page receives.
func (s *Session) ReadLibrary(ctx context.Context, o ReadLibraryOptions) (*Snapshot, error) {
	if o.MaxPages == 0 {
		o.MaxPages = 60
	}
	if o.PageTimeout == 0 {
		o.PageTimeout = 20 * time.Second
	}
	col := newCollector()
	pending := make(chan error, 64)
	searched := make(chan error, 8)
	handler := func(resp playwright.Response) {
		path := pathOf(resp.URL())
		switch {
		case listEndpoint.MatchString(path):
			reqBody, _ := resp.Request().PostData()
			// Body blocks, so read it off the event goroutine.
			go func() {
				body, err := resp.Body()
				if err == nil {
					err = col.add([]byte(reqBody), body)
				}
				pending <- err
			}()
		case searchEndpoint.MatchString(path):
			go func() {
				body, err := resp.Body()
				if err == nil {
					err = col.addSearch(body)
				}
				searched <- err
			}()
		}
	}
	s.ctx.OnResponse(handler)
	defer s.ctx.RemoveListener("response", handler)

	if _, err := s.page.Goto(addContentURL); err != nil {
		return nil, fmt.Errorf("open add-content page: %w", err)
	}
	if !s.onDashboard() {
		return nil, ErrSignedOut
	}

	timer := time.NewTimer(o.PageTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return col.snapshot(), ctx.Err()
		case err := <-pending:
			if err != nil {
				return col.snapshot(), err
			}
			pages, cursor := col.progress()
			s.log.Info("list page", "pages", pages, "cursor", cursor)
			incremental := o.Known != nil && cursor != cursorLast && col.tailKnown(o.Known, knownTail)
			if cursor == cursorLast || incremental {
				if err := s.searchOnce(ctx, col, searched, o.PageTimeout); err != nil {
					return col.snapshot(), err
				}
				snap := col.snapshot()
				if incremental {
					snap.Incremental = true
					snap.Complete = col.fullSharedState()
				}
				return snap, nil
			}
			if pages >= o.MaxPages {
				return col.snapshot(), fmt.Errorf("stopped after %d pages", pages)
			}
			if err := s.scrollToEnd(ctx); err != nil {
				return col.snapshot(), err
			}
			timer.Reset(o.PageTimeout)
		case <-timer.C:
			if !s.onDashboard() {
				return col.snapshot(), ErrSignedOut
			}
			// Infinite scroll sometimes needs a second nudge.
			pages, _ := col.progress()
			if pages == 0 {
				return col.snapshot(), errors.New("the book list never loaded")
			}
			if err := s.scrollToEnd(ctx); err != nil {
				return col.snapshot(), err
			}
			timer.Reset(o.PageTimeout)
			select {
			case <-ctx.Done():
				return col.snapshot(), ctx.Err()
			case err := <-pending:
				pending <- err // handle it in the main loop
			case <-timer.C:
				return col.snapshot(), errors.New("no more pages loaded, but the list has not reached its end")
			}
		}
	}
}

// searchOnce types a short query into the search box. The search response
// carries the complete share state of every child in view, which list pages
// do not. The query itself does not matter; a fragment of a real title keeps
// it looking ordinary.
func (s *Session) searchOnce(ctx context.Context, col *collector, searched <-chan error, timeout time.Duration) error {
	snap := col.snapshot()
	if len(snap.Books) == 0 {
		return errors.New("no books to search for")
	}
	title := []rune(snap.Books[rand.IntN(len(snap.Books))].Title)
	query := string(title[:min(len(title), 2+rand.IntN(3))])

	field := s.page.Locator(searchFieldSelector)
	if err := s.openSearch(ctx, field); err != nil {
		return err
	}
	if err := field.Click(); err != nil {
		return fmt.Errorf("focus search field: %w", err)
	}
	if err := Sleep(ctx, 400*time.Millisecond, 1200*time.Millisecond); err != nil {
		return err
	}
	if err := field.PressSequentially(query, playwright.LocatorPressSequentiallyOptions{
		Delay: playwright.Float(float64(80 + rand.IntN(120))),
	}); err != nil {
		return fmt.Errorf("type search query: %w", err)
	}
	if err := field.Press("Enter"); err != nil {
		return fmt.Errorf("submit search: %w", err)
	}

	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-searched:
		if err != nil {
			return err
		}
	case <-t.C:
		return errors.New("search response did not arrive")
	}
	// The search must cover every child the list was loaded for.
	have := map[string]bool{}
	for _, c := range col.sharedChildren() {
		have[c] = true
	}
	for _, c := range col.listChildren() {
		if !have[c] {
			return fmt.Errorf("search did not return the share state of child …%s", tail(c))
		}
	}
	s.log.Info("share state read from search", "children", len(have))
	return nil
}

// openSearch shows the search box if it is not open yet.
func (s *Session) openSearch(ctx context.Context, field playwright.Locator) error {
	if visible, _ := field.IsVisible(); visible {
		return nil
	}
	button := s.page.Locator(searchOpenSelector).First()
	if err := button.Click(playwright.LocatorClickOptions{Timeout: playwright.Float(float64(searchOpenWaitTimeout.Milliseconds()))}); err != nil {
		return fmt.Errorf("open search: %w", err)
	}
	if err := field.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(float64(searchOpenWaitTimeout.Milliseconds())),
	}); err != nil {
		return fmt.Errorf("search field did not appear: %w", err)
	}
	return Sleep(ctx, 300*time.Millisecond, 900*time.Millisecond)
}

func tail(id string) string {
	if len(id) <= 4 {
		return id
	}
	return id[len(id)-4:]
}

// scrollToEnd scrolls down in a few uneven steps, like a person would.
func (s *Session) scrollToEnd(ctx context.Context) error {
	steps := 2 + rand.IntN(3)
	for i := 0; i < steps; i++ {
		if err := s.page.Mouse().Wheel(0, float64(600+rand.IntN(900))); err != nil {
			return fmt.Errorf("scroll: %w", err)
		}
		if err := Sleep(ctx, 300*time.Millisecond, 1200*time.Millisecond); err != nil {
			return err
		}
	}
	_, err := s.page.Evaluate(`() => window.scrollTo(0, document.body.scrollHeight)`)
	return err
}

func (s *Session) onDashboard() bool {
	u, err := url.Parse(s.page.URL())
	return err == nil && u.Hostname() == "parents.amazon.co.jp"
}

// Sleep waits a random duration between min and max, or until ctx ends.
func Sleep(ctx context.Context, min, max time.Duration) error {
	d := min
	if max > min {
		d += rand.N(max - min)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func pathOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Path
}

func stripQuery(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}
