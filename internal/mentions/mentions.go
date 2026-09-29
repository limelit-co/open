// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package mentions finds, in an answer, the brands we were told to look for.
//
// It is a text search, not a model call, and that is the whole design. A
// model asked "which brands appear here" can invent one that does not, miss
// one that plainly does, and give a different answer on Tuesday. A search for
// strings we already hold cannot do any of those things, so the number it
// produces can be recomputed from the stored answer and will come out the
// same every time.
//
// What that buys and what it costs are both worth being explicit about. It
// buys reproducibility and zero per-answer cost, which is why the headline
// number in this project is one you can audit. It costs discovery: a brand
// nobody listed is invisible here. Finding unlisted brands, and reading how
// each one is framed, needs a model and is a Limelit Cloud feature.
package mentions

import (
	"regexp"
	"strings"
	"unicode"
)

// NormalizeKey reduces a brand string to its matching key: lowercased, with
// every rune that is not a letter or digit dropped.
//
// "Run:AI", "RunAI" and "run ai" all become "runai", so a brand written three
// ways in one answer is one brand. The same key is what links a mention row
// back to a competitor, so changing this function changes what existing rows
// mean: it is the kind of thing to migrate rather than edit.
func NormalizeKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Brand is one thing to look for.
type Brand struct {
	// CompetitorID is nil for the property itself.
	CompetitorID *int64
	// Name is the display name, used for the mention row.
	Name string
	// Aliases are the other spellings, including the display name.
	Aliases []string
	// Domain is the brand's site. A citation to it counts as a mention, and
	// the domain written in prose counts too.
	Domain string
}

// Mention is one occurrence of one brand in one answer.
type Mention struct {
	CompetitorID *int64
	BrandKey     string
	BrandName    string
	// Start and End are byte offsets into the answer text, so the dashboard
	// can highlight exactly what matched.
	Start int
	End   int
	// ListRank is the 1-based position of the enclosing list item, nil when
	// the mention is not inside a list. It is what makes "third in a ranked
	// list" different from "mentioned in passing".
	ListRank *int
}

// Matcher searches an answer for a fixed set of brands.
type Matcher struct {
	brands []Brand
}

// NewMatcher builds a matcher. Brands with no searchable string are dropped
// rather than matching everything.
func NewMatcher(brands []Brand) *Matcher {
	var kept []Brand
	for _, b := range brands {
		b.Aliases = usefulTerms(append([]string{b.Name}, b.Aliases...))
		if len(b.Aliases) == 0 && strings.TrimSpace(b.Domain) == "" {
			continue
		}
		kept = append(kept, b)
	}
	return &Matcher{brands: kept}
}

// Find returns every mention in text, in document order.
//
// Overlaps are resolved by preferring the longest match at a position, so
// "Acme Analytics" is not also reported as "Acme" when both are tracked. One
// brand matched several times keeps every occurrence: the count is evidence,
// and the dashboard decides what to do with it.
func (m *Matcher) Find(text string) []Mention {
	if text == "" || len(m.brands) == 0 {
		return nil
	}
	ranks := listRanks(text)

	var found []Mention
	for _, brand := range m.brands {
		for _, term := range brand.searchTerms() {
			for _, span := range findTerm(text, term) {
				found = append(found, Mention{
					CompetitorID: brand.CompetitorID,
					BrandKey:     NormalizeKey(brand.Name),
					BrandName:    brand.Name,
					Start:        span[0],
					End:          span[1],
					ListRank:     ranks.at(span[0]),
				})
			}
		}
	}
	return dedupe(found)
}

// searchTerms is every string that counts as this brand: its names and its
// domain. The domain is included because an answer that links a brand without
// naming it has still put that brand in front of the reader.
func (b Brand) searchTerms() []string {
	terms := append([]string(nil), b.Aliases...)
	if d := strings.TrimSpace(strings.ToLower(b.Domain)); d != "" {
		terms = append(terms, d)
	}
	return terms
}

// usefulTerms drops blanks and anything too short to search for safely.
//
// A one or two character term matches inside ordinary words: a brand called
// "Go" would match "going", "good" and "category". Two characters is where
// the false positives stop being occasional and start being constant, and a
// wrong mention is worse than a missing one because it silently inflates the
// number the whole product is about.
func usefulTerms(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if len([]rune(s)) < 3 || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	return out
}

// findTerm returns the byte spans where term occurs as a whole word.
//
// Whole word matters both ways. Without it "Acme" matches inside "Acmeify";
// with a naive word boundary "acme.com" would never match, because a dot is
// not a word character. So the boundary test is on the characters either
// side: a match is real when neither neighbour is a letter or a digit.
func findTerm(text, term string) [][2]int {
	if term == "" {
		return nil
	}
	lowerText := strings.ToLower(text)
	lowerTerm := strings.ToLower(term)

	var spans [][2]int
	for offset := 0; ; {
		i := strings.Index(lowerText[offset:], lowerTerm)
		if i < 0 {
			break
		}
		start := offset + i
		end := start + len(lowerTerm)
		if boundedAt(lowerText, start, end) {
			spans = append(spans, [2]int{start, end})
		}
		offset = start + 1
	}
	return spans
}

