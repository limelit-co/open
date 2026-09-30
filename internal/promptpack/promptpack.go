// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package promptpack fills a starter set of tracked prompts from what the
// setup wizard already knows: the brand, its category phrase and its
// competitors.
//
// The pack is a fixed template list, not a model call. That is a deliberate
// line: generating prompts from a model is a hosted feature, and it would
// also mean the first thing a new instance did was spend money before the
// user had seen a single number.
//
// The templates are the questions a buyer actually types. Each one is a
// different shape of intent, because a set of twelve rewordings of "best X"
// measures one thing twelve times.
package promptpack

import (
	"strings"
)

// Input is what the wizard collected.
type Input struct {
	// Brand is the property name, for the head-to-head and branded prompts.
	Brand string
	// Category is the phrase a buyer would use, for example "AI visibility
	// tracking", "CRM for startups" or "converting Kindle books to PDF".
	// phrase fits it to the templates.
	Category string
	// Competitors are display names, used for alternatives and head-to-head.
	Competitors []string
}

// Prompt is one generated prompt.
type Prompt struct {
	Text     string
	Category string
	// Branded marks a prompt that names the property. It is kept, because
	// what an engine says about you when asked directly is worth reading,
	// but it is excluded from the headline visibility number.
	Branded bool
}

// Categories used for the generated prompts, so the grid can be grouped
// before anyone has written a tag.
const (
	CategoryDiscovery  = "discovery"
	CategoryComparison = "comparison"
	CategoryUseCase    = "use case"
	CategoryBrand      = "brand"
)

// Build returns the starter prompts. The result is deterministic for the
// same input, so a user who reruns the wizard sees the same list rather than
// a reshuffled one.
//
// Whether a prompt counts as branded is decided by package mentions, not
// here. The Branded field below is what the templates know they wrote; a
// prompt a user types by hand goes through the same matcher that decides
// whether an answer mentions the brand, so one rule governs both.
func Build(in Input) []Prompt {
	category := strings.TrimSpace(in.Category)
	brand := strings.TrimSpace(in.Brand)
	if category == "" {
		return nil
	}

	var out []Prompt
	add := func(text, cat string, branded bool) {
		text = strings.Join(strings.Fields(text), " ")
		if text != "" {
			out = append(out, Prompt{Text: text, Category: cat, Branded: branded})
		}
	}

	// Discovery: the question with no brand in it at all. This is where
	// visibility is won or lost, and it is the only shape that measures
	// whether an engine reaches for you unprompted.
	ph := phrase(category)
	add("What are the best "+ph.tools+"?", CategoryDiscovery, false)
	add("Which "+ph.tool+" should I use?", CategoryDiscovery, false)
	add("What is the best "+ph.tool+" for a small team?", CategoryDiscovery, false)
	add("Which "+ph.tools+" are worth paying for?", CategoryDiscovery, false)
	add("What are the top "+ph.tools+" right now?", CategoryDiscovery, false)

	// Use case: how a buyer actually phrases the problem, rather than the
	// category label a vendor uses.
	add("How do I choose "+ph.aTool+"?", CategoryUseCase, false)
	add("What should I look for in "+ph.aTool+"?", CategoryUseCase, false)
	add("Is there a free or open source "+ph.tool+"?", CategoryUseCase, false)

	// Comparison: one per competitor, capped so the pack stays a starter set
	// rather than a bill.
	competitors := trimAll(in.Competitors)
	for i, c := range competitors {
		if i >= 3 {
			break
		}
		add(c+" alternatives", CategoryComparison, false)
	}
	if brand != "" && len(competitors) > 0 {
		add(brand+" vs "+competitors[0], CategoryComparison, true)
	}

	// Brand: what an engine says when asked directly. Branded, so it is
	// visible in the grid and absent from the headline.
	if brand != "" {
		add("What is "+brand+"?", CategoryBrand, true)
		add("Is "+brand+" any good?", CategoryBrand, true)
	}

	return out
}

// toolPhrase is a category written into the templates' three slots.
type toolPhrase struct {
	tool, tools, aTool string
}

