package amazon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"
)

const (
	dashboardHost = "parents.amazon.co.jp"
	writePath     = "/ajax/update-add-content-status-batch"

	statusAdded    = "ADDED"
	statusNotAdded = "NOT_ADDED"
)

// ErrAmbiguous means the page shows more than one toggle for a title, so
// the right one cannot be told apart.
var ErrAmbiguous = errors.New("more than one toggle with this title")

// ErrNotInSearch means a search did not return the book.
var ErrNotInSearch = errors.New("book not found by search")

// expectedWrite is the only write the guard lets through while a toggle is
// being pressed.
type expectedWrite struct {
	child, status, asin string
}

// writeBody is the body of POST /ajax/update-add-content-status-batch.
type writeBody struct {
	ChildToAllowlistStatusMap map[string]string `json:"childToAllowlistStatusMap"`
	ASINs                     []string          `json:"asins"`
	ASINToContentTypeMap      map[string]string `json:"asinToContentTypeMap"`
}

// matches reports whether body is exactly the write e describes: one
// child, one book, the expected direction.
func (e *expectedWrite) matches(body string) bool {
	if e == nil {
		return false
	}
	var w writeBody
	if err := json.Unmarshal([]byte(body), &w); err != nil {
		return false
	}
	return len(w.ChildToAllowlistStatusMap) == 1 &&
		w.ChildToAllowlistStatusMap[e.child] == e.status &&
		len(w.ASINs) == 1 && w.ASINs[0] == e.asin &&
		len(w.ASINToContentTypeMap) <= 1 &&
		(len(w.ASINToContentTypeMap) == 0 || w.ASINToContentTypeMap[e.asin] == contentTypeEbook)
}

func (s *Session) setExpect(e *expectedWrite) {
	s.mu.Lock()
	s.expect = e
	s.mu.Unlock()
}

// writeExpected consumes the expectation, so one press allows one write.
func (s *Session) writeExpected(body string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ok := s.expect.matches(body)
	if ok {
		s.expect = nil
	}
	return ok
}

// OpenAddContent loads the add-content page.
func (s *Session) OpenAddContent(ctx context.Context) error {
	if _, err := s.page.Goto(addContentURL); err != nil {
		return fmt.Errorf("open add-content page: %w", err)
	}
	if !s.onDashboard() {
		return ErrSignedOut
	}
	return Sleep(ctx, 2*time.Second, 4*time.Second)
}

// responseWaiter collects responses of one dashboard endpoint.
type responseWaiter struct {
	ch      chan capturedResponse
	stop    func()
	timeout time.Duration
}

type capturedResponse struct {
	reqBody, body []byte
	status        int
	err           error
}

func (s *Session) waitFor(path string, timeout time.Duration) *responseWaiter {
	w := &responseWaiter{ch: make(chan capturedResponse, 16), timeout: timeout}
	handler := func(resp playwright.Response) {
		if pathOf(resp.URL()) != path {
			return
		}
		reqBody, _ := resp.Request().PostData()
		go func() {
			body, err := resp.Body()
			w.ch <- capturedResponse{reqBody: []byte(reqBody), body: body, status: resp.Status(), err: err}
		}()
	}
	s.ctx.OnResponse(handler)
	w.stop = func() { s.ctx.RemoveListener("response", handler) }
	return w
}

func (w *responseWaiter) next(ctx context.Context) (capturedResponse, error) {
	t := time.NewTimer(w.timeout)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return capturedResponse{}, ctx.Err()
	case r := <-w.ch:
		return r, r.err
	case <-t.C:
		return capturedResponse{}, errors.New("timed out waiting for the dashboard")
	}
}

// SelectChild narrows the list to one kids profile and to books, through
// the filter dialog, and checks that the page then asks for childID. With a
// profile name the row with that name is used; without one each child is
// tried in turn.
func (s *Session) SelectChild(ctx context.Context, childID, name string) error {
	if name != "" {
		dialog, err := s.openFilter(ctx)
		if err != nil {
			return err
		}
		idx, err := childIndex(dialog, name)
		if err != nil {
			return err
		}
		ids, err := s.filterTo(ctx, dialog, idx)
		if err != nil {
			return err
		}
		if len(ids) != 1 || ids[0] != childID {
			return fmt.Errorf("profile %q is not …%s on the dashboard", name, tail(childID))
		}
		return nil
	}
	for i := 0; ; i++ {
		dialog, err := s.openFilter(ctx)
		if err != nil {
			return err
		}
		n, err := dialog.GetByRole("checkbox", playwright.LocatorGetByRoleOptions{Name: "共有中"}).Count()
		if err != nil || n == 0 {
			return errors.New("no children in the filter dialog")
		}
		if i >= n {
			return fmt.Errorf("no child in the filter is …%s", tail(childID))
		}
		ids, err := s.filterTo(ctx, dialog, i)
		if err != nil {
			return err
		}
		if len(ids) == 1 && ids[0] == childID {
			return nil
		}
	}
}

