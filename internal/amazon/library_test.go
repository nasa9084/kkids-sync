package amazon

import (
	"github.com/mxschmitt/playwright-go"
	"reflect"
	"testing"
)

// Shapes follow what the parent dashboard returned in the PoC; IDs are made up.
const (
	firstReq  = `{"searchQuery":"","childIdList":["amzn1.account.CHILDA","amzn1.account.CHILDB"],"contentTypeToCursorMap":{"EBOOK":"","VIDEO":""}}`
	firstResp = `{
	  "childIdToAllowlistMap": {
	    "amzn1.account.CHILDA": {"allowlistData": {"EBOOK": [
	      {"allowlistStateType": "GRANTED", "identifier": "B000000001"}
	    ], "APP": []}},
	    "amzn1.account.CHILDB": {"allowlistData": {"EBOOK": [
	      {"allowlistStateType": "GRANTED", "identifier": "B000000001"},
	      {"allowlistStateType": "GRANTED", "identifier": "B000000002"}
	    ]}}
	  },
	  "contentTypeToCursorMap": {"EBOOK": "1788", "VIDEO": "IS_LAST"},
	  "purchasedItemsMetadataList": [
	    {"category": "EBOOK", "title": "Book One", "asin": "B000000001", "thumbnail": "https://example.com/1"},
	    {"category": "EBOOK", "title": "Book Two", "asin": "B000000002", "thumbnail": "https://example.com/2"},
	    {"category": "VIDEO", "title": "A Video", "asin": "B0VIDEO001"}
	  ]
	}`
	nextReq  = `{"searchQuery":"","childIdList":["amzn1.account.CHILDA"],"contentTypeToCursorMap":{"EBOOK":"1788"}}`
	nextResp = `{
	  "childIdToAllowlistMap": {
	    "amzn1.account.CHILDA": {"allowlistData": {"EBOOK": [
	      {"allowlistStateType": "GRANTED", "identifier": "B000000003"},
	      {"allowlistStateType": "DENIED", "identifier": "B000000002"}
	    ]}}
	  },
	  "contentTypeToCursorMap": {"EBOOK": "IS_LAST"},
	  "purchasedItemsMetadataList": [
	    {"category": "EBOOK", "title": "Book Two (renamed)", "asin": "B000000002"},
	    {"category": "EBOOK", "title": "Book Three", "asin": "B000000003"}
	  ]
	}`
)

// searchResp carries the full allowlist, unlike list pages.
const searchResp = `{
  "childIdToAllowlistMap": {
    "amzn1.account.CHILDA": {"allowlistData": {"EBOOK": [
      {"allowlistStateType": "GRANTED", "identifier": "B000000001"},
      {"allowlistStateType": "GRANTED", "identifier": "B000000003"},
      {"allowlistStateType": "GRANTED", "identifier": "B000000009"}
    ]}},
    "amzn1.account.CHILDB": {"allowlistData": {"EBOOK": [
      {"allowlistStateType": "GRANTED", "identifier": "B000000002"}
    ]}}
  },
  "purchasedItemsMetadataList": []
}`

func TestCollector(t *testing.T) {
	c := newCollector()
	if err := c.add([]byte(firstReq), []byte(firstResp)); err != nil {
		t.Fatal(err)
	}
	if pages, cur := c.progress(); pages != 1 || cur != "1788" {
		t.Fatalf("after first page: pages=%d cursor=%q", pages, cur)
	}
	if s := c.snapshot(); s.Complete {
		t.Fatal("complete after the first page")
	}
	if err := c.add([]byte(nextReq), []byte(nextResp)); err != nil {
		t.Fatal(err)
	}

	got := c.snapshot()
	wantBooks := []Book{
		{ASIN: "B000000001", Title: "Book One", Thumbnail: "https://example.com/1"},
		{ASIN: "B000000002", Title: "Book Two (renamed)"},
		{ASIN: "B000000003", Title: "Book Three"},
	}
	if !reflect.DeepEqual(got.Books, wantBooks) {
		t.Errorf("Books = %+v, want %+v", got.Books, wantBooks)
	}
	wantShared := map[string][]string{
		"amzn1.account.CHILDA": {"B000000001", "B000000003"},
		"amzn1.account.CHILDB": {"B000000001", "B000000002"},
	}
	if !reflect.DeepEqual(got.Shared, wantShared) {
		t.Errorf("Shared = %v, want %v", got.Shared, wantShared)
	}
	if got.Complete {
		t.Error("Complete = true without a search response")
	}

	if err := c.addSearch([]byte(searchResp)); err != nil {
		t.Fatal(err)
	}
	got = c.snapshot()
	wantShared = map[string][]string{
		"amzn1.account.CHILDA": {"B000000001", "B000000003", "B000000009"},
		"amzn1.account.CHILDB": {"B000000002"},
	}
	if !reflect.DeepEqual(got.Shared, wantShared) {
		t.Errorf("after search: Shared = %v, want %v", got.Shared, wantShared)
	}
	if !got.Complete {
		t.Error("Complete = false after paging to the end and a search")
	}
	if got, want := c.listChildren(), []string{"amzn1.account.CHILDA", "amzn1.account.CHILDB"}; !reflect.DeepEqual(got, want) {
		t.Errorf("listChildren = %v, want %v", got, want)
	}

	// A late list page must not overwrite the complete state.
	if err := c.add([]byte(firstReq), []byte(firstResp)); err != nil {
		t.Fatal(err)
	}
	if got := c.snapshot().Shared; !reflect.DeepEqual(got, wantShared) {
		t.Errorf("after a late page: Shared = %v, want %v", got, wantShared)
	}
}

func TestAddSearchWithoutState(t *testing.T) {
	if err := newCollector().addSearch([]byte(`{"childIdToAllowlistMap":{}}`)); err == nil {
		t.Error("want error for a search response without share state")
	}
}

func TestCollectorRejectsBrokenJSON(t *testing.T) {
	c := newCollector()
	if err := c.add([]byte(`{`), []byte(firstResp)); err == nil {
		t.Error("want error for broken request")
	}
	if err := c.add([]byte(firstReq), []byte(`<html>`)); err == nil {
		t.Error("want error for broken response")
	}
}

func TestTailKnown(t *testing.T) {
	c := newCollector()
	if c.tailKnown(map[string]bool{"B000000001": true}, 10) {
		t.Error("tailKnown before any page")
	}
	if err := c.add([]byte(firstReq), []byte(firstResp)); err != nil {
		t.Fatal(err)
	}
	// The page holds B000000001, B000000002 (the video is ignored).
	if c.tailKnown(map[string]bool{"B000000001": true}, 10) {
		t.Error("tailKnown with an unknown book on the page")
	}
	if !c.tailKnown(map[string]bool{"B000000002": true}, 1) {
		t.Error("tailKnown(n=1) should only look at the last book")
	}
	if !c.tailKnown(map[string]bool{"B000000001": true, "B000000002": true}, 10) {
		t.Error("tailKnown with every book known")
	}
}

func TestHasSignInCookie(t *testing.T) {
	// What a browser that never signed in holds after opening the dashboard.
	anon := []playwright.Cookie{{Name: "session-id", Value: "x"}, {Name: "ubid-acbjp", Value: "x"}, {Name: "csm-hit", Value: "x"}}
	if hasSignInCookie(anon) {
		t.Error("anonymous cookies counted as signed in")
	}
	if !hasSignInCookie(append(anon, playwright.Cookie{Name: "at-acbjp", Value: "x"})) {
		t.Error("at-acbjp not counted as signed in")
	}
}
