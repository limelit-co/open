// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mentions

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// A brand whose name is also a description of what it does.
//
// "Kindle to PDF" (kindletopdf.com) is a product, and it is also what every
// answer about the category says: "how to convert Kindle to PDF", "BitRecover
// Kindle to PDF Converter", "any Kindle to PDF tool". A whole-word search for
// the name counts all of those as the brand. Measured on one brand's 200
// answers, it counted 122 where 44 named the product.
//
// So a name of that shape counts only where it reads as a name. The shape is
// narrow on purpose: more than one word, at least one of them a connecting
// word ("to", "for", "and", ...), and the words run together spell the
// domain's first label ("kindle to pdf", kindletopdf.com). "Athena HQ" or
// "SE Ranking" spell their domains too, but no connecting word makes them a
// phrase, and they are matched exactly as before. The domain and its label
// are never affected: an answer that says kindletopdf or kindletopdf.com
// names the brand wherever it says it.
//
// testdata/phrase_names.json holds the cases that decide this. Limelit
// Cloud's matcher reads the same file, byte for byte, so the two agree.

// connectingWords make a multi-word name read as a phrase.
var connectingWords = map[string]bool{
	"to": true, "for": true, "and": true, "or": true, "of": true, "in": true,
	"on": true, "with": true, "into": true, "from": true, "by": true,
	"the": true, "a": true, "an": true, "vs": true, "versus": true, "&": true,
}

// PhraseName reports whether name is a brand name that is also a
// description, so it counts only where it reads as a name.
func PhraseName(name, domainLabel string) bool {
	words := strings.Fields(strings.ToLower(name))
	if len(words) < 2 || domainLabel == "" {
		return false
	}
	connected := false
	for _, w := range words {
		if connectingWords[w] {
			connected = true
			break
		}
	}
	return connected && NormalizeKey(name) == NormalizeKey(domainLabel)
}

// DomainLabel is the first label of a domain without its "www.":
// "kindletopdf" for www.kindletopdf.com.
func DomainLabel(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	if i := strings.Index(d, "://"); i >= 0 {
		d = d[i+3:]
	}
	if i := strings.IndexByte(d, '/'); i >= 0 {
		d = d[:i]
	}
	d = strings.TrimPrefix(d, "www.")
	if i := strings.IndexByte(d, '.'); i >= 0 {
		return d[:i]
	}
	return d
}

// taskWordsBefore make the name the object of the task: "convert Kindle to
// PDF", "How to Convert Kindle to PDF", "export kindle to PDF".
var taskWordsBefore = map[string]bool{
	"convert": true, "converts": true, "converting": true, "converted": true,
	"export": true, "exports": true, "exporting": true, "exported": true,
	"save": true, "saves": true, "saving": true,
	"print": true, "prints": true, "printing": true,
	"transfer": true, "transferring": true,
	"turn": true, "turning": true, "transform": true, "transforming": true,
	"change": true, "changing": true,
}

// genericNounsAfter make the name a kind of thing: "a Kindle to PDF
// converter", "Kindle to PDF conversion", "any Kindle to PDF tool".
var genericNounsAfter = map[string]bool{
	"converter": true, "converters": true, "conversion": true, "conversions": true,
	"tool": true, "tools": true, "software": true, "program": true, "programs": true,
	"utility": true, "utilities": true, "method": true, "methods": true,
	"guide": true, "guides": true, "tutorial": true, "tutorials": true,
	"process": true, "workflow": true, "workflows": true, "solution": true, "solutions": true,
}

// openingWords are capitalized only because they start a sentence, so one of
// them right before the name does not make the name part of another name.
var openingWords = map[string]bool{
	"use": true, "using": true, "try": true, "with": true, "get": true, "choose": true,
	"pick": true, "install": true, "open": true, "visit": true, "download": true,
	"add": true, "consider": true, "and": true, "or": true, "but": true, "also": true,
	"then": true, "like": true, "for": true, "via": true, "see": true, "meet": true,
	"the": true, "a": true, "an": true, "is": true, "it": true, "its": true,
	"this": true, "that": true, "our": true, "your": true, "their": true,
	"if": true, "when": true, "while": true, "so": true, "yes": true, "no": true,
	"note": true, "tip": true, "best": true, "top": true, "recommended": true,
	"alternatively": true, "i": true, "we": true, "you": true,
}

// standsAsName reports whether text[start:end], an occurrence of a phrase
// name, reads as the name. It rejects a task word right before it, a generic
// noun right after it, and a capitalized word glued right before it that is
// not a sentence opener ("BitRecover Kindle to PDF Converter"). The last
// rule needs the original case; when cased is false it is skipped.
func standsAsName(text string, start, end int, cased bool) bool {
	before := wordBefore(text, start)
	if taskWordsBefore[strings.ToLower(before)] {
		return false
	}
	if genericNounsAfter[strings.ToLower(wordAfter(text, end))] {
		return false
	}
	if cased && before != "" && hasUpper(before) && !openingWords[strings.ToLower(before)] {
		return false
	}
	return true
}

// wordBefore is the word separated from offset at by spaces or tabs only, or
// "" when anything else (a newline, punctuation, markup) comes first.
func wordBefore(text string, at int) string {
	i := at
	for i > 0 && (text[i-1] == ' ' || text[i-1] == '\t') {
		i--
	}
	if i == at {
		return ""
	}
	end := i
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:i])
		if !isWordRune(r) && r != '\'' && r != '’' {
			break
		}
		i -= size
	}
	return text[i:end]
}

// wordAfter is the word separated from offset at by spaces or tabs only.
func wordAfter(text string, at int) string {
	i := at
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	if i == at {
		return ""
	}
	start := i
	for i < len(text) {
		r, size := utf8.DecodeRuneInString(text[i:])
		if !isWordRune(r) {
			break
		}
		i += size
	}
	return text[start:i]
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func hasUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}
