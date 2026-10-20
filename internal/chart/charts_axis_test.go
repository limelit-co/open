// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package chart

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestDateScaleSpacesDaysByCalendarTime. A daily week followed by weekly
// runs must not draw the two at the same width, and every long gap is a
// bridge so nothing is curved across it.
func TestDateScaleSpacesDaysByCalendarTime(t *testing.T) {
	days := []string{"2026-07-13", "2026-07-14", "2026-07-15", "2026-07-16", "2026-07-17", "2026-07-18", "2026-07-19", "2026-07-22", "2026-07-29", "2026-08-06", "2026-08-15"}
	ax := dateScale(days, 0, 330)
	if !ax.dated {
		t.Fatal("ISO dates did not parse")
	}
	// 33 days across 330 units: ten units a day.
	if math.Abs(ax.perDay-10) > 0.01 {
		t.Errorf("perDay = %.2f, want 10", ax.perDay)
	}
	if math.Abs(ax.xs[1]-ax.xs[0]-10) > 0.01 || math.Abs(ax.xs[8]-ax.xs[7]-70) > 0.01 {
		t.Errorf("x is not proportional to time: %v", ax.xs)
	}
	// Gaps 1,1,1,1,1,1,3,7,8,9 with median 1: the last four are bridges.
	want := []bool{false, false, false, false, false, false, true, true, true, true}
	for i, b := range want {
		if ax.bridge[i] != b {
			t.Errorf("bridge[%d] = %v, want %v", i, ax.bridge[i], b)
		}
	}
	if runs := solidRuns(ax.bridge, len(days)); len(runs) != 5 || runs[0] != [2]int{0, 6} {
		t.Errorf("runs = %v, want a week then four single days", runs)
	}
}

// TestDateScaleTreatsAnEvenCadenceAsSolid. A weekly series has no gaps of
// its own kind, so it draws as one line.
func TestDateScaleTreatsAnEvenCadenceAsSolid(t *testing.T) {
	ax := dateScale([]string{"2026-07-01", "2026-07-08", "2026-07-15", "2026-07-22"}, 0, 300)
	for i, b := range ax.bridge {
		if b {
			t.Errorf("weekly gap %d marked as a bridge", i)
		}
	}
}

// TestDateScaleBridgesALongGapNextToAShortOne. Seeded history that ends,
// then two live daily runs a month later: gaps of 29 and 1. A mean of the
// two would call 29 days "typical" and curve across them.
func TestDateScaleBridgesALongGapNextToAShortOne(t *testing.T) {
	for _, days := range [][]string{
		{"2026-08-15", "2026-09-13", "2026-09-14"},
		{"2026-08-15", "2026-08-16", "2026-09-13"},
	} {
		ax := dateScale(days, 0, 300)
		long := 0
		if days[1] == "2026-08-16" {
			long = 1
		}
		if !ax.bridge[long] {
			t.Errorf("%v: the %d-day gap was not bridged", days, 28+long)
		}
		if ax.bridge[1-long] {
			t.Errorf("%v: the one-day gap was bridged", days)
		}
	}
}

// TestDateScaleFallsBackToIndexSpacing. Labels that are not dates get even
// spacing and no bridges rather than a broken axis.
func TestDateScaleFallsBackToIndexSpacing(t *testing.T) {
	ax := dateScale([]string{"a", "b", "c"}, 0, 100)
	if ax.dated {
		t.Error("non-dates reported as dated")
	}
	if ax.xs[0] != 0 || ax.xs[1] != 50 || ax.xs[2] != 100 {
		t.Errorf("xs = %v, want even spacing", ax.xs)
	}
	for _, b := range ax.bridge {
		if b {
			t.Error("a fallback axis has no bridges")
		}
	}
	one := dateScale([]string{"2026-07-01"}, 0, 100)
	if one.xs[0] != 50 {
		t.Errorf("a single day sits at %v, want the middle", one.xs[0])
	}
}

// TestMonotonePathNeverOvershoots. Between two neighbours the curve's
// control points stay inside their value range, so 57, 0, 66 cannot dip
// below zero or rise above 66 and read as a measurement.
func TestMonotonePathNeverOvershoots(t *testing.T) {
	xs := []float64{0, 10, 20, 30, 40}
	ys := []float64{57, 0, 66, 66, 10}
	d := monotonePath(xs, ys)
	if !strings.HasPrefix(d, "M0.0 57.0 C") {
		t.Fatalf("path does not start at the first point: %s", d)
	}
	re := regexp.MustCompile(`C([\d.-]+) ([\d.-]+) ([\d.-]+) ([\d.-]+) ([\d.-]+) ([\d.-]+)`)
	segs := re.FindAllStringSubmatch(d, -1)
	if len(segs) != len(xs)-1 {
		t.Fatalf("%d segments for %d points", len(segs), len(xs))
	}
	for i, m := range segs {
		lo, hi := math.Min(ys[i], ys[i+1]), math.Max(ys[i], ys[i+1])
		for _, k := range []int{2, 4} {
			cy, _ := strconv.ParseFloat(m[k], 64)
			if cy < lo-0.05 || cy > hi+0.05 {
				t.Errorf("segment %d control y %.1f outside [%.1f, %.1f]", i, cy, lo, hi)
			}
		}
	}
	if two := monotonePath([]float64{0, 1}, []float64{5, 6}); !strings.Contains(two, " L") {
		t.Errorf("two points should be a straight segment: %s", two)
	}
}

