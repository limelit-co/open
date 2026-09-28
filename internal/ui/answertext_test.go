// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"strings"
	"testing"

	"github.com/limelit-co/open/internal/metrics"
)

// TestRenderAnswerMarksTheBrandAtTheMatcherOffset is the invariant this whole
// file exists to protect: the highlight and the count come from the same
// offsets, so the screen cannot show a different number of brands than the
// metric does.
func TestRenderAnswerMarksTheBrandAtTheMatcherOffset(t *testing.T) {
	text := "Acme leads the field, ahead of Rival."
	brands := []metrics.ChatBrand{
		{Name: "Acme", IsOwn: true, Offset: 0},
		{Name: "Rival", IsOwn: false, Offset: strings.Index(text, "Rival")},
	}
	got := string(renderAnswer(text, brands))

	if !strings.Contains(got, `<mark class="mark-own">Acme</mark>`) {
		t.Errorf("own brand not marked: %s", got)
	}
	if !strings.Contains(got, `<mark class="mark-rival">Rival</mark>`) {
		t.Errorf("rival not marked: %s", got)
	}
}

func TestRenderAnswerMarksInsideMarkdown(t *testing.T) {
	// The matcher sees the raw text, so an offset frequently lands inside
	// Markdown syntax. Converting first would break every offset.
	text := "**Profound** is the leader."
	brands := []metrics.ChatBrand{{Name: "Profound", Offset: strings.Index(text, "Profound")}}
	got := string(renderAnswer(text, brands))

	if strings.Contains(got, "**") {
		t.Errorf("markdown syntax survived: %s", got)
	}
	if !strings.Contains(got, "<strong>") {
		t.Errorf("bold was not rendered: %s", got)
	}
	if !strings.Contains(got, "Profound</mark>") {
		t.Errorf("the brand inside the bold was not marked: %s", got)
	}
}

func TestRenderAnswerConvertsLinks(t *testing.T) {
	text := "See ([peec.ai](https://peec.ai/pricing?utm_source=openai)) for pricing."
	got := string(renderAnswer(text, nil))

	if strings.Contains(got, "](http") {
		t.Errorf("raw link syntax survived: %s", got)
	}
	if !strings.Contains(got, `href="https://peec.ai/pricing?utm_source=openai"`) {
		t.Errorf("link not converted: %s", got)
	}
	if !strings.Contains(got, `rel="noopener noreferrer nofollow"`) {
		t.Error("an outbound link from third-party text must not pass referrer or rank")
	}
}

// TestRenderAnswerNeverBuildsADangerousHref: the answer text comes from a
// third party, so a javascript: or data: href would be a script vector. The
// scheme appearing as escaped literal text is harmless; it appearing inside
// an href is not, so that is what this checks.
func TestRenderAnswerNeverBuildsADangerousHref(t *testing.T) {
	for _, bad := range []string{
		"[click](javascript:alert(1))",
		"[click](data:text/html,<script>alert(1)</script>)",
		"[click](vbscript:msgbox)",
		"[click](JaVaScRiPt:alert(1))",
	} {
		got := strings.ToLower(string(renderAnswer(bad, nil)))
		for _, scheme := range []string{"javascript:", "vbscript:", "data:"} {
			if strings.Contains(got, `href="`+scheme) {
				t.Errorf("built a %s href from %q: %s", scheme, bad, got)
			}
		}
	}
	// safeHref is the backstop if the link pattern is ever widened.
	if got := safeHref("javascript:alert(1)"); got != "#" {
		t.Errorf("safeHref passed a script scheme: %q", got)
	}
}

func TestRenderAnswerEscapesHTML(t *testing.T) {
	got := string(renderAnswer(`<script>alert("x")</script> and <img onerror=y>`, nil))
	if strings.Contains(got, "<script") || strings.Contains(got, "<img") {
		t.Errorf("raw HTML from the engine survived: %s", got)
	}
}

func TestRenderAnswerKeepsEngineNumbering(t *testing.T) {
	// The rank an engine gave IS the measurement. Letting the browser
	// renumber a list would quietly change it.
	got := string(renderAnswer("1. Profound\n2. Otterly\n3. Peec AI", nil))
	for _, n := range []string{"1.", "2.", "3."} {
		if !strings.Contains(got, n) {
			t.Errorf("rank %q was dropped: %s", n, got)
		}
	}
}

