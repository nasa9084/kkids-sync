// Package titles reads series names and volume numbers out of Kindle
// titles. It has no dependencies so both web and the reconciler can use it.
package titles

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var (
	// Trailing label groups such as "(ヤングジャンプコミックスDIGITAL)" or "【電子版】".
	trailingLabel = regexp.MustCompile(`\s*(\([^()]*\)|【[^【】]*】|\[[^\[\]]*\])\s*$`)
	onlyDigits    = regexp.MustCompile(`^\d+$`)
	// A trailing group that is a volume, not a label: (3), (上), (II).
	volumeGroup = regexp.MustCompile(`^(\d+|[上中下]|[IVX]+)$`)
	// 〈新装版〉, 《完全版》, ...
	editionMark = regexp.MustCompile(`[〈《][^〉》]*版[〉》]`)
	// "テルマエ・ロマエIII": a Roman numeral glued to the end.
	romanEnd = regexp.MustCompile(`[IVX]+$`)

	// The steps below follow the owner's earlier Notion formulas: normalize,
	// remove edition notes, remove the label (brand), then find the episode
	// (volume) part; what is left is the series name.

	// 第一集 → 第1集 (NFKC already turns ①, full-width digits and so on into ASCII).
	kanjiVol = regexp.MustCompile(`第([一二三四五六七八九十])([巻集])`)

	// Edition notes anywhere in the title: 【電子書籍限定書き下ろしSS付き】,
	// 【単話版】, [雑誌], 特装版, ...
	specialPart = regexp.MustCompile(`【(((((電子(書籍)?|デジタル)版?|Amazon\.co\.jp)(限定)?)?((((イラスト)?特典)?(コラボ)?(ペーパー)?)|[書描]き下ろし(SS)?|おまけ|カラーレシピ)(特典)?付き? ?)|(単話|カラーページ増量)版|電子(単行本|版|特別版)|無料)(/共通ペーパー付き)?】|\[(電子書籍版|雑誌)\]|(通常|特装)版`)

	// Label groups anywhere: "(角川コミックス・エース)", "(講談社ノベルス)", ...
	brand = regexp.MustCompile(`\(([^()]*(文庫|新書|コミックス?|[Cc]omics?|COMICS?|ブックス|ノベルズ|ノベルス|BOOKS?|カドカワ|角川|講談社|コルク|アスキードワンゴ|MFC|GANMA!|ギター・マガジン)[^()]*)\)`)

	// The episode (volume) part. Leftmost match wins, as in the original.
	episode = regexp.MustCompile(`(^\d+ )|(^\d+: ([^ ]+)?)|(^#\d+)|(^\d巻「[^「」]+」)|(\([上中下]\))|([上中下]$)|(第?\d+[巻集]:?(( 「[^」]+」他?)|( 「[^」]+ 他」)|( 『[^』]+ 他』))?)|(\(\d+\) ?[〜~].+$)|(\(\d+\))|( ?【\d+】 ?)|(:? ?(Vol\.)?\d+ *$)|(\d{4}年 ?(\d\.)?\d{1,2}月(・(\d{4}年 ?)?\d{1,2}月)?号(\(vol\.\d+\))?$)|(第.部 ?「[^「」]+[0-9IVX]+」( ?本好きの下剋上 第[一二三四五六七八九十]部)?$)|([IVX]+[〜~ ].+$)|( [^ ]+[編章]( \d+)?$)`)

	firstNumber = regexp.MustCompile(`\d+`)
	yearMonth   = regexp.MustCompile(`(\d{4})年 ?(\d{1,2})月`)
	// "涼宮ハルヒの憂鬱 「涼宮ハルヒ」シリーズ": the title names its series.
	namedSeries = regexp.MustCompile(`「([^「」]+)」シリーズ`)
	romanTail   = regexp.MustCompile(`([IVX]+)[」]?`)
	upperMiddle = map[string]int{"上": 1, "中": 2, "下": 3}
	kanjiDigits = map[string]string{"一": "1", "二": "2", "三": "3", "四": "4", "五": "5", "六": "6", "七": "7", "八": "8", "九": "9", "十": "10"}
)

