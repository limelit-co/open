// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package chart

// How a figure is written. One home, shared by the dashboard's copy and the
// labels inside a chart, so the donut's centre and the card beside it never
// format the same number two ways.

import (
	"fmt"
	"strings"

	"github.com/limelit-co/open/internal/engines"
)

// Pct formats a percentage the way every surface here formats it: no decimal
// unless the number is small enough that the decimal is the difference
// between "nothing" and "something".
func Pct(v float64) string {
	if v > 0 && v < 10 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0")
	}
	return fmt.Sprintf("%.0f", v)
}

// Thousands writes an integer with separators: 1,297 not 1297. fmt has no
// verb for it.
func Thousands(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// AnswersWord keeps the denominator readable at n=1.
func AnswersWord(n int) string {
	if n == 1 {
		return "1 answer"
	}
	return fmt.Sprintf("%d answers", n)
}

// engineLabel is the engine's display name. An engine is named for what it
// is, never for how it was reached.
func engineLabel(id string) string {
	if e, ok := engines.Lookup(id); ok {
		return e.Label
	}
	return id
}
