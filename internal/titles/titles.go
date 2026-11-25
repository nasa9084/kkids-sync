// Package titles reads series names and volume numbers out of Kindle
// titles. It has no dependencies so both web and the reconciler can use it.
package titles

import (
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var (
	// Trailing label groups such as "(ヤングジャンプコミックスDIGITAL)" or "【電子版】".
	trailingLabel = regexp.MustCompile(`\s*(\([^()]*\)|【[^【】]*】|\[[^\[\]]*\])\s*$`)
	volParen      = regexp.MustCompile(`^(.*?)\s*\((\d{1,4})\)`)
	volKan        = regexp.MustCompile(`^(.*?)\s*第?(\d{1,4})\s*巻`)
	volSpaced     = regexp.MustCompile(`^(.*\S)\s+(\d{1,4})$`) // "アイシールド21 11"
	volTight      = regexp.MustCompile(`^(.*\D)(\d{1,4})$`)    // "異世界転移、地雷付き。2"
	onlyDigits    = regexp.MustCompile(`^\d+$`)
)

// ParseSeries guesses the series name and volume number from a Kindle title,
// e.g. "パリピ孔明（２７） (コミックDAYSコミックス)" → ("パリピ孔明", 27).
// ok is false for titles that do not look like part of a numbered series.
// The admin page can fix wrong guesses.
func ParseSeries(title string) (name string, volume int, ok bool) {
	t := strings.TrimSpace(norm.NFKC.String(title))
	// Drop label groups at the end, but keep "(27)": that is the volume.
	for {
		m := trailingLabel.FindStringSubmatchIndex(t)
		if m == nil {
			break
		}
		inner := strings.Trim(t[m[2]:m[3]], "()【】[]")
		if onlyDigits.MatchString(inner) {
			break
		}
		t = strings.TrimSpace(t[:m[0]])
	}
	for _, re := range []*regexp.Regexp{volParen, volKan, volSpaced, volTight} {
		if m := re.FindStringSubmatch(t); m != nil {
			name = strings.TrimRight(strings.TrimSpace(m[1]), " :：-‐")
			v, err := strconv.Atoi(m[2])
			if err != nil || name == "" {
				continue
			}
			return name, v, true
		}
	}
	return "", 0, false
}

// DisplayTitle drops label groups such as "(角川コミックス・エース)" from the
// end of a title for display; volume numbers like "(27)" stay.
func DisplayTitle(title string) string {
	t := strings.TrimSpace(title)
	for {
		m := trailingLabel.FindStringSubmatchIndex(norm.NFKC.String(t))
		if m == nil {
			return t
		}
		// Work on the original text: find the last opening bracket of the
		// same kind and cut there, unless the group is a volume number.
		n := norm.NFKC.String(t)
		inner := strings.Trim(n[m[2]:m[3]], "()【】[]")
		if onlyDigits.MatchString(inner) {
			return t
		}
		cut := strings.LastIndexAny(t, "(（【[［")
		if cut <= 0 {
			return t
		}
		t = strings.TrimSpace(t[:cut])
	}
}
