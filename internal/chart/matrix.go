// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package chart

import (
	"fmt"
	"html/template"
	"strings"
)

// GridCell is one prompt on one engine.
type GridCell struct {
	// Asked is false for a prompt never asked of this engine. NoAnswer is
	// an engine that was asked and showed no AI answer at all. Neither is a
	// zero, and each is drawn as what it is.
	Asked    bool
	NoAnswer bool
	Answers  int
	Mentions int
	// Visibility is Mentions over Answers, 0 to 100.
	Visibility float64
}

// GridRow is one prompt across every engine.
type GridRow struct {
	Prompt  string
	Branded bool
	Cells   []GridCell
}

// Grid geometry: a prompt column, then one column per engine.
const (
	gridLabelW = 252.0
	gridHeadH  = 44.0
	gridRowH   = 32.0
	gridGap    = 4.0
	gridMinCol = 76.0
	gridWidth  = 720.0
)

// PromptGrid draws the prompt by engine grid as SVG, for a frame about 700
// pixels wide: the dashboard's grid, heat-binned the same way (CellBin), with
// each cell printing its own fraction, because "0/1" is a census of one
// answer the reader can open and "0%" is an estimate from a single draw.
//
// columns are the engines' display names. A long name wraps to two lines.
func PromptGrid(columns []string, rows []GridRow) template.HTML {
	if len(columns) == 0 || len(rows) == 0 {
		return ""
	}
	colW := (gridWidth - gridLabelW) / float64(len(columns))
	if colW < gridMinCol {
		colW = gridMinCol
	}
	w := gridLabelW + colW*float64(len(columns))
	h := gridHeadH + gridRowH*float64(len(rows))

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart chart-grid" viewBox="0 0 %.0f %.0f" role="img" aria-label="%s">`,
		w, h, template.HTMLEscapeString(fmt.Sprintf("Visibility by prompt and engine: %d prompts on %d engines.", len(rows), len(columns))))

	for i, name := range columns {
		x := gridLabelW + colW*(float64(i)+0.5)
		lines := wrapLabel(name, int(colW/7.5))
		y := gridHeadH - 10 - 14*float64(len(lines)-1)
		fmt.Fprintf(&b, `<text class="grid-col" x="%.1f" y="%.1f" text-anchor="middle"><title>%s</title>`, x, y, template.HTMLEscapeString(name))
		for k, line := range lines {
			dy := 0.0
			if k > 0 {
				dy = 14
			}
			fmt.Fprintf(&b, `<tspan x="%.1f" dy="%.0f">%s</tspan>`, x, dy, template.HTMLEscapeString(line))
		}
		b.WriteString(`</text>`)
	}

	for r, row := range rows {
		y := gridHeadH + gridRowH*float64(r)
		cy := y + gridRowH/2
		cls := "grid-prompt"
		if row.Branded {
			cls += " grid-branded"
		}
		label := truncate(row.Prompt, 38)
		fmt.Fprintf(&b, `<text class="%s" x="%.0f" y="%.1f" text-anchor="end" dominant-baseline="middle"><title>%s</title>%s</text>`,
			cls, gridLabelW-12, cy, template.HTMLEscapeString(promptTitle(row)), template.HTMLEscapeString(label))
		for i, c := range row.Cells {
			if i >= len(columns) {
				break
			}
			x := gridLabelW + colW*float64(i) + gridGap/2
			cw, ch := colW-gridGap, gridRowH-gridGap
			var bin, text, tip string
			switch {
			case !c.Asked:
				bin, tip = "hm-none", columns[i]+": not asked yet"
			case c.NoAnswer:
				bin, text = "hm-none cell-noanswer", "no AI answer"
				tip = columns[i] + ": asked, and it showed no AI answer; not counted against you"
			default:
				bin = CellBin(c.Answers, c.Visibility)
				text = fmt.Sprintf("%d/%d", c.Mentions, c.Answers)
				tip = fmt.Sprintf("%s: named in %d of %s", columns[i], c.Mentions, AnswersWord(c.Answers))
			}
			fmt.Fprintf(&b, `<g class="cell %s"><title>%s</title><rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="5"/>`,
				bin, template.HTMLEscapeString(tip), x, y+gridGap/2, cw, ch)
			if text != "" {
				fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="middle" dominant-baseline="middle">%s</text>`, x+cw/2, cy, template.HTMLEscapeString(text))
			}
			b.WriteString(`</g>`)
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func promptTitle(row GridRow) string {
	if row.Branded {
		return row.Prompt + " (names your brand; left out of the headline)"
	}
	return row.Prompt
}

// wrapLabel splits a column name into at most two lines of about max runes,
// at a space, cutting the second line if it is still too long.
func wrapLabel(s string, max int) []string {
	if max < 4 || len([]rune(s)) <= max {
		return []string{s}
	}
	words := strings.Fields(s)
	first := ""
	i := 0
	for ; i < len(words); i++ {
		next := strings.TrimSpace(first + " " + words[i])
		if first != "" && len([]rune(next)) > max {
			break
		}
		first = next
	}
	rest := strings.Join(words[i:], " ")
	if rest == "" {
		return []string{truncate(first, max)}
	}
	return []string{truncate(first, max), truncate(rest, max)}
}
