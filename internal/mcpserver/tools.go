// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mcpserver

// The tool catalog. Names and argument shapes mirror Limelit Cloud so an
// agent that learned one keeps working after an upgrade.

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/limelit-co/open/internal/metrics"
	"github.com/limelit-co/open/internal/store"
)

// ---- Property and competitors ---------------------------------------------

type emptyArgs struct{}

type propertyOut struct {
	Name    string       `json:"name"`
	Domain  string       `json:"website_domain"`
	Aliases []string     `json:"aliases"`
	Targets []targetInfo `json:"targets"`
	Setup   Setup        `json:"setup" jsonschema:"where this instance stands, and the one next step to tell the user"`
}

type competitorOut struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Domain string `json:"domain" jsonschema:"the identity key: a competitor is its domain"`
}

type competitorsOut struct {
	Competitors []competitorOut `json:"competitors"`
}

func registerProperty(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_active_property",
		Description: "Start here. Call this first when the user asks about their brand in AI answers or AI " +
			"search, what Limelit can do, or how to get started with Limelit, and whenever another Limelit " +
			"result comes back empty. Returns the brand (name, website domain, the aliases the mention matcher " +
			"searches for, the targets with their access mode) and a setup block: data_state, has_answers, one " +
			"next_step to tell the user with the dashboard link, what Limelit answers once set up, and questions " +
			"to try. Report Limelit figures only when has_answers is true.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, propertyOut, error) {
		setup, err := Status(ctx, d.DB, d.Metrics, d.DashboardURL)
		if err != nil {
			return nil, propertyOut{}, err
		}
		// A fresh instance is a normal answer, not an error: the setup block
		// is what the model should relay.
		out := propertyOut{Aliases: []string{}, Targets: []targetInfo{}, Setup: setup}
		p, err := d.DB.Property(ctx)
		if errors.Is(err, store.ErrNotFound) {
			return nil, out, nil
		} else if err != nil {
			return nil, propertyOut{}, err
		}
		targets, err := loadTargets(ctx, d.DB)
		if err != nil {
			return nil, propertyOut{}, err
		}
		out.Name, out.Domain, out.Aliases, out.Targets = p.Name, p.Domain, p.Aliases, targets
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_competitors",
		Description: "List the tracked competitors. Every one is measured on the same prompts as the " +
			"property, so their numbers are directly comparable with yours.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, competitorsOut, error) {
		rows, err := d.DB.Competitors(ctx)
		if err != nil {
			return nil, competitorsOut{}, err
		}
		out := competitorsOut{Competitors: make([]competitorOut, 0, len(rows))}
		for _, c := range rows {
			out.Competitors = append(out.Competitors, competitorOut{ID: c.ID, Name: c.Name, Domain: c.Domain})
		}
		return nil, out, nil
	})
}

// ---- Prompts ---------------------------------------------------------------

type listPromptsArgs struct {
	IncludeInactive bool   `json:"include_inactive,omitempty"`
	Segment         string `json:"segment,omitempty" jsonschema:"Cloud only. Passing this is an error rather than being ignored"`
}

type promptOut struct {
	ID       int64  `json:"id"`
	Text     string `json:"text"`
	Category string `json:"category"`
	Branded  bool   `json:"branded" jsonschema:"true when the prompt names the property; excluded from the headline"`
	Active   bool   `json:"is_active"`
}

type promptsOut struct {
	Prompts []promptOut `json:"prompts"`
}

func registerPrompts(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_prompts",
		Description: "List the tracked prompts. A prompt tagged branded names the property itself and is " +
			"excluded from the headline visibility number, because asking an engine about you measures the " +
			"question rather than the market. Category matters too: an answer to a comparison prompt lists " +
			"rivals by construction.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listPromptsArgs) (*mcp.CallToolResult, promptsOut, error) {
		if in.Segment != "" {
			return nil, promptsOut{}, rejectCloudArg("segment")
		}
		rows, err := d.DB.Prompts(ctx, in.IncludeInactive)
		if err != nil {
			return nil, promptsOut{}, err
		}
		out := promptsOut{Prompts: make([]promptOut, 0, len(rows))}
		for _, p := range rows {
			out.Prompts = append(out.Prompts, promptOut{
				ID: p.ID, Text: p.Text, Category: p.Category, Branded: p.Branded, Active: p.Active,
			})
		}
		return nil, out, nil
	})
}

