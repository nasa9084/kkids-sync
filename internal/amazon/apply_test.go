package amazon

import "testing"

func TestExpectedWriteMatches(t *testing.T) {
	e := &expectedWrite{child: "amzn1.account.A", status: statusAdded, asin: "B000000001"}
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"exact", `{"childToAllowlistStatusMap":{"amzn1.account.A":"ADDED"},"asins":["B000000001"],"asinToContentTypeMap":{"B000000001":"EBOOK"}}`, true},
		{"other child", `{"childToAllowlistStatusMap":{"amzn1.account.B":"ADDED"},"asins":["B000000001"],"asinToContentTypeMap":{"B000000001":"EBOOK"}}`, false},
		{"both children", `{"childToAllowlistStatusMap":{"amzn1.account.A":"ADDED","amzn1.account.B":"ADDED"},"asins":["B000000001"]}`, false},
		{"wrong direction", `{"childToAllowlistStatusMap":{"amzn1.account.A":"NOT_ADDED"},"asins":["B000000001"]}`, false},
		{"other book", `{"childToAllowlistStatusMap":{"amzn1.account.A":"ADDED"},"asins":["B000000002"]}`, false},
		{"two books", `{"childToAllowlistStatusMap":{"amzn1.account.A":"ADDED"},"asins":["B000000001","B000000002"]}`, false},
		{"not a book", `{"childToAllowlistStatusMap":{"amzn1.account.A":"ADDED"},"asins":["B000000001"],"asinToContentTypeMap":{"B000000001":"APP"}}`, false},
		{"garbage", `{`, false},
	}
	for _, tt := range tests {
		if got := e.matches(tt.body); got != tt.want {
			t.Errorf("%s: matches = %v, want %v", tt.name, got, tt.want)
		}
	}
	var none *expectedWrite
	if none.matches(tests[0].body) {
		t.Error("nil expectation allowed a write")
	}
}

func TestWriteExpectedIsSingleUse(t *testing.T) {
	s := &Session{}
	body := `{"childToAllowlistStatusMap":{"amzn1.account.A":"NOT_ADDED"},"asins":["B000000001"]}`
	s.setExpect(&expectedWrite{child: "amzn1.account.A", status: statusNotAdded, asin: "B000000001"})
	if !s.writeExpected(body) {
		t.Fatal("expected write refused")
	}
	if s.writeExpected(body) {
		t.Error("the same write was allowed twice")
	}
}
