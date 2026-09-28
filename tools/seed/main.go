// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// seed loads a set of already-answered prompts into a fresh instance.
//
// It exists to stand up a demo with history on day one. The answers come from
// a JSON file; the mentions and citations do not. Those are produced by this
// build's own matcher and classifier over the imported text, so a seeded
// instance shows exactly what the open core computes and nothing the source
// system decided.
//
//	go run ./tools/seed -in seed.json -data ./data
//
// The file shape is documented on the types below. Every answer carries its
// original timestamp, so the trend shows the days the answers happened rather
// than one tall bar on the day of the seed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/runner"
	"github.com/limelit-co/open/internal/store"
)

type seedFile struct {
	Property struct {
		Name    string   `json:"name"`
		Domain  string   `json:"domain"`
		Aliases []string `json:"aliases"`
	} `json:"property"`
	Competitors []struct {
		Name     string `json:"name"`
		Domain   string `json:"domain"`
		Category string `json:"category"`
	} `json:"competitors"`
	Prompts []struct {
		ID              string `json:"id"`
		Text            string `json:"text"`
		Category        string `json:"category"`
		LocationCountry string `json:"location_country"`
		Active          bool   `json:"is_active"`
	} `json:"prompts"`
	Runs []struct {
		PromptID  string `json:"prompt_id"`
		Platform  string `json:"platform"`
		StartedAt string `json:"started_at"`
		Text      string `json:"raw_text"`
		Model     string `json:"model"`
		InTokens  int    `json:"prompt_tokens"`
		OutTokens int    `json:"completion_tokens"`
		Citations []struct {
			URL      string `json:"url"`
			Position int    `json:"position"`
		} `json:"citations"`
	} `json:"runs"`
}

// targetFor maps an engine id onto the target this build would use for it.
// The demo is read-only and never calls a provider, so the provider named
// here is documentation of how such an answer is normally obtained.
var targetFor = map[string]store.Target{
	"chatgpt":      {Spec: "chatgpt:openai:online", Engine: "chatgpt", Provider: "openai", Online: true, Access: "api"},
	"claude":       {Spec: "claude:anthropic:online", Engine: "claude", Provider: "anthropic", Online: true, Access: "api"},
	"perplexity":   {Spec: "perplexity:perplexity", Engine: "perplexity", Provider: "perplexity", Access: "api"},
	"gemini":       {Spec: "gemini:google:online", Engine: "gemini", Provider: "google", Online: true, Access: "api"},
	"ai_overview":  {Spec: "ai_overview:searchapi", Engine: "ai_overview", Provider: "searchapi", Access: "scraped"},
	"ai_mode":      {Spec: "ai_mode:searchapi", Engine: "ai_mode", Provider: "searchapi", Access: "scraped"},
	"bing_copilot": {Spec: "bing_copilot:searchapi", Engine: "bing_copilot", Provider: "searchapi", Access: "scraped"},
}