// ---- Metrics ---------------------------------------------------------------

type windowArgs struct {
	Days    int    `json:"days,omitempty" jsonschema:"trailing window in days; omit for 30"`
	Segment string `json:"segment,omitempty" jsonschema:"Cloud only. Passing this is an error rather than being ignored"`
}

type overviewOut struct {
	envelope
	Visibility          float64          `json:"visibility_pct" jsonschema:"share of answers naming the property, branded prompts excluded"`
	ShareOfVoice        float64          `json:"share_of_voice_pct"`
	CitationShare       float64          `json:"citation_share_pct"`
	MeanPosition        float64          `json:"mean_position" jsonschema:"average rank in a ranked list; 0 means never ranked"`
	AnswersWithMentions int              `json:"answers_with_mentions"`
	Prompts             int              `json:"prompt_count"`
	Competitors         int              `json:"competitor_count"`
	ByCategory          []categoryOut    `json:"by_category"`
	Ranking             []standingOut    `json:"ranking"`
	TopSources          []sourceOut      `json:"top_sources"`
	Excluded            exclusionNoteOut `json:"excluded"`
}

type exclusionNoteOut struct {
	BrandedPrompts  string `json:"branded_prompts"`
	NoAnswerSurface string `json:"no_answer_surface"`
}

var standardExclusions = exclusionNoteOut{
	BrandedPrompts:  "Prompts naming the property are excluded from visibility. They are still measurable with get_matrix.",
	NoAnswerSurface: "Answers where the surface did not render are excluded from every denominator. They are not brand misses.",
}

type categoryOut struct {
	Category   string  `json:"category"`
	Visibility float64 `json:"visibility_pct"`
	Answers    int     `json:"n"`
}

type standingOut struct {
	Name         string  `json:"name"`
	IsOwn        bool    `json:"is_own"`
	Visibility   float64 `json:"visibility_pct"`
	ShareOfVoice float64 `json:"share_of_voice_pct"`
	MeanPosition float64 `json:"mean_position"`
	Answers      int     `json:"answers"`
	Mentions     int     `json:"mentions"`
}

type sourceOut struct {
	Site       string `json:"site"`
	SourceType string `json:"source_type" jsonschema:"own, competitor, social, informational or other"`
	Citations  int    `json:"citations"`
	Answers    int    `json:"answers"`
}

