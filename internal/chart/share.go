// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package chart

// Which brands a chart names, and in which colour.

import (
	"fmt"

	"github.com/limelit-co/open/internal/metrics"
)

// NamedRivals is how many rivals a chart names beside the property. Twenty
// lines is spaghetti; seven reads. Everyone past the cut is drawn in the
// field grey, and the chart says how many it folded.
const NamedRivals = 6

// Palette is one vocabulary of colour: the property in the brand colour, the
// leading rivals in standings order each in their own hue, everyone else in
// the field grey. Hues are assigned alphabetically among the named rivals
// (see SeriesClass), so a brand is one colour on every chart built from the
// same standings.
type Palette struct {
	named  map[string]bool
	rivals []string
}

// NewPalette names the property and the first NamedRivals rivals of the
// standings, which arrive most mentioned first.
func NewPalette(standings []metrics.BrandStanding) Palette {
	p := Palette{named: map[string]bool{}}
	for _, b := range standings {
		if b.IsOwn {
			p.named[b.Name] = true
			continue
		}
		if len(p.rivals) < NamedRivals {
			p.named[b.Name] = true
			p.rivals = append(p.rivals, b.Name)
		}
	}
	return p
}

// Named reports whether a brand is drawn as itself rather than folded.
func (p Palette) Named(name string, isOwn bool) bool { return isOwn || p.named[name] }

// Class is the brand's colour class.
func (p Palette) Class(name string, isOwn bool) string {
	switch {
	case isOwn:
		return "s-own"
	case !p.named[name]:
		return "s-field"
	}
	return SeriesClass(name, p.rivals, false)
}

// ShareSlices cuts share of voice into the donut's slices: the property and
// the named rivals as themselves, every other brand with a share summed into
// one slice, because at 2*pi*80 units of ring a 0.3% share is a quarter of a
// unit long and draws as nothing while sitting in a legend as if it were
// there.
func ShareSlices(standings []metrics.BrandStanding, p Palette) ([]DonutSlice, DonutOwn) {
	var (
		slices []DonutSlice
		own    DonutOwn
		rest   DonutSlice
		folded string
	)
	for _, b := range standings {
		own.Total += b.Mentions
		if b.IsOwn {
			own.Share, own.Mentions, own.HasData = b.ShareOfVoice, b.Mentions, b.Mentions > 0
		}
		if p.Named(b.Name, b.IsOwn) {
			if b.ShareOfVoice > 0 {
				slices = append(slices, DonutSlice{Name: b.Name, IsOwn: b.IsOwn, Class: p.Class(b.Name, b.IsOwn), Share: b.ShareOfVoice})
			}
			continue
		}
		if b.ShareOfVoice > 0 {
			rest.Share += b.ShareOfVoice
			rest.Count++
			folded = b.Name
		}
	}
	if rest.Count > 0 {
		rest.Name = fmt.Sprintf("%d other brands", rest.Count)
		if rest.Count == 1 {
			rest.Name = folded
		}
		rest.Class, rest.Rest = "s-rest", true
		slices = append(slices, rest)
	}
	return slices, own
}
