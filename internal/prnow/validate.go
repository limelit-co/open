// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package prnow

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The limits PRNow documents for a release. PRNow has the final say; these
// exist so a draft that cannot pass is caught here, before a round trip, and
// with every problem listed at once rather than one per attempt.
const (
	TitleMinChars      = 5
	TitleMaxChars      = 200
	TitleMinWords      = 5
	SummaryMaxWords    = 45
	ContentMinWords    = 250
	ContentMaxWords    = 1200
	MaxLinks           = 5
	MaxImages          = 3
	MinCategories      = 1
	MaxCategories      = 5
	MetaTitleMax       = 60
	MetaDescriptionMax = 200
	MetaKeywordsMax    = 250
)

// Problems is everything wrong with a draft. Warnings do not stop a submit.
type Problems struct {
	Errors   []string
	Warnings []string
}

// OK reports whether the draft can be sent.
func (p Problems) OK() bool { return len(p.Errors) == 0 }

func (p Problems) Error() string {
	return "the release does not meet PRNow's limits:\n  - " + strings.Join(p.Errors, "\n  - ")
}

var (
	tagRE   = regexp.MustCompile(`(?s)<[^>]*>`)
	linkRE  = regexp.MustCompile(`(?i)<a\s[^>]*href\s*=`)
	imageRE = regexp.MustCompile(`(?i)<img\s`)
	// htmlRE recognises content that is already HTML, so a stray "<" in
	// plain text does not count as markup.
	htmlRE = regexp.MustCompile(`(?i)</?(p|a|br|h[1-6]|ul|ol|li|div|strong|em|b|i|img|embed|iframe|blockquote)\b`)
)

// Validate checks d against the documented limits. plan is not checked:
// PRNow does not publish its plan names, so it is passed through as given.
func Validate(d Draft) Problems {
	var p Problems
	add := func(format string, a ...any) { p.Errors = append(p.Errors, fmt.Sprintf(format, a...)) }

	title := strings.TrimSpace(d.Title)
	switch n := utf8.RuneCountInString(title); {
	case n < TitleMinChars:
		add("title is %d characters; it needs at least %d", n, TitleMinChars)
	case n > TitleMaxChars:
		add("title is %d characters; the limit is %d", n, TitleMaxChars)
	}
	if n := len(strings.Fields(title)); title != "" && n < TitleMinWords {
		add("title is %d words; it needs at least %d", n, TitleMinWords)
	}

	if n := Words(d.Content); n < ContentMinWords || n > ContentMaxWords {
		add("content is %d words; it must be %d to %d", n, ContentMinWords, ContentMaxWords)
	}
	if n := len(linkRE.FindAllStringIndex(d.Content, -1)); n > MaxLinks {
		add("content has %d links; the limit is %d (YouTube and Google Maps embeds do not count)", n, MaxLinks)
	}
	if n := len(imageRE.FindAllStringIndex(d.Content, -1)); n > MaxImages {
		add("content has %d inline images; the limit is %d", n, MaxImages)
	}

	switch n := len(nonEmpty(d.Categories)); {
	case n < MinCategories:
		add("at least %d category is required (limelit prnow categories lists them)", MinCategories)
	case n > MaxCategories:
		add("%d categories given; the limit is %d", n, MaxCategories)
	}
	if strings.TrimSpace(d.Country) == "" {
		add("country is required, a name or a two-letter code")
	}
	if strings.TrimSpace(d.City) == "" {
		add("city is required")
	}

	for _, f := range []struct {
		name  string
		value string
		max   int
	}{
		{"meta title", d.MetaTitle, MetaTitleMax},
		{"meta description", d.MetaDescription, MetaDescriptionMax},
		{"meta keywords", d.MetaKeywords, MetaKeywordsMax},
	} {
		if n := utf8.RuneCountInString(f.value); n > f.max {
			add("%s is %d characters; the limit is %d", f.name, n, f.max)
		}
	}

	if n := len(strings.Fields(d.Summary)); n > SummaryMaxWords {
		p.Warnings = append(p.Warnings, fmt.Sprintf("summary is %d words; PRNow cuts it to %d", n, SummaryMaxWords))
	}
	return p
}

// Words counts the words a reader sees: tags removed, entities decoded.
func Words(content string) int {
	return len(strings.Fields(html.UnescapeString(tagRE.ReplaceAllString(content, " "))))
}

// HTMLFromText turns plain text into the HTML PRNow takes: each block
// separated by a blank line becomes an escaped paragraph. Text that already
// has tags is returned as it is.
func HTMLFromText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if htmlRE.MatchString(text) {
		return strings.TrimSpace(text)
	}
	var b strings.Builder
	for _, block := range strings.Split(text, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("<p>")
		b.WriteString(html.EscapeString(strings.Join(strings.Fields(block), " ")))
		b.WriteString("</p>")
	}
	return b.String()
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