// TestSpreadLabelsKeepsTheGapAndTheBounds.
func TestSpreadLabelsKeepsTheGapAndTheBounds(t *testing.T) {
	got := spreadLabels([]float64{100, 102, 104, 300, 358}, 14, 24, 360)
	sorted := append([]float64(nil), got...)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i] > sorted[j] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	for i := 1; i < len(sorted); i++ {
		if sorted[i]-sorted[i-1] < 14-0.01 {
			t.Errorf("labels %v closer than the gap", got)
		}
	}
	for _, y := range got {
		if y < 24 || y > 360 {
			t.Errorf("label at %.1f is outside the plot", y)
		}
	}
	// Input order is preserved: the lowest input is still the lowest output.
	if got[0] > got[3] {
		t.Errorf("order was not preserved: %v", got)
	}
}

// TestRaceNeverCurvesAcrossAGap. A stretch nobody measured is a shaded band
// with a dashed straight bridge, never a curve, and never a dot.
func TestRaceNeverCurvesAcrossAGap(t *testing.T) {
	pts := []TrendPoint{}
	for _, d := range []string{"2026-07-13", "2026-07-14", "2026-07-15", "2026-07-16", "2026-07-29"} {
		pts = append(pts, TrendPoint{Day: d, Value: 50, N: 30})
	}
	out := string(RaceChart([]RaceSeries{{Name: "Acme", IsOwn: true, Class: "s-own", Points: pts, Last: 50}}))
	if !strings.Contains(out, `class="gap-band"`) {
		t.Error("the unmeasured stretch has no band")
	}
	if !strings.Contains(out, `series-own line bridge"`) {
		t.Error("the gap has no bridge path")
	}
	if strings.Count(out, "<title>No run between 2026-07-16 and 2026-07-29</title>") != 1 {
		t.Error("the band does not say which days it spans")
	}
	// The solid own line is one run of four points: exactly three cubic
	// segments and nothing else.
	re := regexp.MustCompile(`class="series s-own series-own line" pathLength="1" d="([^"]+)"`)
	m := re.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no solid own line: %s", out)
	}
	if strings.Count(m[1], " C") != 3 || strings.Count(m[1], "M") != 1 {
		t.Errorf("solid line should be one run of three curves: %s", m[1])
	}
	if !strings.Contains(out, `<text class="axis axis-x"`) || !strings.Contains(out, ">Jul 13<") || !strings.Contains(out, ">Jul 29<") {
		t.Error("date labels are missing or not human")
	}
	if !strings.Contains(out, `class="n-bar"`) {
		t.Error("the answers strip is missing")
	}
}

// TestRaceMarksThinDaysHollow. A point resting on fewer than thinN answers
// is drawn hollow and says so in its title.
func TestRaceMarksThinDaysHollow(t *testing.T) {
	out := string(RaceChart([]RaceSeries{{Name: "Acme", IsOwn: true, Class: "s-own", Points: []TrendPoint{
		{Day: "2026-07-13", Value: 0, N: thinN - 1}, {Day: "2026-07-14", Value: 50, N: thinN},
	}}}))
	if !strings.Contains(out, "dot-thin") || !strings.Contains(out, "thin sample") {
		t.Error("a thin day is not marked")
	}
	if strings.Count(out, "dot-thin") != 1 {
		t.Errorf("thin marks = %d, want 1", strings.Count(out, "dot-thin"))
	}
	if !strings.Contains(out, "data-thin") {
		t.Error("the tick does not carry the thin flag for the hover")
	}
}

// TestRaceDrawsNoLeadLineFromALonePoint. On a one-day chart the point sits
// in the middle; a line from it to the label gutter would read as a trend.
func TestRaceDrawsNoLeadLineFromALonePoint(t *testing.T) {
	out := string(RaceChart([]RaceSeries{
		{Name: "Acme", IsOwn: true, Class: "s-own", Points: []TrendPoint{{Day: "2026-09-14", Value: 0, N: 30}}},
		{Name: "Rival", Class: "s-1", Points: []TrendPoint{{Day: "2026-09-14", Value: 1, N: 30}}, Last: 1},
	}))
	if strings.Contains(out, `class="lead `) {
		t.Error("a lone point grew a lead line")
	}
	if !strings.Contains(out, `class="end s-1"`) || !strings.Contains(out, `class="end s-own end-own"`) {
		t.Error("end labels are missing")
	}
}