func boundedAt(text string, start, end int) bool {
	before := byte(' ')
	if start > 0 {
		before = text[start-1]
	}
	after := byte(' ')
	if end < len(text) {
		after = text[end]
	}
	return !isWordByte(before) && !isWordByte(after)
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// dedupe keeps one mention per position, preferring the longest match, and
// returns them in document order.
func dedupe(in []Mention) []Mention {
	if len(in) == 0 {
		return nil
	}
	// Longest first at each start, so the longer brand name wins.
	best := map[int]Mention{}
	for _, m := range in {
		if cur, ok := best[m.Start]; !ok || m.End-m.Start > cur.End-cur.Start {
			best[m.Start] = m
		}
	}

	starts := make([]int, 0, len(best))
	for s := range best {
		starts = append(starts, s)
	}
	sortInts(starts)

	var out []Mention
	lastEnd := -1
	for _, s := range starts {
		m := best[s]
		// A shorter match inside one already taken is the same mention.
		if m.Start < lastEnd {
			continue
		}
		out = append(out, m)
		lastEnd = m.End
	}
	return out
}

func sortInts(v []int) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j-1] > v[j]; j-- {
			v[j-1], v[j] = v[j], v[j-1]
		}
	}
}

// listItem is the byte range of one list item and its rank.
type listItem struct {
	start, end, rank int
}

type listMap []listItem

func (l listMap) at(offset int) *int {
	for _, item := range l {
		if offset >= item.start && offset < item.end {
			rank := item.rank
			return &rank
		}
	}
	return nil
}

// listLine matches the start of a list item: "1." or "1)" for a numbered
// list, and "-", "*" or a bullet for an unordered one.
//
// An unordered list still gets a rank, because models write their
// recommendation order into the sequence whether or not they number it, and a
// brand first in a bulleted list of five is not in the same position as the
// brand last in it.
var listLine = regexp.MustCompile(`(?m)^[ \t]*(?:(\d{1,2})[.)]|[-*\x{2022}])[ \t]+`)

// listRanks maps each list item to its rank, restarting at every new list.
//
// A list ends at a blank line followed by non-list prose, which is how a
// model separates "here are five tools" from the paragraph after it. Without
// the restart, a second list halfway down an answer would keep counting from
// the first and report rank 9 for something the model put first.
//
// An item's span ends where that prose begins, so a brand named in the
// paragraph after a list is prose and has no rank. A marker indented deeper
// than the list's own items opens a nested list; it belongs to the item above
// it, so "1." with two sub-bullets under it is followed by item 2, not item 4.
func listRanks(text string) listMap {
	matches := listLine.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return nil
	}

	type item struct{ start, indent, rank int }
	var (
		items  []item
		rank   int
		indent = -1
	)
	for _, m := range matches {
		start := m[0]
		ind := indentWidth(text[start:m[1]])
		// Prose between the previous item and this one means the earlier
		// list ended, however this one is indented.
		if n := len(items); n > 0 && itemEnd(text, items[n-1].start, start, items[n-1].indent) < start {
			rank, indent = 0, -1
		}
		if indent >= 0 && ind > indent {
			continue
		}
		if m[2] >= 0 && atoi(text[m[2]:m[3]]) == 1 {
			// Numbered: trust the number the model wrote, so a list that
			// starts at 1 after an aside is read as a new list.
			rank = 0
		}
		rank++
		indent = ind
		items = append(items, item{start: start, indent: ind, rank: rank})
	}

	out := make(listMap, 0, len(items))
	for i, it := range items {
		next := len(text)
		if i+1 < len(items) {
			next = items[i+1].start
		}
		out = append(out, listItem{start: it.start, end: itemEnd(text, it.start, next, it.indent), rank: it.rank})
	}
	return out
}

// itemEnd is where the list item starting at start stops: at next, the start
// of the following item, or earlier at a blank line whose next line is prose.
// A line is prose when it is not indented under the item and is not itself a
// list marker; an indented paragraph or a nested list stays in the item.
func itemEnd(text string, start, next, indent int) int {
	for off := start; ; {
		i := strings.Index(text[off:next], "\n\n")
		if i < 0 {
			return next
		}
		blank := off + i
		line := strings.TrimLeft(text[blank:next], "\n")
		if nl := strings.IndexByte(line, '\n'); nl >= 0 {
			line = line[:nl]
		}
		if strings.TrimSpace(line) != "" && indentWidth(line) <= indent && !listLine.MatchString(line) {
			return blank
		}
		off = blank + 2
	}
}

// indentWidth is the width of a line's leading whitespace, a tab counting as
// four spaces.
func indentWidth(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// IsBranded reports whether a prompt names one of these brands, which is what
// tags it as branded and keeps it out of the headline number.
func IsBranded(text string, brand Brand) bool {
	m := NewMatcher([]Brand{brand})
	return len(m.Find(text)) > 0
}
