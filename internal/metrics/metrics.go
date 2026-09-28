// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package metrics aggregates stored answers into the numbers the dashboard,
// the MCP tools and the API all show.
//
// One package, because a number that differs between two surfaces is worse
// than a number that is missing from both. Everything here is a query over
// rows the runner wrote, so any figure can be re-derived from the answers and
// checked by hand.
//
// Two denominator rules run through all of it, and they are where visibility
// metrics usually go wrong:
//
//   - A chat whose status is no_answer_surface is excluded everywhere. The
//     surface did not render, which says nothing about the brand; counting it
//     as an answer that ignored you would drag every number toward zero for
//     reasons that have nothing to do with you.
//   - Branded prompts are excluded from the headline. Asking an engine about
//     yourself and counting the answer measures the question, not the market.
//     They stay visible everywhere else, tagged, because what an engine says
//     when asked directly is worth reading.
package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"

	"github.com/limelit-co/open/internal/store"
)

// LowNThreshold is where a number stops being a trend and starts being an
// anecdote. Below it every result says so, so nobody presents four answers as
// a market position.
const LowNThreshold = 20

// Service reads metrics.
type Service struct{ db *store.DB }

// New builds the service.
func New(db *store.DB) *Service { return &Service{db: db} }

// Window narrows a query in time. Days of zero means every answer ever.
type Window struct {
	Days int
	// IncludeBranded puts prompts that name the property back in. The grid
	// uses it; the headline never does.
	IncludeBranded bool
}

// where builds the shared filter every query starts from.
func (w Window) where(alias string) (string, []any) {
	clauses := []string{alias + ".status = 'ok'"}
	var args []any
	if w.Days > 0 {
		clauses = append(clauses, fmt.Sprintf("%s.created_at >= datetime('now', ?)", alias))
		args = append(args, fmt.Sprintf("-%d days", w.Days))
	}
	if !w.IncludeBranded {
		clauses = append(clauses, "p.branded = 0")
	}
	return strings.Join(clauses, " AND "), args
}

// Overview is the headline.
type Overview struct {
	// Visibility is the share of answers that mention the property.
	Visibility float64
	// CitationShare is the share of cited sources that are the property's.
	CitationShare float64
	// ShareOfVoice is the property's mentions over every tracked brand's.
	ShareOfVoice float64
	// MeanPosition is the property's average rank where it appears in a
	// ranked list, zero when it never does.
	MeanPosition float64
	// Answers is the denominator: how many answers these rest on.
	Answers int
	// AnswersWithMentions counts the answers naming the property.
	AnswersWithMentions int
	// LowN is true under LowNThreshold answers. It is a caption, not a
	// warning: a new instance is legitimately here for its first day.
	LowN bool
	// Prompts and Competitors are the tracked counts.
	Prompts     int
	Competitors int
	// Categories breaks visibility down by prompt shape. Discovery is the
	// only shape that measures whether an engine reaches for you unprompted,
	// so a low headline with healthy discovery means something different
	// from the reverse.
	Categories []CategoryVisibility
}

// CategoryVisibility is visibility within one prompt category.
type CategoryVisibility struct {
	Category   string
	Visibility float64
	Answers    int
	Mentions   int
}

// BrandStanding is one brand's position in the ranking.
type BrandStanding struct {
	// CompetitorID is nil for the property itself.
	CompetitorID *int64
	Name         string
	IsOwn        bool
	// Answers is how many answers mention this brand.
	Answers int
	// Mentions is every occurrence, which can exceed Answers.
	Mentions int
	// Visibility is Answers over the window's answer count.
	Visibility float64
	// ShareOfVoice is this brand's mentions over every tracked brand's.
	ShareOfVoice float64
	// MeanPosition is zero when the brand never appears in a ranked list.
	MeanPosition float64
}

// SourceRow is one cited site.
type SourceRow struct {
	Site       string
	SourceType string
	Citations  int
	Answers    int
}

// DayPoint is one day in a series.
type DayPoint struct {
	Day        string
	Visibility float64
	Answers    int
	Mentions   int
}

