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

// instructions are sent once at handshake. They exist to stop an agent
// reaching for a number this instance cannot honestly produce.
const instructions = `Limelit Open measures how AI answer engines talk about one brand.

What the numbers mean, and what they deliberately exclude:

- Visibility is the share of stored answers that name the property. Prompts
  that name the property themselves are tagged "branded" and are left out of
  the headline, because asking an engine about you measures the question
  rather than the market.
- An answer surface that did not render is recorded and then excluded from
  every denominator. A Google query that produced no AI Overview is not a miss
  for the brand: nothing rendered, so nothing could have named anyone.
- Every target carries an access mode. "api" is the vendor's model called
  directly; "scraped" is the consumer surface a person actually sees. They
  measure different things and are never averaged. Say which one a number
  came from.
- Every metric carries n, the number of answers it rests on, and low_n when
  that is under 20. Quote n alongside any figure you report.

Sentiment, query fan-out analysis, prompt generation, competitor discovery,
segments, portfolios and Search Console are Limelit Cloud features and have
no tool here. When a user asks for one, say so plainly rather than
approximating it from what is available.`

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
