package amazon

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Book is one Kindle title in the owner's library.
type Book struct {
	ASIN      string `json:"asin"`
	Title     string `json:"title"`
	Thumbnail string `json:"thumbnail,omitempty"`
}

// Snapshot is what one pass over the add-content page saw.
type Snapshot struct {
	Books []Book `json:"books"`
	// Shared maps a child profile ID to the ASINs shared with it.
	Shared map[string][]string `json:"shared"`
	// Complete is true when paging reached the end of the list and the share
	// state came from a search response, which carries every shared ASIN.
	// List pages only carry the share state of their first page.
	Complete bool `json:"complete"`
}

// listRequest is the body of POST /ajax/get-additional-content.
type listRequest struct {
	ChildIDList            []string          `json:"childIdList"`
	ContentTypeToCursorMap map[string]string `json:"contentTypeToCursorMap"`
}

// listResponse is the part of the response we use.
type listResponse struct {
	ChildIDToAllowlistMap map[string]struct {
		AllowlistData map[string][]struct {
			AllowlistStateType string `json:"allowlistStateType"`
			Identifier         string `json:"identifier"`
		} `json:"allowlistData"`
	} `json:"childIdToAllowlistMap"`
	ContentTypeToCursorMap     map[string]string `json:"contentTypeToCursorMap"`
	PurchasedItemsMetadataList []struct {
		Category  string `json:"category"`
		Title     string `json:"title"`
		ASIN      string `json:"asin"`
		Thumbnail string `json:"thumbnail"`
	} `json:"purchasedItemsMetadataList"`
}

const (
	contentTypeEbook = "EBOOK"
	cursorLast       = "IS_LAST"
	stateGranted     = "GRANTED"
)

// searchResponse is the part of POST /ajax/search-additional-content we use.
// Unlike list pages, it carries the full allowlist of every child in the
// request, whatever the query.
type searchResponse struct {
	ChildIDToAllowlistMap map[string]struct {
		AllowlistData map[string][]struct {
			AllowlistStateType string `json:"allowlistStateType"`
			Identifier         string `json:"identifier"`
		} `json:"allowlistData"`
	} `json:"childIdToAllowlistMap"`
}

// collector accumulates list pages as the browser loads them.
type collector struct {
	mu         sync.Mutex
	books      map[string]Book
	order      []string
	shared     map[string]map[string]bool
	children   []string // from the first list request
	fullShared bool     // shared came from a search response
	cursor     string   // EBOOK cursor from the latest page
	pages      int
}

func newCollector() *collector {
	return &collector{
		books:  map[string]Book{},
		shared: map[string]map[string]bool{},
	}
}

// add records one request/response pair of /ajax/get-additional-content.
func (c *collector) add(reqBody, respBody []byte) error {
	var req listRequest
	if err := json.Unmarshal(reqBody, &req); err != nil {
		return fmt.Errorf("decode list request: %w", err)
	}
	var resp listResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return fmt.Errorf("decode list response: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.pages++
	if c.children == nil {
		c.children = append([]string(nil), req.ChildIDList...)
	}
	for _, it := range resp.PurchasedItemsMetadataList {
		if it.Category != contentTypeEbook || it.ASIN == "" {
			continue
		}
		if _, ok := c.books[it.ASIN]; !ok {
			c.order = append(c.order, it.ASIN)
		}
		c.books[it.ASIN] = Book{ASIN: it.ASIN, Title: it.Title, Thumbnail: it.Thumbnail}
	}
	// Once a search has given the complete state, partial page data would
	// only be older.
	if !c.fullShared {
		for child, v := range resp.ChildIDToAllowlistMap {
			set := c.shared[child]
			if set == nil {
				set = map[string]bool{}
				c.shared[child] = set
			}
			for _, e := range v.AllowlistData[contentTypeEbook] {
				if e.AllowlistStateType == stateGranted {
					set[e.Identifier] = true
				}
			}
		}
	}
	if cur, ok := resp.ContentTypeToCursorMap[contentTypeEbook]; ok {
		c.cursor = cur
	}
	return nil
}

// addSearch records a search response; its allowlists replace whatever the
// list pages reported.
func (c *collector) addSearch(respBody []byte) error {
	var resp searchResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return fmt.Errorf("decode search response: %w", err)
	}
	if len(resp.ChildIDToAllowlistMap) == 0 {
		return fmt.Errorf("search response has no share state")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	shared := map[string]map[string]bool{}
	for child, v := range resp.ChildIDToAllowlistMap {
		set := map[string]bool{}
		for _, e := range v.AllowlistData[contentTypeEbook] {
			if e.AllowlistStateType == stateGranted {
				set[e.Identifier] = true
			}
		}
		shared[child] = set
	}
	c.shared = shared
	c.fullShared = true
	return nil
}

// sharedChildren reports the children with known share state.
func (c *collector) sharedChildren() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.shared))
	for k := range c.shared {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *collector) listChildren() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]string(nil), c.children...)
	sort.Strings(out)
	return out
}

func (c *collector) progress() (pages int, cursor string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pages, c.cursor
}

func (c *collector) snapshot() *Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := &Snapshot{
		Shared:   map[string][]string{},
		Complete: c.cursor == cursorLast && c.fullShared,
	}
	for _, asin := range c.order {
		s.Books = append(s.Books, c.books[asin])
	}
	for child, set := range c.shared {
		asins := make([]string, 0, len(set))
		for a := range set {
			asins = append(asins, a)
		}
		sort.Strings(asins)
		s.Shared[child] = asins
	}
	return s
}
