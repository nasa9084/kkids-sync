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
	// Selected is the child profile the page was showing.
	Selected string `json:"selected"`
	// Complete is true when paging reached the end of the list.
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

// collector accumulates list pages as the browser loads them.
type collector struct {
	mu       sync.Mutex
	books    map[string]Book
	order    []string
	shared   map[string]map[string]bool
	selected string
	cursor   string // EBOOK cursor from the latest page
	pages    int
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
	// Paging requests carry only the selected child; the first load of the
	// page carries every child, so a single ID is the reliable signal.
	if len(req.ChildIDList) == 1 {
		c.selected = req.ChildIDList[0]
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
	if cur, ok := resp.ContentTypeToCursorMap[contentTypeEbook]; ok {
		c.cursor = cur
	}
	return nil
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
		Selected: c.selected,
		Complete: c.cursor == cursorLast,
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