func registerMetrics(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_overview_kpis",
		Description: "The headline numbers over a window: visibility, share of voice, citation share, " +
			"average position, the competitor ranking and the top cited sources. Every figure carries n, " +
			"the answers it rests on. Quote n when you report a figure: this instance may only have a " +
			"day of data.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in windowArgs) (*mcp.CallToolResult, overviewOut, error) {
		if in.Segment != "" {
			return nil, overviewOut{}, rejectCloudArg("segment")
		}
		days, err := windowDays(in.Days, 30, 365)
		if err != nil {
			return nil, overviewOut{}, err
		}
		w := metrics.Window{Days: days}

		ov, err := d.Metrics.Overview(ctx, w)
		if err != nil {
			return nil, overviewOut{}, err
		}
		targets, err := loadTargets(ctx, d.DB)
		if err != nil {
			return nil, overviewOut{}, err
		}
		standings, err := d.Metrics.Standings(ctx, w)
		if err != nil {
			return nil, overviewOut{}, err
		}
		sources, err := d.Metrics.Sources(ctx, w, 10)
		if err != nil {
			return nil, overviewOut{}, err
		}

		out := overviewOut{
			envelope:            envelope{WindowDays: days, N: ov.Answers, LowN: ov.LowN, Targets: targets},
			Visibility:          ov.Visibility,
			ShareOfVoice:        ov.ShareOfVoice,
			CitationShare:       ov.CitationShare,
			MeanPosition:        ov.MeanPosition,
			AnswersWithMentions: ov.AnswersWithMentions,
			Prompts:             ov.Prompts,
			Competitors:         ov.Competitors,
			Excluded:            standardExclusions,
		}
		for _, c := range ov.Categories {
			out.ByCategory = append(out.ByCategory, categoryOut{Category: c.Category, Visibility: c.Visibility, Answers: c.Answers})
		}
		for _, b := range standings {
			out.Ranking = append(out.Ranking, standingOut{
				Name: b.Name, IsOwn: b.IsOwn, Visibility: b.Visibility,
				ShareOfVoice: b.ShareOfVoice, MeanPosition: b.MeanPosition,
				Answers: b.Answers, Mentions: b.Mentions,
			})
		}
		for _, s := range sources {
			out.TopSources = append(out.TopSources, sourceOut{Site: s.Site, SourceType: s.SourceType, Citations: s.Citations, Answers: s.Answers})
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_kpi_history",
		Description: "The daily visibility series behind the headline. Only days that actually ran appear: " +
			"gaps are not filled with zeros, because a day nobody ran anything is not a day the brand " +
			"vanished.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in windowArgs) (*mcp.CallToolResult, historyOut, error) {
		if in.Segment != "" {
			return nil, historyOut{}, rejectCloudArg("segment")
		}
		days, err := windowDays(in.Days, 30, 366)
		if err != nil {
			return nil, historyOut{}, err
		}
		points, err := d.Metrics.Series(ctx, metrics.Window{Days: days})
		if err != nil {
			return nil, historyOut{}, err
		}
		targets, err := loadTargets(ctx, d.DB)
		if err != nil {
			return nil, historyOut{}, err
		}
		out := historyOut{envelope: envelope{WindowDays: days, Targets: targets}}
		for _, p := range points {
			out.Days = append(out.Days, dayOut{Day: p.Day, Visibility: p.Visibility, Answers: p.Answers, Mentions: p.Mentions})
			out.N += p.Answers
		}
		out.LowN = out.N < metrics.LowNThreshold
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_matrix",
		Description: "The prompt by target grid: one cell per prompt and engine. This is the screen that " +
			"answers which engine is your weak one and which prompt you never win. Branded prompts are " +
			"included here and flagged, unlike the headline.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in matrixArgs) (*mcp.CallToolResult, matrixOut, error) {
		if in.Segment != "" {
			return nil, matrixOut{}, rejectCloudArg("segment")
		}
		switch in.Metric {
		case "", "visibility", "sov", "position":
		case "sentiment":
			return nil, matrixOut{}, rejectCloudArg("metric: sentiment")
		default:
			return nil, matrixOut{}, fmt.Errorf("metric must be visibility, sov or position")
		}

		m, err := d.Metrics.Matrix(ctx, metrics.Window{})
		if err != nil {
			return nil, matrixOut{}, err
		}
		out := matrixOut{Metric: in.Metric}
		if out.Metric == "" {
			out.Metric = "visibility"
		}
		for _, t := range m.Targets {
			out.Targets = append(out.Targets, targetInfo{Target: t.Spec, Access: t.Access, Engine: t.Engine})
		}
		for _, r := range m.Rows {
			row := matrixRowOut{PromptID: r.PromptID, Prompt: r.Prompt, Category: r.Category, Branded: r.Branded}
			for _, t := range m.Targets {
				cell, ran := r.Cells[t.ID]
				c := matrixCellOut{Target: t.Spec, Engine: t.Engine, Access: t.Access, Ran: ran && cell.Answers > 0}
				if c.Ran {
					c.Answers, c.Mentions = cell.Answers, cell.Mentions
					c.Visibility, c.MeanPosition = cell.Visibility, cell.MeanPosition
				}
				row.Cells = append(row.Cells, c)
				out.N += c.Answers
			}
			out.Rows = append(out.Rows, row)
		}
		out.LowN = out.N < metrics.LowNThreshold
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_top_sources",
		Description: "The sites the engines cite when they answer about this category, most cited first, " +
			"each classified as your own, a tracked competitor, social, informational or other. This is " +
			"the list of pages you would have to appear on or displace.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sourcesArgs) (*mcp.CallToolResult, sourcesOut, error) {
		if in.Segment != "" {
			return nil, sourcesOut{}, rejectCloudArg("segment")
		}
		days, err := windowDays(in.Days, 30, 365)
		if err != nil {
			return nil, sourcesOut{}, err
		}
		limit := in.Limit
		if limit <= 0 || limit > 200 {
			limit = 50
		}
		rows, err := d.Metrics.Sources(ctx, metrics.Window{Days: days}, limit)
		if err != nil {
			return nil, sourcesOut{}, err
		}
		ov, err := d.Metrics.Overview(ctx, metrics.Window{Days: days})
		if err != nil {
			return nil, sourcesOut{}, err
		}
		targets, err := loadTargets(ctx, d.DB)
		if err != nil {
			return nil, sourcesOut{}, err
		}
		out := sourcesOut{envelope: envelope{WindowDays: days, N: ov.Answers, LowN: ov.LowN, Targets: targets}}
		for _, s := range rows {
			out.Sources = append(out.Sources, sourceOut{Site: s.Site, SourceType: s.SourceType, Citations: s.Citations, Answers: s.Answers})
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_source_urls",
		Description: "The individual pages cited on one site, most cited first. Use it after " +
			"list_top_sources to see exactly which page of a competitor or publisher is winning.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sourceURLArgs) (*mcp.CallToolResult, sourceURLsOut, error) {
		if in.Segment != "" {
			return nil, sourceURLsOut{}, rejectCloudArg("segment")
		}
		if in.Value == "" {
			return nil, sourceURLsOut{}, fmt.Errorf("value is required: the bare host, for example otterly.ai")
		}
		days, err := windowDays(in.Days, 30, 365)
		if err != nil {
			return nil, sourceURLsOut{}, err
		}
		limit := in.Limit
		if limit <= 0 || limit > 200 {
			limit = 50
		}
		rows, err := d.Metrics.SourceURLs(ctx, in.Value, metrics.Window{Days: days}, limit)
		if err != nil {
			return nil, sourceURLsOut{}, err
		}
		out := sourceURLsOut{Site: in.Value, envelope: envelope{WindowDays: days}}
		for _, u := range rows {
			out.URLs = append(out.URLs, sourceURLOut{URL: u.URL, Title: u.Title, Citations: u.Citations, Answers: u.Answers})
			out.N += u.Answers
		}
		return nil, out, nil
	})
}

