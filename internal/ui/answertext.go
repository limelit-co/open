// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

// Rendering an answer for a human to read.
//
// Engines answer in Markdown. Showing it raw puts "([peec.ai](https://peec.ai/
// pricing?utm_source=openai))" in the middle of a sentence, on the one screen
// whose whole job is to be read.
//
// The hard part is that the brand highlights are byte offsets into the raw
// text, produced by the same matcher that produced the counts. Converting the
// Markdown first would invalidate every offset; highlighting first would mean
// running Markdown over HTML and mangling the marks. So the marks go in as
// sentinel bytes that no Markdown rule can match, the conversion runs, and
// the sentinels become tags at the end. That keeps the highlight and the
// number derived from the same offsets, which is the one thing an evidence
// screen must never get wrong.

import (
	"fmt"
	"html/template"
	"regexp"
	"sort"
	"strings"

	"github.com/limelit-co/open/internal/metrics"
)

// Sentinels, from the Unicode Private Use Area.
//
// Not NUL and friends: html/template's escaper rewrites C0 control bytes to
// the replacement character, which silently ate the opening mark and left a
// stray closing tag. These pass through escaping untouched, cannot appear in
// an engine's answer, and match none of the Markdown patterns below.
const (
	markOwnOpen   = "\uE000"
	markRivalOpen = "\uE001"
	markClose     = "\uE002"

	// Placeholders for an anchor that has already been built.
	linkOpen  = "\uE010"
	linkClose = "\uE011"
)