// ListChildren selects each child in the filter in turn and returns the
// profile ID the page asks for under each name. It resets the filter after.
func (s *Session) ListChildren(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for i := 0; ; i++ {
		dialog, err := s.openFilter(ctx)
		if err != nil {
			return out, err
		}
		boxes := dialog.GetByRole("checkbox", playwright.LocatorGetByRoleOptions{Name: "共有中"})
		n, err := boxes.Count()
		if err != nil || n == 0 {
			return out, errors.New("no children in the filter dialog")
		}
		if i >= n {
			// Close the dialog we just opened.
			_ = dialog.GetByRole("button", playwright.LocatorGetByRoleOptions{Name: "Close"}).Click()
			break
		}
		name, err := childName(boxes.Nth(i))
		if err != nil {
			return out, err
		}
		ids, err := s.filterTo(ctx, dialog, i)
		if err != nil {
			return out, err
		}
		if len(ids) == 1 {
			out[name] = ids[0]
		}
	}
	if err := s.ResetFilter(ctx); err != nil {
		s.log.Warn("reset filter", "err", err)
	}
	return out, nil
}

// ResetFilter clears the filter so the page shows every child again.
func (s *Session) ResetFilter(ctx context.Context) error {
	dialog, err := s.openFilter(ctx)
	if err != nil {
		return err
	}
	if err := dialog.GetByRole("button", playwright.LocatorGetByRoleOptions{Name: "フィルターを解除"}).Click(); err != nil {
		return fmt.Errorf("clear filter: %w", err)
	}
	if err := Sleep(ctx, 500*time.Millisecond, 1500*time.Millisecond); err != nil {
		return err
	}
	if visible, _ := dialog.IsVisible(); visible {
		if err := dialog.GetByRole("button", playwright.LocatorGetByRoleOptions{Name: "結果を表示"}).Click(); err != nil {
			return fmt.Errorf("show results: %w", err)
		}
	}
	return Sleep(ctx, time.Second, 2*time.Second)
}

func (s *Session) openFilter(ctx context.Context) (playwright.Locator, error) {
	if err := s.page.Locator(`button[aria-label^="フィルターボタン"]`).First().Click(); err != nil {
		return nil, fmt.Errorf("open filter: %w", err)
	}
	dialog := s.page.Locator(`[role="dialog"]`)
	if err := dialog.WaitFor(); err != nil {
		return nil, fmt.Errorf("filter dialog: %w", err)
	}
	return dialog, Sleep(ctx, 500*time.Millisecond, 1500*time.Millisecond)
}

// childName reads the profile name shown next to a child's checkbox.
func childName(box playwright.Locator) (string, error) {
	name, err := box.Locator(`xpath=ancestor::div[p][1]/p`).First().TextContent()
	if err != nil {
		return "", fmt.Errorf("read child name: %w", err)
	}
	return strings.TrimSpace(name), nil
}

// childIndex finds the checkbox position of the child with this name.
func childIndex(dialog playwright.Locator, name string) (int, error) {
	boxes := dialog.GetByRole("checkbox", playwright.LocatorGetByRoleOptions{Name: "共有中"})
	n, err := boxes.Count()
	if err != nil {
		return 0, err
	}
	var seen []string
	for i := 0; i < n; i++ {
		got, err := childName(boxes.Nth(i))
		if err != nil {
			return 0, err
		}
		if got == name {
			return i, nil
		}
		seen = append(seen, got)
	}
	return 0, fmt.Errorf("no profile named %q on the dashboard (found %s)", name, strings.Join(seen, ", "))
}

// filterTo checks only the index-th child and 本, shows the results and
// returns the child IDs of the list request that follows.
func (s *Session) filterTo(ctx context.Context, dialog playwright.Locator, index int) ([]string, error) {
	children := dialog.GetByRole("checkbox", playwright.LocatorGetByRoleOptions{Name: "共有中"})
	n, err := children.Count()
	if err != nil {
		return nil, err
	}
	for i := 0; i < n; i++ {
		if err := setChecked(ctx, children.Nth(i), i == index); err != nil {
			return nil, err
		}
	}
	book := dialog.GetByRole("checkbox", playwright.LocatorGetByRoleOptions{Name: "本", Exact: playwright.Bool(true)})
	if err := setChecked(ctx, book, true); err != nil {
		return nil, fmt.Errorf("select 本: %w", err)
	}
	waiter := s.waitFor("/ajax/get-additional-content", 20*time.Second)
	defer waiter.stop()
	if err := dialog.GetByRole("button", playwright.LocatorGetByRoleOptions{Name: "結果を表示"}).Click(); err != nil {
		return nil, fmt.Errorf("show results: %w", err)
	}
	r, err := waiter.next(ctx)
	if err != nil {
		return nil, fmt.Errorf("filtered list: %w", err)
	}
	var req listRequest
	if err := json.Unmarshal(r.reqBody, &req); err != nil {
		return nil, fmt.Errorf("filtered list request: %w", err)
	}
	return req.ChildIDList, Sleep(ctx, time.Second, 3*time.Second)
}