type matrixArgs struct {
	Metric  string `json:"metric,omitempty" jsonschema:"visibility, sov or position. sentiment is Cloud only"`
	Segment string `json:"segment,omitempty" jsonschema:"Cloud only. Passing this is an error rather than being ignored"`
}

type matrixOut struct {
	Metric  string         `json:"metric"`
	N       int            `json:"n"`
	LowN    bool           `json:"low_n"`
	Targets []targetInfo   `json:"targets"`
	Rows    []matrixRowOut `json:"rows"`
}

type matrixRowOut struct {
	PromptID int64           `json:"prompt_id"`
	Prompt   string          `json:"prompt"`
	Category string          `json:"category"`
	Branded  bool            `json:"branded"`
	Cells    []matrixCellOut `json:"cells"`
}

type matrixCellOut struct {
	Target string `json:"target"`
	Engine string `json:"engine"`
	Access string `json:"access"`
	// Ran is false when this prompt was never asked of this engine. That is
	// not a zero: an unasked prompt is not one the engine ignored.
	Ran          bool    `json:"ran"`
	Answers      int     `json:"n"`
	Mentions     int     `json:"mentions"`
	Visibility   float64 `json:"visibility_pct"`
	MeanPosition float64 `json:"mean_position"`
}

type historyOut struct {
	envelope
	Days []dayOut `json:"days"`
}

type dayOut struct {
	Day        string  `json:"day"`
	Visibility float64 `json:"visibility_pct"`
	Answers    int     `json:"n"`
	Mentions   int     `json:"mentions"`
}

type sourcesArgs struct {
	Days    int    `json:"days,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Segment string `json:"segment,omitempty" jsonschema:"Cloud only. Passing this is an error rather than being ignored"`
}

type sourcesOut struct {
	envelope
	Sources []sourceOut `json:"sources"`
}

type sourceURLArgs struct {
	Value   string `json:"value" jsonschema:"bare host, for example otterly.ai. Not a URL"`
	Days    int    `json:"days,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Segment string `json:"segment,omitempty" jsonschema:"Cloud only. Passing this is an error rather than being ignored"`
}