func TestRenderAnswerHandlesHeadingsAndBullets(t *testing.T) {
	got := string(renderAnswer("## Options\n\n- One\n- Two\n\nA closing line.", nil))
	for _, want := range []string{"<h3>Options</h3>", "<ul>", "<li>One</li>", "<p>A closing line.</p>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
	if strings.Contains(got, "##") {
		t.Errorf("heading syntax survived: %s", got)
	}
}

func TestRenderAnswerOverlappingMatchesDoNotNest(t *testing.T) {
	// Nested marks would be broken markup. The first match wins.
	text := "Acme Corp leads."
	brands := []metrics.ChatBrand{
		{Name: "Acme Corp", IsOwn: true, Offset: 0},
		{Name: "Acme", IsOwn: true, Offset: 0},
	}
	got := string(renderAnswer(text, brands))
	if strings.Count(got, "<mark") != 1 {
		t.Errorf("expected one mark, got: %s", got)
	}
}

func TestBrandChipsGroupRepeats(t *testing.T) {
	brands := []metrics.ChatBrand{
		{Name: "Peec AI", Position: 1}, {Name: "Peec AI", Position: 3},
		{Name: "Peec AI", Position: 0}, {Name: "Otterly", Position: 2},
		{Name: "Acme", IsOwn: true, Position: 0},
	}
	chips := brandChips(brands)
	if len(chips) != 3 {
		t.Fatalf("chips = %d, want 3 distinct brands", len(chips))
	}
	// Own brand first even when it has no rank: it is the one the reader
	// came to check.
	if !chips[0].IsOwn {
		t.Errorf("own brand is not first: %+v", chips)
	}
	var peec *BrandChipView
	for i := range chips {
		if chips[i].Name == "Peec AI" {
			peec = &chips[i]
		}
	}
	if peec == nil || peec.Position != "#1" {
		t.Fatalf("Peec AI = %+v, want best rank #1", peec)
	}
	if peec.Times != "3 times" {
		t.Errorf("Times = %q, want the repeat count", peec.Times)
	}
	for _, c := range chips {
		if c.Name == "Otterly" && c.Times != "" {
			t.Errorf("a single mention should carry no count: %+v", c)
		}
	}
}

func TestShortenURLKeepsLinksReadable(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.peec.ai/pricing?utm_source=openai": "peec.ai",
		"http://example.com":                            "example.com",
		"https://sub.example.co.uk/a/b":                 "sub.example.co.uk",
	} {
		if got := shortenURL(in); got != want {
			t.Errorf("shortenURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRenderAnswerDoesNotDoubleWrapALink caught a real bug: the bare-URL rule
// matched the URL inside the href the link rule had just written, spilling
// rel and target attributes into the visible sentence.
func TestRenderAnswerDoesNotDoubleWrapALink(t *testing.T) {
	text := "Plans start at $95/month. ([peec.ai](https://peec.ai/pricing?utm_source=openai))"
	got := string(renderAnswer(text, nil))

	if strings.Count(got, "<a ") != 1 {
		t.Errorf("expected exactly one anchor, got: %s", got)
	}
	// The bug wrote the attribute run twice, so counting it is the precise
	// check: once is the real anchor, twice means the URL inside the href
	// was matched again and wrapped.
	if n := strings.Count(got, `rel="noopener noreferrer nofollow"`); n != 1 {
		t.Errorf("attribute run appears %d times, want 1: %s", n, got)
	}
	// The visible label is the link text the engine wrote, not the URL.
	if !strings.Contains(got, ">peec.ai</a>") {
		t.Errorf("link label lost: %s", got)
	}
}

func TestRenderAnswerLinksABareURL(t *testing.T) {
	got := string(renderAnswer("See https://example.com/a/b?c=d for more.", nil))
	if strings.Count(got, "<a ") != 1 {
		t.Errorf("expected one anchor: %s", got)
	}
	// A bare URL is shortened so it does not swallow the sentence.
	if !strings.Contains(got, ">example.com</a>") {
		t.Errorf("bare URL not shortened: %s", got)
	}
}

// TestRenderAnswerHandlesABrandInsideALinkURL is the real-world case that
// broke the page. A brand key is often a domain, so the matcher matches
// "peec.ai" inside https://peec.ai/pricing, and a <mark> inside an href
// makes the browser treat the rest of the attributes as text.
func TestRenderAnswerHandlesABrandInsideALinkURL(t *testing.T) {
	text := "Plans start at $95/month. ([peec.ai](https://peec.ai/pricing?utm_source=openai))"
	brands := []metrics.ChatBrand{
		{Name: "peec.ai", Offset: strings.Index(text, "[peec.ai]") + 1},
		{Name: "peec.ai", Offset: strings.Index(text, "https://peec.ai") + len("https://")},
	}
	got := string(renderAnswer(text, brands))

	if strings.Contains(got, `href="https://<mark`) {
		t.Fatalf("a mark tag landed inside the href: %s", got)
	}
	if !strings.Contains(got, `href="https://peec.ai/pricing?utm_source=openai"`) {
		t.Errorf("href is not clean: %s", got)
	}
	if n := strings.Count(got, `rel="noopener noreferrer nofollow"`); n != 1 {
		t.Errorf("attribute run appears %d times, want 1: %s", n, got)
	}
	// The visible label still carries its highlight.
	if !strings.Contains(got, `<mark class="mark-rival">peec.ai</mark></a>`) {
		t.Errorf("the link label lost its mark: %s", got)
	}
}