func main() {
	in := flag.String("in", "", "seed file")
	dataDir := flag.String("data", "", "data directory to create the database in")
	flag.Parse()
	if *in == "" || *dataDir == "" {
		fmt.Fprintln(os.Stderr, "usage: seed -in seed.json -data ./data")
		os.Exit(2)
	}
	if err := run(*in, *dataDir); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run(in, dataDir string) error {
	raw, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	var seed seedFile
	if err := json.Unmarshal(raw, &seed); err != nil {
		return fmt.Errorf("decode seed: %w", err)
	}

	ctx := context.Background()
	dbPath := filepath.Join(dataDir, "limelit.db")
	if _, err := os.Stat(dbPath); err == nil {
		return fmt.Errorf("%s already exists; seeding is for a fresh instance", dbPath)
	}
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.SaveProperty(ctx, store.Property{
		Name: seed.Property.Name, Domain: seed.Property.Domain, Aliases: seed.Property.Aliases,
	}); err != nil {
		return err
	}
	for _, c := range seed.Competitors {
		if _, err := db.AddCompetitor(ctx, store.Competitor{Name: c.Name, Domain: c.Domain, Category: c.Category}); err != nil {
			return fmt.Errorf("competitor %s: %w", c.Domain, err)
		}
	}

	promptIDs := map[string]int64{}
	for _, p := range seed.Prompts {
		id, err := db.AddPrompt(ctx, store.Prompt{
			Text: p.Text, Category: p.Category, LocationCountry: p.LocationCountry, Active: p.Active,
		})
		if err != nil {
			return fmt.Errorf("prompt %q: %w", p.Text, err)
		}
		promptIDs[p.ID] = id
	}

	// Only the engines that actually appear get a target.
	targetIDs := map[string]int64{}
	for _, r := range seed.Runs {
		if _, ok := targetIDs[r.Platform]; ok {
			continue
		}
		tmpl, ok := targetFor[r.Platform]
		if !ok {
			return fmt.Errorf("run on unknown engine %q", r.Platform)
		}
		id, err := db.AddTarget(ctx, tmpl)
		if err != nil {
			return fmt.Errorf("target %s: %w", tmpl.Spec, err)
		}
		targetIDs[r.Platform] = id
	}

	// The matcher and classifier are built once, from the property and
	// competitors just written, exactly as a live pass builds them.
	analyzer, err := runner.NewStoreAnalyzer(ctx, db)
	if err != nil {
		return err
	}

	// One evaluation per day the source ran, so the history reads as the
	// passes it was.
	byDay := map[string][]int{}
	for i, r := range seed.Runs {
		byDay[dayOf(r.StartedAt)] = append(byDay[dayOf(r.StartedAt)], i)
	}
	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)

	var chats, mentions, citations, skipped int
	for _, day := range days {
		idx := byDay[day]
		evalID, err := db.CreateEvaluation(ctx, len(idx))
		if err != nil {
			return err
		}
		for _, i := range idx {
			r := seed.Runs[i]
			promptID, ok := promptIDs[r.PromptID]
			if !ok || strings.TrimSpace(r.Text) == "" {
				skipped++
				continue
			}
			cited := make([]provider.Citation, 0, len(r.Citations))
			for _, c := range r.Citations {
				cited = append(cited, provider.Citation{URL: c.URL, Position: c.Position})
			}
			ms, cs := analyzer.Analyze(r.Text, cited)

			if _, err := db.RecordChat(ctx, store.ChatRecord{
				EvaluationID: evalID,
				PromptID:     promptID,
				TargetID:     targetIDs[r.Platform],
				Status:       store.ChatOK,
				Text:         r.Text,
				Model:        r.Model,
				InputTokens:  r.InTokens,
				OutputTokens: r.OutTokens,
				Calls:        1,
				Mentions:     ms,
				Citations:    cs,
				CreatedAt:    sqliteTime(r.StartedAt),
			}); err != nil {
				return fmt.Errorf("answer on %s: %w", day, err)
			}
			chats++
			mentions += len(ms)
			citations += len(cs)
		}
		if err := db.FinishEvaluation(ctx, evalID, store.EvaluationDone); err != nil {
			return err
		}
	}

	fmt.Printf("seeded %s\n", dbPath)
	fmt.Printf("  %d competitors, %d prompts, %d targets\n", len(seed.Competitors), len(seed.Prompts), len(targetIDs))
	fmt.Printf("  %d answers over %d days, %d mentions, %d citations, %d skipped\n", chats, len(days), mentions, citations, skipped)
	return nil
}

// dayOf and sqliteTime accept the RFC 3339 form Postgres emits and the SQLite
// form this store uses.
func dayOf(ts string) string {
	if t, err := parseTime(ts); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

func sqliteTime(ts string) string {
	if t, err := parseTime(ts); err == nil {
		return t.UTC().Format("2006-01-02 15:04:05")
	}
	return ts
}

func parseTime(ts string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999", "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised time %q", ts)
}