type sourceURLsOut struct {
	envelope
	Site string         `json:"site"`
	URLs []sourceURLOut `json:"urls"`
}

type sourceURLOut struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Citations int    `json:"citations"`
	Answers   int    `json:"answers"`
}

// ---- Answers ---------------------------------------------------------------

type listChatsArgs struct {
	Days     int    `json:"days,omitempty"`
	PromptID int64  `json:"prompt_id,omitempty"`
	Target   string `json:"target,omitempty" jsonschema:"a target spec such as chatgpt:openai:online"`
	Show     string `json:"show,omitempty" jsonschema:"all, mentioned, missed or failed"`
	Limit    int    `json:"limit,omitempty"`
	Offset   int    `json:"offset,omitempty"`
	Segment  string `json:"segment,omitempty" jsonschema:"Cloud only. Passing this is an error rather than being ignored"`
}

type chatRowOut struct {
	ID        int64   `json:"id"`
	PromptID  int64   `json:"prompt_id"`
	Prompt    string  `json:"prompt"`
	Target    string  `json:"target"`
	Engine    string  `json:"engine"`
	Access    string  `json:"access"`
	Status    string  `json:"status"`
	Model     string  `json:"model"`
	CreatedAt string  `json:"created_at"`
	Preview   string  `json:"preview" jsonschema:"the opening of the answer; call get_chat for the body"`
	Mentioned bool    `json:"mentioned"`
	Mentions  int     `json:"mentions"`
	Citations int     `json:"citations"`
	Position  float64 `json:"position"`
}

type chatsOut struct {
	Total int          `json:"total"`
	Chats []chatRowOut `json:"chats"`
}

type getChatArgs struct {
	ID int64 `json:"id"`
}

type chatDetailOut struct {
	chatRowOut
	Text   string `json:"text"`
	Brands []struct {
		Name     string `json:"name"`
		IsOwn    bool   `json:"is_own"`
		Position int    `json:"position"`
		Offset   int    `json:"offset"`
	} `json:"brands"`
	Sources []struct {
		Position   int    `json:"position"`
		URL        string `json:"url"`
		Site       string `json:"site"`
		Title      string `json:"title"`
		SourceType string `json:"source_type"`
	} `json:"sources"`
	FanOut       []string `json:"fan_out" jsonschema:"the searches the engine ran while grounding; empty means the provider did not report any"`
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	Error        string   `json:"error,omitempty"`
}

