// Package guard decides which requests the automated browser may make.
//
// The worker holds a session with full rights on the Amazon account, so the
// browser is restricted to the pages it needs. Anything that could buy,
// delete or deregister is refused outright.
package guard

import (
	"net/url"
	"regexp"
	"strings"
)

// Decision is the result of checking one request.
type Decision int

const (
	Allow Decision = iota
	Block
)

func (d Decision) String() string {
	if d == Allow {
		return "allow"
	}
	return "block"
}

// Policy holds the rules for one browser session.
type Policy struct {
	// ReadOnly blocks state-changing methods sent to the parent dashboard.
	// Sign-in still works, because sign-in lives on www.amazon.co.jp.
	ReadOnly bool
}

// navigationHosts are the hosts the browser may navigate to as a page.
// Subresources (images, scripts, telemetry) are not limited by host, so
// pages load the same way they do for a person.
var navigationHosts = []string{
	"parents.amazon.co.jp",
	"www.amazon.co.jp",
	"amazon.co.jp",
}

var parentHosts = []string{
	"parents.amazon.co.jp",
}

// deniedPath matches paths that must never be requested on an Amazon host.
var deniedPath = regexp.MustCompile(`(?i)(^|/)(gp/buy|gp/cart|cart|checkout|gp/checkoutportal|gp/aw/c|gp/digital/fiona/.*(delete|deregister)|hz/mycd/.*(delete|deregister|return))(/|$|\?)`)

var mutatingMethods = map[string]bool{
	"POST":   true,
	"PUT":    true,
	"PATCH":  true,
	"DELETE": true,
}

// Check decides whether a request may go out.
func (p Policy) Check(rawURL, method string, isNavigation bool) (Decision, string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return Block, "unparsable url"
	}
	if u.Scheme == "data" || u.Scheme == "blob" {
		return Allow, ""
	}
	host := strings.ToLower(u.Hostname())

	if isAmazonHost(host) && deniedPath.MatchString(u.Path) {
		return Block, "denied path"
	}
	if isNavigation && !hostIn(host, navigationHosts) {
		return Block, "navigation outside allowlist"
	}
	if p.ReadOnly && mutatingMethods[strings.ToUpper(method)] && hostIn(host, parentHosts) {
		return Block, "read-only mode"
	}
	return Allow, ""
}

func isAmazonHost(host string) bool {
	return host == "amazon.co.jp" || strings.HasSuffix(host, ".amazon.co.jp")
}

func hostIn(host string, list []string) bool {
	for _, h := range list {
		if host == h {
			return true
		}
	}
	return false
}
