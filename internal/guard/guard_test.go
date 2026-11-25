package guard

import "testing"

func TestCheck(t *testing.T) {
	ro := Policy{ReadOnly: true}
	rw := Policy{}
	tests := []struct {
		name   string
		p      Policy
		url    string
		method string
		nav    bool
		want   Decision
	}{
		{"dashboard page", ro, "https://parents.amazon.co.jp/settings/content", "GET", true, Allow},
		{"sign-in page", ro, "https://www.amazon.co.jp/ap/signin?x=1", "GET", true, Allow},
		{"sign-in post", ro, "https://www.amazon.co.jp/ap/signin", "POST", false, Allow},
		{"image cdn", ro, "https://m.media-amazon.com/images/I/a.jpg", "GET", false, Allow},
		{"buy", rw, "https://www.amazon.co.jp/gp/buy/spc/handlers/display.html", "GET", true, Block},
		{"cart", rw, "https://www.amazon.co.jp/cart/add-to-cart", "POST", false, Block},
		{"checkout", rw, "https://www.amazon.co.jp/checkout/p/x", "GET", true, Block},
		{"mycd delete", rw, "https://www.amazon.co.jp/hz/mycd/ajax/delete", "POST", false, Block},
		{"outside navigation", rw, "https://example.com/", "GET", true, Block},
		{"store page navigation", rw, "https://www.amazon.co.jp/dp/B000000000", "GET", true, Allow},
		{"dashboard post read-only", ro, "https://parents.amazon.co.jp/ajax/update", "POST", false, Block},
		{"dashboard read post", ro, "https://parents.amazon.co.jp/ajax/get-explore-page-data", "POST", false, Allow},
		{"dashboard read post other verb", ro, "https://parents.amazon.co.jp/ajax/fetch_items", "POST", false, Allow},
		{"dashboard write post", ro, "https://parents.amazon.co.jp/ajax/update-content-permission", "POST", false, Block},
		{"get not at start", ro, "https://parents.amazon.co.jp/ajax/set-target", "POST", false, Block},
		{"dashboard post read-write", rw, "https://parents.amazon.co.jp/ajax/update", "POST", false, Allow},
		{"cartography is not cart", rw, "https://www.amazon.co.jp/cartography/x", "GET", true, Allow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, why := tt.p.Check(tt.url, tt.method, tt.nav)
			if got != tt.want {
				t.Errorf("Check(%q, %q, %v) = %v (%s), want %v", tt.url, tt.method, tt.nav, got, why, tt.want)
			}
		})
	}
}