func registerAnswers(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_chats",
		Description: "The stored answers, newest first, without their bodies. This is the evidence behind " +
			"every metric. Use show=missed to read the answers that left the brand out, which is usually " +
			"the more useful half.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listChatsArgs) (*mcp.CallToolResult, chatsOut, error) {
		if in.Segment != "" {
			return nil, chatsOut{}, rejectCloudArg("segment")
		}
		filter := metrics.ChatFilter{PromptID: in.PromptID, Days: in.Days, Limit: in.Limit, Offset: in.Offset}
		switch in.Show {
		case "", "all":
		case "mentioned":
			yes := true
			filter.Mentioned = &yes
		case "missed":
			no := false
			filter.Mentioned = &no
		case "failed":
			filter.Status = store.ChatFailed
		default:
			return nil, chatsOut{}, fmt.Errorf("show must be all, mentioned, missed or failed")
		}
		if in.Target != "" {
			id, err := targetIDBySpec(ctx, d.DB, in.Target)
			if err != nil {
				return nil, chatsOut{}, err
			}
			filter.TargetID = id
		}

		rows, err := d.Metrics.Chats(ctx, filter)
		if err != nil {
			return nil, chatsOut{}, err
		}
		total, err := d.Metrics.CountChats(ctx, filter)
		if err != nil {
			return nil, chatsOut{}, err
		}
		out := chatsOut{Total: total}
		for _, c := range rows {
			out.Chats = append(out.Chats, chatRow(c))
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_chat",
		Description: "One answer in full: the text, every brand found in it with its rank and byte offset, " +
			"every source cited, and the searches the engine ran on the way there. The offsets are the " +
			"matcher's own, so what is counted and what is quotable are the same thing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getChatArgs) (*mcp.CallToolResult, chatDetailOut, error) {
		if in.ID == 0 {
			return nil, chatDetailOut{}, fmt.Errorf("id is required")
		}
		c, err := d.Metrics.Chat(ctx, in.ID)
		if err != nil {
			return nil, chatDetailOut{}, fmt.Errorf("no answer with id %d", in.ID)
		}
		out := chatDetailOut{
			chatRowOut:   chatRow(c.ChatSummary),
			Text:         c.Text,
			FanOut:       c.FanOut,
			InputTokens:  c.InputTok,
			OutputTokens: c.OutputTok,
			Error:        c.Error,
		}
		for _, b := range c.Brands {
			out.Brands = append(out.Brands, struct {
				Name     string `json:"name"`
				IsOwn    bool   `json:"is_own"`
				Position int    `json:"position"`
				Offset   int    `json:"offset"`
			}{b.Name, b.IsOwn, b.Position, b.Offset})
		}
		for _, s := range c.Sources {
			out.Sources = append(out.Sources, struct {
				Position   int    `json:"position"`
				URL        string `json:"url"`
				Site       string `json:"site"`
				Title      string `json:"title"`
				SourceType string `json:"source_type"`
			}{s.Position, s.URL, s.Site, s.Title, s.SourceType})
		}
		return nil, out, nil
	})
}

func chatRow(c metrics.ChatSummary) chatRowOut {
	return chatRowOut{
		ID: c.ID, PromptID: c.PromptID, Prompt: c.Prompt, Target: c.Target,
		Engine: c.Engine, Access: c.Access, Status: c.Status, Model: c.Model,
		CreatedAt: c.CreatedAt, Preview: c.Preview, Mentioned: c.Mentioned,
		Mentions: c.Mentions, Citations: c.Citations, Position: c.Position,
	}
}

func targetIDBySpec(ctx context.Context, db *store.DB, spec string) (int64, error) {
	rows, err := db.Targets(ctx, false)
	if err != nil {
		return 0, err
	}
	var known []string
	for _, t := range rows {
		if t.Spec == spec {
			return t.ID, nil
		}
		known = append(known, t.Spec)
	}
	return 0, fmt.Errorf("no target %q; configured: %v", spec, known)
}

// ---- Instance --------------------------------------------------------------

type targetsOut struct {
	Targets []targetDetailOut `json:"targets"`
}

type targetDetailOut struct {
	ID       int64  `json:"id"`
	Target   string `json:"target"`
	Engine   string `json:"engine"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Access   string `json:"access"`
	Enabled  bool   `json:"enabled"`
}

type usageOut struct {
	Days  int           `json:"days"`
	Usage []usageDayOut `json:"usage"`
	Note  string        `json:"note"`
}

type usageDayOut struct {
	Day          string `json:"day"`
	Target       string `json:"target"`
	Calls        int    `json:"calls"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

func registerInstance(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_targets",
		Description: "The configured targets: which engine is reached through which provider, in which " +
			"access mode. Credentials are never returned. An api target and a scraped target of the same " +
			"engine measure different surfaces and are never averaged together.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, targetsOut, error) {
		rows, err := d.DB.Targets(ctx, false)
		if err != nil {
			return nil, targetsOut{}, err
		}
		out := targetsOut{Targets: make([]targetDetailOut, 0, len(rows))}
		for _, t := range rows {
			out.Targets = append(out.Targets, targetDetailOut{
				ID: t.ID, Target: t.Spec, Engine: t.Engine, Provider: t.Provider,
				Model: t.Model, Access: t.Access, Enabled: t.Enabled,
			})
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_usage",
		Description: "Calls and tokens per target per day. There is no currency here on purpose: this " +
			"tool counts what was spent in requests, not in money, because the open core has no pricing " +
			"table and inventing one would be worse than omitting it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in windowArgs) (*mcp.CallToolResult, usageOut, error) {
		days, err := windowDays(in.Days, 30, 31)
		if err != nil {
			return nil, usageOut{}, err
		}
		rows, err := d.DB.Usage(ctx, days)
		if err != nil {
			return nil, usageOut{}, err
		}
		out := usageOut{Days: days, Note: "Counts only. The open core carries no pricing table."}
		for _, u := range rows {
			out.Usage = append(out.Usage, usageDayOut{
				Day: u.Day, Target: u.TargetSpec, Calls: u.Calls,
				InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
			})
		}
		return nil, out, nil
	})
}