// Overview returns the headline numbers.
func (s *Service) Overview(ctx context.Context, w Window) (Overview, error) {
	var out Overview
	filter, args := w.where("c")

	// The denominator first. Everything else divides by it, so computing it
	// once stops two figures on one screen disagreeing.
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM chat c JOIN prompt p ON p.id = c.prompt_id WHERE `+filter, args...).Scan(&out.Answers)
	if err != nil {
		return out, err
	}
	out.LowN = out.Answers < LowNThreshold

	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT c.id) FROM chat c
		JOIN prompt p ON p.id = c.prompt_id
		JOIN mention m ON m.chat_id = c.id AND m.competitor_id IS NULL
		WHERE `+filter, args...).Scan(&out.AnswersWithMentions); err != nil {
		return out, err
	}
	out.Visibility = ratio(out.AnswersWithMentions, out.Answers)

	var ownMentions, allMentions int
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN m.competitor_id IS NULL THEN 1 ELSE 0 END), 0),
			COUNT(*)
		FROM mention m
		JOIN chat c ON c.id = m.chat_id
		JOIN prompt p ON p.id = c.prompt_id
		WHERE `+filter, args...).Scan(&ownMentions, &allMentions); err != nil {
		return out, err
	}
	out.ShareOfVoice = ratio(ownMentions, allMentions)

	var ownCites, allCites int
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN ct.source_type = 'own' THEN 1 ELSE 0 END), 0),
			COUNT(*)
		FROM citation ct
		JOIN chat c ON c.id = ct.chat_id
		JOIN prompt p ON p.id = c.prompt_id
		WHERE `+filter, args...).Scan(&ownCites, &allCites); err != nil {
		return out, err
	}
	out.CitationShare = ratio(ownCites, allCites)

	var meanPos sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `
		SELECT AVG(m.list_rank) FROM mention m
		JOIN chat c ON c.id = m.chat_id
		JOIN prompt p ON p.id = c.prompt_id
		WHERE m.competitor_id IS NULL AND m.list_rank IS NOT NULL AND `+filter, args...).Scan(&meanPos); err != nil {
		return out, err
	}
	if meanPos.Valid {
		out.MeanPosition = round2(meanPos.Float64)
	}

	counts, err := s.db.Counts(ctx)
	if err != nil {
		return out, err
	}
	out.Prompts, out.Competitors = counts.Prompts, counts.Competitors

	if out.Categories, err = s.categories(ctx, w); err != nil {
		return out, err
	}
	return out, nil
}