var (
	mdLink   = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^\s)]+)\)`)
	mdBold   = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	mdItalic = regexp.MustCompile(`(^|[\s(])\*([^*\n]+)\*`)
	mdCode   = regexp.MustCompile("`([^`\n]+)`")
	mdHead   = regexp.MustCompile(`^#{1,6}\s+(.*)$`)
	mdBullet = regexp.MustCompile(`^\s*[-*+]\s+(.*)$`)
	mdNumber = regexp.MustCompile(`^\s*(\d+)[.)]\s+(.*)$`)
	// A bare URL left over after links are handled, so a raw link is still
	// clickable rather than a wall of query string.
	mdBareURL = regexp.MustCompile(`(^|[\s(])(https?://[^\s)<]+)`)
)

// renderAnswer turns one answer into readable HTML with its brands marked.
func renderAnswer(text string, brands []metrics.ChatBrand) template.HTML {
	marked := insertMarks(text, brands)
	rendered := markdownToHTML(marked)
	rendered = strings.ReplaceAll(rendered, markOwnOpen, `<mark class="mark-own">`)
	rendered = strings.ReplaceAll(rendered, markRivalOpen, `<mark class="mark-rival">`)
	rendered = strings.ReplaceAll(rendered, markClose, `</mark>`)
	return template.HTML(rendered)
}

// insertMarks places sentinels at the matcher's byte offsets.
func insertMarks(text string, brands []metrics.ChatBrand) string {
	type span struct {
		start, end int
		own        bool
	}
	spans := make([]span, 0, len(brands))
	for _, b := range brands {
		if b.Offset < 0 || b.Offset >= len(text) {
			continue
		}
		end := b.Offset + len(b.Name)
		if end > len(text) {
			end = len(text)
		}
		spans = append(spans, span{b.Offset, end, b.IsOwn})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

	var (
		b    strings.Builder
		last int
	)
	for _, s := range spans {
		// Overlapping matches would nest marks and break the markup. The
		// first one wins, which is also what the longest-match rule in the
		// matcher already guarantees for the common case.
		if s.start < last {
			continue
		}
		b.WriteString(text[last:s.start])
		if s.own {
			b.WriteString(markOwnOpen)
		} else {
			b.WriteString(markRivalOpen)
		}
		b.WriteString(text[s.start:s.end])
		b.WriteString(markClose)
		last = s.end
	}
	b.WriteString(text[last:])
	return b.String()
}

// markdownToHTML handles the small subset of Markdown that engines actually
// use in an answer. It is deliberately not a full parser: anything it does
// not recognise is escaped and shown as written, which is the safe direction
// to fail in.
func markdownToHTML(in string) string {
	lines := strings.Split(strings.ReplaceAll(in, "\r\n", "\n"), "\n")

	var (
		out    strings.Builder
		inList bool
		para   []string
	)

	flushPara := func() {
		if len(para) == 0 {
			return
		}
		fmt.Fprintf(&out, "<p>%s</p>", strings.Join(para, "<br>"))
		para = nil
	}
	closeList := func() {
		if inList {
			out.WriteString("</ul>")
			inList = false
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flushPara()
			closeList()
			continue
		}
		// A horizontal rule is noise in an answer excerpt.
		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			flushPara()
			closeList()
			continue
		}
		if m := mdHead.FindStringSubmatch(trimmed); m != nil {
			flushPara()
			closeList()
			fmt.Fprintf(&out, "<h3>%s</h3>", inlineMarkdown(m[1]))
			continue
		}
		if m := mdBullet.FindStringSubmatch(line); m != nil {
			flushPara()
			if !inList {
				out.WriteString("<ul>")
				inList = true
			}
			fmt.Fprintf(&out, "<li>%s</li>", inlineMarkdown(m[1]))
			continue
		}
		if m := mdNumber.FindStringSubmatch(line); m != nil {
			flushPara()
			if !inList {
				out.WriteString("<ul>")
				inList = true
			}
			// Rendered as a list item carrying its own number, so the rank an
			// engine gave is preserved exactly rather than renumbered by the
			// browser. The rank is the measurement.
			fmt.Fprintf(&out, `<li><span class="li-rank">%s.</span> %s</li>`, template.HTMLEscapeString(m[1]), inlineMarkdown(m[2]))
			continue
		}
		closeList()
		para = append(para, inlineMarkdown(trimmed))
	}
	flushPara()
	closeList()
	return out.String()
}

// inlineMarkdown escapes one line and then applies inline formatting.
//
// Escaping first is what makes this safe: every angle bracket in the engine's
// text is inert before any tag is introduced.
//
// Anchors are parked behind a placeholder as soon as they are built. Without
// that, the bare-URL rule matches the URL inside the href the link rule just
// wrote and wraps it a second time, which spills rel and target attributes
// into the visible sentence.
func inlineMarkdown(s string) string {
	s = template.HTMLEscapeString(s)

	var parked []string
	park := func(html string) string {
		parked = append(parked, html)
		return fmt.Sprintf("%s%d%s", linkOpen, len(parked)-1, linkClose)
	}

	s = mdLink.ReplaceAllStringFunc(s, func(m string) string {
		parts := mdLink.FindStringSubmatch(m)
		return park(anchor(parts[2], parts[1]))
	})
	s = mdBareURL.ReplaceAllStringFunc(s, func(m string) string {
		parts := mdBareURL.FindStringSubmatch(m)
		return parts[1] + park(anchor(parts[2], shortenURL(stripMarks(parts[2]))))
	})
	s = mdCode.ReplaceAllString(s, `<code>$1</code>`)
	s = mdBold.ReplaceAllString(s, `<strong>$1</strong>`)
	s = mdItalic.ReplaceAllString(s, `$1<em>$2</em>`)

	for i, html := range parked {
		s = strings.Replace(s, fmt.Sprintf("%s%d%s", linkOpen, i, linkClose), html, 1)
	}
	return s
}

// anchor builds one outbound link. nofollow and noreferrer because the text
// is a third party's and this tool should not pass rank or referrer on their
// behalf.
//
// The href is stripped of mark sentinels first, and that is not cosmetic. A
// brand key is often a domain, so the matcher legitimately matches "peec.ai"
// inside https://peec.ai/pricing. Leaving the sentinel there would put a
// <mark> tag inside an href attribute, which the browser parses as a broken
// tag and spills the remaining attributes into the sentence as text.
func anchor(href, label string) string {
	return fmt.Sprintf(`<a href="%s" rel="noopener noreferrer nofollow" target="_blank">%s</a>`,
		safeHref(stripMarks(href)), label)
}

// stripMarks removes the sentinels from a string that is about to become an
// attribute value rather than visible text.
func stripMarks(s string) string {
	return strings.NewReplacer(markOwnOpen, "", markRivalOpen, "", markClose, "").Replace(s)
}

// safeHref allows only http and https. The text came from a third party, and
// a javascript: or data: href is a script vector.
func safeHref(raw string) string {
	raw = strings.TrimRight(raw, ".,;:")
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return "#"
	}
	return raw
}

// shortenURL keeps a bare link from swallowing the sentence it sits in.
func shortenURL(raw string) string {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	trimmed = strings.TrimPrefix(trimmed, "www.")
	if i := strings.IndexAny(trimmed, "/?#"); i > 0 {
		return trimmed[:i]
	}
	return trimmed
}

// brandChips groups repeated mentions of one brand into a single chip.
//
// A brand named eight times in one answer is one brand, and eight identical
// chips in a row say nothing the count does not. The best rank is kept,
// because the best position the engine gave is the fact that matters.
func brandChips(brands []metrics.ChatBrand) []BrandChipView {
	type agg struct {
		name  string
		own   bool
		count int
		best  int
		order int
	}
	byName := map[string]*agg{}
	order := 0
	for _, b := range brands {
		a, ok := byName[b.Name]
		if !ok {
			a = &agg{name: b.Name, own: b.IsOwn, order: order}
			byName[b.Name] = a
			order++
		}
		a.count++
		if b.Position > 0 && (a.best == 0 || b.Position < a.best) {
			a.best = b.Position
		}
	}

	list := make([]*agg, 0, len(byName))
	for _, a := range byName {
		list = append(list, a)
	}
	// Own brand first, then by best rank, then by first appearance.
	sort.Slice(list, func(i, j int) bool {
		if list[i].own != list[j].own {
			return list[i].own
		}
		ri, rj := list[i].best, list[j].best
		if ri == 0 {
			ri = 1 << 30
		}
		if rj == 0 {
			rj = 1 << 30
		}
		if ri != rj {
			return ri < rj
		}
		return list[i].order < list[j].order
	})

	out := make([]BrandChipView, 0, len(list))
	for _, a := range list {
		chip := BrandChipView{Name: a.name, IsOwn: a.own, Position: rankLabel(a.best)}
		if a.count > 1 {
			chip.Times = fmt.Sprintf("%d times", a.count)
		}
		out = append(out, chip)
	}
	return out
}