// phrase fits a category phrase to "the best ___ tools", whatever shape the
// user typed it in, because a prompt like "What are the best Converting
// kindle books to pdf tools?" tells a reader, and an engine, that a template
// wrote it:
//
//   - A trailing "tool", "tools", "software", "app" or "apps" is dropped, so
//     "PDF converter tool" does not become "PDF converter tool tools".
//   - A phrase that starts with a verb ("Converting kindle books to pdf",
//     "tracking AI visibility") is what the tool is for: "tools for
//     converting kindle books to pdf", with the verb lowercased.
//   - A phrase with "for" in it ("CRM for startups") puts "tools" after the
//     thing, before the audience: "CRM tools for startups".
//   - Anything else is a noun: "AI visibility tracking tools".
func phrase(category string) toolPhrase {
	words := strings.Fields(category)
	for len(words) > 1 {
		switch strings.ToLower(words[len(words)-1]) {
		case "tool", "tools", "software", "app", "apps":
			words = words[:len(words)-1]
			continue
		}
		break
	}
	if len(words) == 0 {
		return toolPhrase{tool: "tool", tools: "tools", aTool: "a tool"}
	}
	first := words[0]
	// "Convert Kindle books to PDF": a task, so the tool is one to do it.
	if taskVerbs[strings.ToLower(first)] && len(words) > 1 {
		words[0] = strings.ToLower(first)
		rest := strings.Join(words, " ")
		return toolPhrase{tool: "tool to " + rest, tools: "tools to " + rest, aTool: "a tool to " + rest}
	}
	if lower := strings.ToLower(first); len(lower) > 4 && strings.HasSuffix(lower, "ing") && first[1:] == lower[1:] {
		words[0] = lower
		rest := strings.Join(words, " ")
		return toolPhrase{tool: "tool for " + rest, tools: "tools for " + rest, aTool: "a tool for " + rest}
	}
	joined := strings.Join(words, " ")
	if i := strings.Index(joined, " for "); i > 0 {
		head, audience := joined[:i], joined[i:]
		return toolPhrase{
			tool: head + " tool" + audience, tools: head + " tools" + audience,
			aTool: indefiniteArticle(head) + " " + head + " tool" + audience,
		}
	}
	return toolPhrase{tool: joined + " tool", tools: joined + " tools", aTool: indefiniteArticle(joined) + " " + joined + " tool"}
}

// taskVerbs start a category typed as a task ("Convert Kindle books to PDF",
// "Track brand mentions"). A list, not a grammar: these are the verbs people
// use to name what a tool does.
var taskVerbs = map[string]bool{
	"convert": true, "export": true, "import": true, "track": true, "monitor": true,
	"manage": true, "measure": true, "analyze": true, "analyse": true, "automate": true,
	"build": true, "create": true, "make": true, "design": true, "edit": true, "write": true,
	"generate": true, "translate": true, "transcribe": true, "record": true, "schedule": true,
	"send": true, "share": true, "sync": true, "backup": true, "back": true, "save": true,
	"remove": true, "compress": true, "merge": true, "split": true, "sign": true, "scan": true,
	"find": true, "compare": true, "download": true, "upload": true, "host": true, "deploy": true,
	"test": true, "check": true, "optimize": true, "optimise": true, "organize": true,
	"organise": true, "plan": true, "book": true, "learn": true, "hire": true, "sell": true,
	"invoice": true, "collect": true, "print": true, "read": true, "store": true, "search": true,
}

// indefiniteArticle picks "a" or "an" for a category phrase. A generated
// prompt that reads "a AI visibility tool" tells a user the list was written
// by a template, which is exactly the impression the first screen should not
// leave.
//
// The rule is the written-vowel one, with the two exceptions that break it in
// this vocabulary: "a UX tool" and "a European..." are both correct because
// they start with a consonant SOUND.
func indefiniteArticle(phrase string) string {
	phrase = strings.TrimSpace(phrase)
	if phrase == "" {
		return "a"
	}
	lower := strings.ToLower(phrase)
	if strings.HasPrefix(lower, "u") || strings.HasPrefix(lower, "eu") {
		return "a"
	}
	switch lower[0] {
	case 'a', 'e', 'i', 'o':
		return "an"
	}
	return "a"
}

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}