// Normalize prepares a title for matching: NFKC (full-width letters, digits
// and brackets, circled numbers), 第一集 → 第1集, and single spaces.
func Normalize(title string) string {
	t := norm.NFKC.String(title)
	t = kanjiVol.ReplaceAllStringFunc(t, func(m string) string {
		sub := kanjiVol.FindStringSubmatch(m)
		return "第" + kanjiDigits[sub[1]] + sub[2]
	})
	return strings.Join(strings.Fields(t), " ")
}

// StripLabels removes edition notes and label groups from a normalized
// title, leaving the name and the episode part.
func StripLabels(t string) string {
	t = specialPart.ReplaceAllString(t, "")
	t = editionMark.ReplaceAllString(t, "")
	t = brand.ReplaceAllString(t, "")
	// Other trailing groups that are not a volume number, e.g. "(MFコミックス アライブシリーズ)".
	for {
		m := trailingLabel.FindStringSubmatchIndex(t)
		if m == nil {
			break
		}
		inner := strings.Trim(t[m[2]:m[3]], "()【】[]")
		if volumeGroup.MatchString(inner) {
			break
		}
		t = t[:m[0]]
	}
	return strings.Join(strings.Fields(t), " ")
}

// ParseSeries guesses the series name and volume number from a Kindle title,
// e.g. "パリピ孔明（２７） (コミックDAYSコミックス)" → ("パリピ孔明", 27).
// ok is false for titles that do not look like part of a series. The admin
// page can fix wrong guesses, and rules there are tried before this.
func ParseSeries(title string) (name string, volume int, ok bool) {
	t := StripLabels(Normalize(title))
	if m := namedSeries.FindStringSubmatch(t); m != nil {
		_, v, _ := parseEpisode(namedSeries.ReplaceAllString(t, ""))
		return m[1], v, true
	}
	return parseEpisode(t)
}

func parseEpisode(t string) (name string, volume int, ok bool) {
	t = strings.TrimSpace(t)
	loc := episode.FindStringIndex(t)
	if loc == nil {
		// "テルマエ・ロマエIII": only when the numeral follows a non-Latin
		// character, so that "WALL-E" or "ROMAX" stay whole.
		if m := romanEnd.FindStringIndex(t); m != nil && m[0] > 0 {
			if r, _ := utf8.DecodeLastRuneInString(t[:m[0]]); r > unicode.MaxASCII {
				loc = m
			}
		}
	}
	if loc == nil {
		return "", 0, false
	}
	part := t[loc[0]:loc[1]]
	// The name is what comes before the episode part; a subtitle after it
	// ("サイコロジカル(上) 兎吊木垓輔の戯言殺し") is not part of the series.
	// Only when the episode part leads ("#07 ...") is the rest the name.
	name = t[:loc[0]]
	if strings.TrimSpace(name) == "" {
		name = t[loc[1]:]
		// "#07 <episode title>: <x>編 <series>", "第13集: <episode title> 他 <series>"
		for _, sep := range []string{"編 ", " 他 "} {
			if i := strings.LastIndex(name, sep); i >= 0 {
				name = name[i+len(sep):]
				break
			}
		}
	}
	name = strings.TrimRight(strings.TrimSpace(name), " :-‐、,")
	if name == "" {
		return "", 0, false
	}
	return name, volumeOf(part), true
}

// volumeOf reads the volume out of an episode part: the first number, or
// 上/中/下, or a Roman numeral; 0 when there is none.
func volumeOf(part string) int {
	if m := yearMonth.FindStringSubmatch(part); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		return y*100 + mo
	}
	if n := firstNumber.FindString(part); n != "" {
		if v, err := strconv.Atoi(n); err == nil {
			return v
		}
	}
	for k, v := range upperMiddle {
		if strings.Contains(part, k) {
			return v
		}
	}
	if m := romanTail.FindStringSubmatch(part); m != nil {
		return roman(m[1])
	}
	return 0
}

func roman(s string) int {
	vals := map[byte]int{'I': 1, 'V': 5, 'X': 10}
	n := 0
	for i := 0; i < len(s); i++ {
		v := vals[s[i]]
		if i+1 < len(s) && vals[s[i+1]] > v {
			n -= v
		} else {
			n += v
		}
	}
	return n
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
