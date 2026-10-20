// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

// The charts live in internal/chart, where the MCP server's chart tools draw
// with the same renderers the dashboard does. These names keep the screens
// in this package reading as they did.

import (
	"github.com/limelit-co/open/internal/chart"
	"github.com/limelit-co/open/internal/metrics"
)

type (
	TrendPoint  = chart.TrendPoint
	RaceSeries  = chart.RaceSeries
	DonutSlice  = chart.DonutSlice
	DonutOwn    = chart.DonutOwn
	EngineGroup = chart.EngineGroup
	EngineBar   = chart.EngineBar
	MixDay      = chart.MixDay
	CloudWord   = chart.CloudWord
)

// CellMinN is the sample below which a grid cell cannot reach the top of the
// heat ramp.
const CellMinN = chart.CellMinN

// thinN is the sample under which a point counts as thin: the charts draw it
// hollow and the copy says so, from the one threshold.
const thinN = metrics.LowNThreshold

var (
	TrendChart       = chart.TrendChart
	Sparkline        = chart.Sparkline
	SparkArea        = chart.SparkArea
	RaceChart        = chart.RaceChart
	Donut            = chart.Donut
	EngineBars       = chart.EngineBars
	CitationMixChart = chart.CitationMixChart
	WordCloud        = chart.WordCloud

	cellBin     = chart.CellBin
	seriesClass = chart.SeriesClass
	mixOrder    = chart.MixOrder
)

func pct(v float64) string     { return chart.Pct(v) }
func thousands(n int) string   { return chart.Thousands(n) }
func answersWord(n int) string { return chart.AnswersWord(n) }