// registerPrompts2 holds the MCP prompt templates: the walks worth repeating.
func registerPrompts2(s *mcp.Server, d Deps) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "limelit_weekly_pulse",
		Description: "What moved this week, with the answers that explain it.",
	}, func(ctx context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{
			Description: "Weekly pulse",
			Messages: []*mcp.PromptMessage{{
				Role: "user",
				Content: &mcp.TextContent{Text: `Give me this week's AI visibility pulse.

1. get_overview_kpis with days=7, then again with days=14 so you can state the move.
2. get_kpi_history for the shape.
3. For anything that moved, list_chats with show=missed and read two with get_chat, and quote the actual sentence.

Report n alongside every figure, and say whether a number came from an api or a scraped target. If n is under 20, say the numbers are still settling rather than presenting them as a trend.`},
			}},
		}, nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "limelit_competitor_radar",
		Description: "Who is gaining on you, by how much, and what they are being cited for.",
		Arguments: []*mcp.PromptArgument{
			{Name: "window_days", Description: "Trailing window, default 30"},
			{Name: "threshold_pp", Description: "Only report moves of at least this many percentage points, default 10"},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		days := req.Params.Arguments["window_days"]
		if days == "" {
			days = "30"
		}
		threshold := req.Params.Arguments["threshold_pp"]
		if threshold == "" {
			threshold = "10"
		}
		return &mcp.GetPromptResult{
			Description: "Competitor radar",
			Messages: []*mcp.PromptMessage{{
				Role: "user",
				Content: &mcp.TextContent{Text: fmt.Sprintf(`Show me the competitor radar over the last %s days.

1. list_competitors, so you know who is actually tracked. A brand that is not tracked cannot appear in these numbers at all, and saying so is part of the answer.
2. get_overview_kpis with days=%s. The ranking it returns carries each brand's visibility, share of voice and average position, mine included.
3. get_overview_kpis again with days=%s doubled, and derive the earlier window by subtraction so you can state a move rather than a level.
4. Report only brands that moved at least %s percentage points, and say plainly if nobody did.
5. For the brand that moved most, call list_source_urls on its domain to see which of its pages are being cited.
6. get_matrix to find the specific prompts where that brand is named and I am not.

Rules for the answer. Quote n with every figure; if n is under 20 say the numbers are still settling rather than presenting a move as a trend. Say whether a figure came from an api or a scraped target. Do not attribute a cause: this instance measures what was said, not why. End with the prompts and the pages, not with advice.`, days, days, days, threshold)},
			}},
		}, nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "limelit_why_not_cited",
		Description: "Why one prompt never names us, and who it names instead.",
		Arguments:   []*mcp.PromptArgument{{Name: "prompt_id", Description: "The prompt to investigate", Required: true}},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		id := req.Params.Arguments["prompt_id"]
		return &mcp.GetPromptResult{
			Description: "Why not cited",
			Messages: []*mcp.PromptMessage{{
				Role: "user",
				Content: &mcp.TextContent{Text: fmt.Sprintf(`Work out why prompt %s does not name us.

1. get_matrix and find that prompt's row. Say which engines miss it and which do not.
2. list_chats with prompt_id=%s and show=missed.
3. get_chat on two of them. Read the fan_out: the searches the engine actually ran are often not the question asked, and they name the query we would have to win.
4. Note which brands the answer named instead, and at what rank.
5. list_top_sources to see who is being cited on this category.

End with the specific pages we would have to appear on or displace. Do not speculate about sentiment or fan-out rewriting: those are Cloud features and this instance cannot measure them.`, id, id)},
			}},
		}, nil
	})
}
