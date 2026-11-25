package amazon

import (
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

func TestCollector(t *testing.T) {
	c := newCollector()
	if err := c.add([]byte(firstReq), []byte(firstResp)); err != nil {
		t.Fatal(err)
	}
	if pages, cur := c.progress(); pages != 1 || cur != "1788" {
		t.Fatalf("after first page: pages=%d cursor=%q", pages, cur)
	}
	if s := c.snapshot(); s.Complete || s.Selected != "" {
		t.Fatalf("after first page: complete=%v selected=%q", s.Complete, s.Selected)
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
	if got.Selected != "amzn1.account.CHILDA" {
		t.Errorf("Selected = %q", got.Selected)
	}
	if !got.Complete {
		t.Error("Complete = false, want true")
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