func TestSparkAreaIsAbsentUnderThreePoints(t *testing.T) {
	if got := SparkArea([]TrendPoint{{Day: "2026-07-13", Value: 1}, {Day: "2026-07-14", Value: 2}}); got != "" {
		t.Errorf("two points drew a spark: %s", got)
	}
	out := string(SparkArea([]TrendPoint{{Day: "2026-07-13", Value: 10}, {Day: "2026-07-14", Value: 40}, {Day: "2026-07-15", Value: 25}}))
	for _, want := range []string{`class="kpi-spark"`, `class="spark-area"`, `class="spark-line"`} {
		if !strings.Contains(out, want) {
			t.Errorf("spark is missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "#") || strings.Contains(out, "url(") {
		t.Error("the spark emitted a colour or a reference from Go")
	}
}

// TestDonutGroupsTheTailAndKeepsTheCentreHonest.
func TestDonutGroupsTheTailAndKeepsTheCentreHonest(t *testing.T) {
	out := string(Donut([]DonutSlice{
		{Name: "Acme", IsOwn: true, Class: "s-own", Share: 29},
		{Name: "Rival", Class: "s-1", Share: 48},
		{Name: "15 other brands", Class: "s-rest", Share: 23, Rest: true, Count: 15},
	}, DonutOwn{Share: 29, Mentions: 4907, Total: 17140, HasData: true}))
	if !strings.Contains(out, "arc-rest") || !strings.Contains(out, "15 other brands: 23% of all tracked-brand mentions") {
		t.Error("the rest slice is not drawn or not named")
	}
	if !strings.Contains(out, "4,907 of 17,140") {
		t.Error("the centre does not state the counts the share rests on")
	}
	if strings.Count(out, `class="arc `) != 3 {
		t.Errorf("arcs = %d, want 3", strings.Count(out, `class="arc `))
	}
	// Own draws first so it starts at twelve o'clock.
	if strings.Index(out, "series-own") > strings.Index(out, `class="arc s-1"`) {
		t.Error("the own slice is not first")
	}
}

// TestEngineStripsOrderRowsByOwnVisibility. The order is the finding: your
// strongest engine is the first row.
func TestEngineStripsOrderRowsByOwnVisibility(t *testing.T) {
	out := string(EngineBars([]EngineGroup{
		{Engine: "chatgpt", Access: "api", Answers: 40, Bars: []EngineBar{{Name: "Acme", IsOwn: true, Class: "s-own", Visibility: 40, Mentions: 16}, {Name: "Rival", Class: "s-1", Visibility: 67, Mentions: 27}}},
		{Engine: "claude", Access: "api", Answers: 40, Bars: []EngineBar{{Name: "Acme", IsOwn: true, Class: "s-own", Visibility: 79, Mentions: 32}, {Name: "Rival", Class: "s-1", Visibility: 10, Mentions: 4}}},
		{Engine: "ai_overview", Access: "scraped", Answers: 11, Bars: []EngineBar{{Name: "Acme", IsOwn: true, Class: "s-own", Visibility: 0}, {Name: "Rival", Class: "s-1", Visibility: 100, Mentions: 11}}},
	}))
	if strings.Index(out, ">Claude<") > strings.Index(out, ">ChatGPT<") {
		t.Error("rows are not strongest first")
	}
	if !strings.Contains(out, "1st of 2") || !strings.Contains(out, "2nd, Rival 67%") {
		t.Errorf("rank column missing: %s", out)
	}
	if !strings.Contains(out, `class="lead-val s-1"`) || !strings.Contains(out, ">Rival 67%<") {
		t.Error("a rival ahead of you is not named above its dot")
	}
	if !strings.Contains(out, "row-thin") || !strings.Contains(out, "11 answers, thin") {
		t.Error("a thin row is not marked")
	}
	if !strings.Contains(out, "not named") {
		t.Error("a measured own zero does not say so")
	}
	if strings.Contains(out, "#") || strings.Contains(out, "rgb(") {
		t.Error("a colour was emitted from Go")
	}
}

func TestOrdinalAndThousands(t *testing.T) {
	for n, want := range map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 22: "22nd", 111: "111th"} {
		if got := ordinal(n); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1297: "1,297", 17140: "17,140", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := Thousands(n); got != want {
			t.Errorf("Thousands(%d) = %q, want %q", n, got, want)
		}
	}
}