// Joining mention onto chat fans one answer out into one row per mention, so
// every count of answers across that join is COUNT(DISTINCT c.id). The
// property has several name variants, so "Acme (acme.com) leads" is two own
// mentions in one answer; a plain COUNT(*) reports it as two answers of which
// one named us, which is 50% visibility on an answer that named us. The same
// applies in Series and Matrix below.
//
// categories breaks visibility down by the prompt's category.
func (s *Service) categories(ctx context.Context, w Window) ([]CategoryVisibility, error) {
	filter, args := w.where("c")
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			CASE WHEN p.category = '' THEN 'uncategorised' ELSE p.category END AS category,
			COUNT(DISTINCT c.id),
			COUNT(DISTINCT CASE WHEN m.id IS NOT NULL THEN c.id END)
		FROM chat c
		JOIN prompt p ON p.id = c.prompt_id
		LEFT JOIN mention m ON m.chat_id = c.id AND m.competitor_id IS NULL
		WHERE `+filter+`
		GROUP BY category
		ORDER BY COUNT(DISTINCT c.id) DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CategoryVisibility
	for rows.Next() {
		var c CategoryVisibility
		if err := rows.Scan(&c.Category, &c.Answers, &c.Mentions); err != nil {
			return nil, err
		}
		c.Visibility = ratio(c.Mentions, c.Answers)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Standings ranks the property against every tracked competitor.
//
// The property is always present, even at zero, because "you are not in this
// conversation" is the single most useful thing this tool can tell someone
// and hiding an empty row would bury it.
func (s *Service) Standings(ctx context.Context, w Window) ([]BrandStanding, error) {
	filter, args := w.where("c")

	var answers, allMentions int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM chat c JOIN prompt p ON p.id = c.prompt_id WHERE `+filter, args...).Scan(&answers); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM mention m
		JOIN chat c ON c.id = m.chat_id
		JOIN prompt p ON p.id = c.prompt_id
		WHERE `+filter, args...).Scan(&allMentions); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT m.competitor_id, m.brand_name,
		       COUNT(DISTINCT c.id), COUNT(*), AVG(m.list_rank)
		FROM mention m
		JOIN chat c ON c.id = m.chat_id
		JOIN prompt p ON p.id = c.prompt_id
		WHERE `+filter+`
		GROUP BY m.competitor_id, m.brand_name
		ORDER BY COUNT(*) DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var (
		out    []BrandStanding
		sawOwn bool
	)
	for rows.Next() {
		var (
			b       BrandStanding
			compID  sql.NullInt64
			meanPos sql.NullFloat64
		)
		if err := rows.Scan(&compID, &b.Name, &b.Answers, &b.Mentions, &meanPos); err != nil {
			return nil, err
		}
		if compID.Valid {
			id := compID.Int64
			b.CompetitorID = &id
		} else {
			b.IsOwn, sawOwn = true, true
		}
		if meanPos.Valid {
			b.MeanPosition = round2(meanPos.Float64)
		}
		b.Visibility = ratio(b.Answers, answers)
		b.ShareOfVoice = ratio(b.Mentions, allMentions)
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if !sawOwn {
		property, err := s.db.Property(ctx)
		if err == nil {
			out = append(out, BrandStanding{Name: property.Name, IsOwn: true})
		}
	}

	// Competitors with no mentions at all are left out: a tracked rival that
	// never came up is a row of zeros, and the property's own zero is the
	// only one worth the space.
	return out, nil
}

// Sources lists cited sites, most cited first.
func (s *Service) Sources(ctx context.Context, w Window, limit int) ([]SourceRow, error) {
	if limit <= 0 {
		limit = 25
	}
	filter, args := w.where("c")
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, `
		SELECT ct.site, ct.source_type, COUNT(*), COUNT(DISTINCT c.id)
		FROM citation ct
		JOIN chat c ON c.id = ct.chat_id
		JOIN prompt p ON p.id = c.prompt_id
		WHERE `+filter+`
		GROUP BY ct.site, ct.source_type
		ORDER BY COUNT(*) DESC, ct.site
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SourceRow
	for rows.Next() {
		var r SourceRow
		if err := rows.Scan(&r.Site, &r.SourceType, &r.Citations, &r.Answers); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Series returns visibility per day, oldest first.
//
// Only days that actually ran appear. Filling the gaps with zeros would draw
// a line that dives to nothing every day nobody ran anything, which reads as
// a collapse in visibility rather than an absence of data.
func (s *Service) Series(ctx context.Context, w Window) ([]DayPoint, error) {
	filter, args := w.where("c")
	rows, err := s.db.QueryContext(ctx, `
		SELECT date(c.created_at) AS day,
		       COUNT(DISTINCT c.id),
		       COUNT(DISTINCT CASE WHEN m.id IS NOT NULL THEN c.id END)
		FROM chat c
		JOIN prompt p ON p.id = c.prompt_id
		LEFT JOIN mention m ON m.chat_id = c.id AND m.competitor_id IS NULL
		WHERE `+filter+`
		GROUP BY day
		ORDER BY day`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DayPoint
	for rows.Next() {
		var p DayPoint
		if err := rows.Scan(&p.Day, &p.Answers, &p.Mentions); err != nil {
			return nil, err
		}
		p.Visibility = ratio(p.Mentions, p.Answers)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Cell is one prompt against one target in the grid.
type Cell struct {
	PromptID int64
	TargetID int64
	// Answers is the denominator for this cell.
	Answers int
	// Mentions is how many of them named the property.
	Mentions   int
	Visibility float64
	// MeanPosition is zero when the property never appeared in a ranked list
	// here.
	MeanPosition float64
}

// MatrixRow is one prompt's row in the grid.
type MatrixRow struct {
	PromptID int64
	Prompt   string
	Category string
	Branded  bool
	Cells    map[int64]Cell
}

// Matrix is the prompt-by-target grid.
type Matrix struct {
	Targets []store.Target
	Rows    []MatrixRow
}

// Matrix returns the grid. Branded prompts are included and tagged, because
// the grid is where you go to see everything rather than the headline.
func (s *Service) Matrix(ctx context.Context, w Window) (Matrix, error) {
	w.IncludeBranded = true
	filter, args := w.where("c")

	targets, err := s.db.Targets(ctx, false)
	if err != nil {
		return Matrix{}, err
	}
	prompts, err := s.db.Prompts(ctx, true)
	if err != nil {
		return Matrix{}, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT c.prompt_id, c.target_id,
		       COUNT(DISTINCT c.id),
		       COUNT(DISTINCT CASE WHEN m.id IS NOT NULL THEN c.id END),
		       AVG(m.list_rank)
		FROM chat c
		JOIN prompt p ON p.id = c.prompt_id
		LEFT JOIN mention m ON m.chat_id = c.id AND m.competitor_id IS NULL
		WHERE `+filter+`
		GROUP BY c.prompt_id, c.target_id`, args...)
	if err != nil {
		return Matrix{}, err
	}
	defer rows.Close()

	cells := map[int64]map[int64]Cell{}
	for rows.Next() {
		var (
			cell    Cell
			meanPos sql.NullFloat64
		)
		if err := rows.Scan(&cell.PromptID, &cell.TargetID, &cell.Answers, &cell.Mentions, &meanPos); err != nil {
			return Matrix{}, err
		}
		if meanPos.Valid {
			cell.MeanPosition = round2(meanPos.Float64)
		}
		cell.Visibility = ratio(cell.Mentions, cell.Answers)
		if cells[cell.PromptID] == nil {
			cells[cell.PromptID] = map[int64]Cell{}
		}
		cells[cell.PromptID][cell.TargetID] = cell
	}
	if err := rows.Err(); err != nil {
		return Matrix{}, err
	}

	out := Matrix{Targets: targets}
	for _, p := range prompts {
		row := MatrixRow{PromptID: p.ID, Prompt: p.Text, Category: p.Category, Branded: p.Branded, Cells: cells[p.ID]}
		if row.Cells == nil {
			row.Cells = map[int64]Cell{}
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}

// ratio divides safely. A window with nothing in it returns zero rather than
// NaN, which is the classic way a dashboard ends up printing "NaN%" on its
// first day.
func ratio(part, whole int) float64 {
	if whole <= 0 {
		return 0
	}
	return round2(float64(part) / float64(whole) * 100)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