func setChecked(ctx context.Context, l playwright.Locator, want bool) error {
	got, err := l.GetAttribute("aria-checked")
	if err != nil {
		return err
	}
	if (got == "true") == want {
		return nil
	}
	if err := l.Click(); err != nil {
		return err
	}
	return Sleep(ctx, 300*time.Millisecond, 900*time.Millisecond)
}

// Toggle shares (add) or unshares a book with the kids profile selected by
// SelectChild. It searches for the book, finds the toggle whose label is
// the exact title of the matching ASIN, and presses it once. Returns false
// when the toggle was already in the wanted state.
func (s *Session) Toggle(ctx context.Context, childID, asin, query string, add bool) (changed bool, err error) {
	field := s.page.Locator(searchFieldSelector)
	if err := s.openSearch(ctx, field); err != nil {
		return false, err
	}
	if err := field.Fill(""); err != nil {
		return false, fmt.Errorf("clear search: %w", err)
	}
	if err := Sleep(ctx, 300*time.Millisecond, 900*time.Millisecond); err != nil {
		return false, err
	}

	search := s.waitFor("/ajax/search-additional-content", 20*time.Second)
	defer search.stop()
	if err := field.PressSequentially(query, playwright.LocatorPressSequentiallyOptions{
		Delay: playwright.Float(float64(60 + randN(140))),
	}); err != nil {
		return false, fmt.Errorf("type search: %w", err)
	}
	if err := field.Press("Enter"); err != nil {
		return false, fmt.Errorf("submit search: %w", err)
	}
	title, err := s.titleFromSearch(ctx, search, childID, asin)
	if err != nil {
		return false, err
	}

	toggle := s.page.GetByRole("switch", playwright.PageGetByRoleOptions{Name: title + ", 本", Exact: playwright.Bool(true)})
	if err := toggle.First().WaitFor(playwright.LocatorWaitForOptions{Timeout: playwright.Float(15000)}); err != nil {
		return false, fmt.Errorf("toggle for %s: %w", asin, err)
	}
	if n, _ := toggle.Count(); n != 1 {
		return false, fmt.Errorf("%s: %w (%d)", asin, ErrAmbiguous, n)
	}
	if err := Sleep(ctx, 800*time.Millisecond, 2500*time.Millisecond); err != nil {
		return false, err
	}
	state, err := toggle.GetAttribute("aria-checked")
	if err != nil {
		return false, err
	}
	if (state == "true") == add {
		return false, nil
	}

	status := statusNotAdded
	if add {
		status = statusAdded
	}
	write := s.waitFor(writePath, 20*time.Second)
	defer write.stop()
	s.setExpect(&expectedWrite{child: childID, status: status, asin: asin})
	defer s.setExpect(nil)
	if err := toggle.Click(); err != nil {
		return false, fmt.Errorf("press toggle: %w", err)
	}
	r, err := write.next(ctx)
	if err != nil {
		return false, fmt.Errorf("write for %s: %w", asin, err)
	}
	if r.status != 200 {
		return false, fmt.Errorf("write for %s: HTTP %d", asin, r.status)
	}
	// Let the page settle, then confirm the toggle shows the new state.
	deadline := time.Now().Add(8 * time.Second)
	for {
		state, err := toggle.GetAttribute("aria-checked")
		if err == nil && (state == "true") == add {
			return true, nil
		}
		if time.Now().After(deadline) {
			return true, fmt.Errorf("toggle for %s did not change", asin)
		}
		if err := Sleep(ctx, 400*time.Millisecond, 800*time.Millisecond); err != nil {
			return true, err
		}
	}
}

// titleFromSearch waits for the search response and returns the exact
// title the page uses for asin. The search must be for childID only.
func (s *Session) titleFromSearch(ctx context.Context, w *responseWaiter, childID, asin string) (string, error) {
	for {
		r, err := w.next(ctx)
		if err != nil {
			return "", fmt.Errorf("search for %s: %w", asin, err)
		}
		var req listRequest
		if err := json.Unmarshal(r.reqBody, &req); err != nil {
			return "", fmt.Errorf("search request: %w", err)
		}
		if len(req.ChildIDList) != 1 || req.ChildIDList[0] != childID {
			return "", fmt.Errorf("search was not limited to …%s", tail(childID))
		}
		var resp listResponse
		if err := json.Unmarshal(r.body, &resp); err != nil {
			return "", fmt.Errorf("search response: %w", err)
		}
		for _, it := range resp.PurchasedItemsMetadataList {
			if it.ASIN == asin && it.Category == contentTypeEbook {
				return it.Title, nil
			}
		}
		return "", fmt.Errorf("%s: %w (%d results)", asin, ErrNotInSearch, len(resp.PurchasedItemsMetadataList))
	}
}
