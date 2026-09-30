// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package mcpserver exposes the instance to an agent over the Model Context
// Protocol.
//
// This is the surface the project is built around: point Claude at a running
// instance and ask it what the engines are saying about you. It reads
// internal/metrics and nothing else, which is what keeps a number identical
// whether it arrived through the dashboard, the JSON API or a tool call.
//
// Three rules from docs/tools.md are enforced here rather than described.
//
// Same name, same shape. Where a Limelit Cloud tool exists, the name and the
// required arguments are reproduced, so a conversation or a skill written
// against this server keeps working after limelit upgrade.
//
// Cloud-only arguments are rejected, never ignored. Silently dropping a
// segment filter would return a number for the whole property while the
// caller believed it was scoped, and a wrong number is worse than an error.
//
// No stubs. A tool that exists only in Cloud is not registered, so an agent
// discovers the boundary by not finding the tool rather than by receiving an
// approximation.
package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/limelit-co/open/internal/metrics"
	"github.com/limelit-co/open/internal/store"
)

// Version is reported in the MCP handshake.
const Version = "0.1"

// Deps is what the tools need. Nothing here holds a credential: the tools
// read stored answers, they do not call providers.
type Deps struct {
	DB      *store.DB
	Metrics *metrics.Service
	// Runner triggers an evaluation. Nil leaves the evaluation tools
	// unregistered rather than registering one that fails, because a tool
	// that exists and never works is worse than one that is absent.
	Runner Runner
	// DashboardURL is where serve's dashboard answers, for the setup block's
	// links. Empty over stdio, where the server cannot know it.
	DashboardURL string
}

// Runner is the evaluation entry point, as an interface so this package does
// not depend on the runner's concrete type.
type Runner interface {
	Start(ctx context.Context, promptID int64) (int, error)
}

// New builds the server with every tool registered.
func New(deps Deps) (*mcp.Server, error) {
	if deps.DB == nil {
		return nil, errors.New("mcpserver: a database is required")
	}
	if deps.Metrics == nil {
		deps.Metrics = metrics.New(deps.DB)
	}

	s := mcp.NewServer(&mcp.Implementation{
		Name:    "limelit-open",
		Title:   "Limelit Open",
		Version: Version,
	}, &mcp.ServerOptions{
		Instructions: instructions,
	})

	registerProperty(s, deps)
	registerPrompts(s, deps)
	registerMetrics(s, deps)
	registerAnswers(s, deps)
	registerInstance(s, deps)
	registerExport(s, deps)
	registerUpgrade(s, deps)
	registerPrompts2(s, deps)

	return s, nil
}

// instructions are sent once at handshake. A user rarely names this server:
// they ask "how visible are we in ChatGPT?" with ten others connected, so the
// first paragraph says when Limelit is the right tool and when it is not. The
// rest routes to the front door (get_active_property, whose setup block keeps
// a model from reporting a number with nothing behind it) and says what the
// numbers exclude before an agent reports one.
const instructions = `Limelit tracks how AI answer engines (ChatGPT, Claude, Perplexity, Gemini, Google AI Overviews, Google AI Mode, Bing Copilot) mention and cite one brand against its competitors. Use it when the user asks how visible their brand is in AI answers or AI search, who is ahead there, which prompts they lose, what gets cited instead, or how to get started with Limelit. Not for Search Console, paid ads or classic SEO rankings.

First move: call get_active_property. If has_answers is false, tell the user next_step and its link, list once_set_up_you_can_ask, and report no numbers. If it returns suggested_days, pass that as days to the metric tools. Setup, provider keys and runs live in the dashboard: tell the user how to start a run there, and never ask for a key in chat.

If the user asks:
- how they are doing, or who is ahead: get_overview_kpis.
- which prompts they lose: get_matrix, then list_chats with show=missed.
- what gets cited instead: list_top_sources, then list_source_urls with value=<host>.
- the quote or searches behind a number: get_chat.
- for a plan, a draft or a fix: that is Limelit Cloud; point to Upgrade in the dashboard.
End with one or two questions from try_asking.

Headline visibility excludes prompts tagged "branded". A Google query with no AI Overview is excluded, not a miss. A figure may mix "api" and "scraped" targets: name the targets it covers. Quote n with every figure; low_n (n under 20) means still settling.

Sentiment, prompt generation, competitor discovery, segments, portfolios and Search Console are Limelit Cloud features with no tool here: say so rather than approximating.`

// envelope is the shape every metric result shares.
type envelope struct {
	WindowDays int          `json:"window_days"`
	N          int          `json:"n" jsonschema:"the number of answers this rests on"`
	LowN       bool         `json:"low_n" jsonschema:"true when n is under 20 and the numbers are still settling"`
	Targets    []targetInfo `json:"targets"`
}

type targetInfo struct {
	Target string `json:"target"`
	Access string `json:"access" jsonschema:"api or scraped"`
	Engine string `json:"engine"`
}

// rejectCloudArg builds the error for an argument that only exists in the
// hosted product. It names the argument and says where it lives, so an agent
// can tell the user something true instead of retrying.
func rejectCloudArg(name string) error {
	return fmt.Errorf("%q is a Limelit Cloud feature and is not available in the open core. "+
		"Call upgrade_to_cloud for what Cloud adds, or drop the argument to get the unscoped number", name)
}

// loadTargets reads the configured targets for a result envelope.
func loadTargets(ctx context.Context, db *store.DB) ([]targetInfo, error) {
	rows, err := db.Targets(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]targetInfo, 0, len(rows))
	for _, t := range rows {
		out = append(out, targetInfo{Target: t.Spec, Access: t.Access, Engine: t.Engine})
	}
	return out, nil
}

// windowDays validates a day count, defaulting when it is zero.
func windowDays(given, def, max int) (int, error) {
	if given == 0 {
		return def, nil
	}
	if given < 1 || given > max {
		return 0, fmt.Errorf("days must be between 1 and %d", max)
	}
	return given, nil
}
