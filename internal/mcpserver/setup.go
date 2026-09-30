// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"github.com/limelit-co/open/internal/config"
	"github.com/limelit-co/open/internal/metrics"
	"github.com/limelit-co/open/internal/store"
)

// Data states, from nothing to a settled window. A model reads these to
// decide whether it may report a number at all.
const (
	StateNotSetUp        = "not_set_up"
	StateSetupIncomplete = "setup_incomplete"
	StateNeverRun        = "never_run"
	StateStale           = "stale"
	StateSettling        = "settling"
	StateReady           = "ready"
)

// defaultWindow is the window the metric tools use when days is omitted.
const defaultWindow = 30

// defaultDashboard is what a stdio connection says when it cannot know where
// serve is listening: the address serve prints on start, which is this unless
// --addr changed it.
const defaultDashboard = "http://localhost:1515"

// OnceSetUpYouCanAsk is what Limelit answers, with no numbers in it, so it is
// safe to show before anything has run.
var OnceSetUpYouCanAsk = []string{
	"How visible is my brand in AI answers, and is it moving?",
	"Who is ahead of us, by share of voice and average position?",
	"Which prompts and engines leave us out?",
	"Which sites and pages get cited instead of us?",
	"Show me the exact answer behind a number.",
}

// TryAsking is offered once there are answers to ask about.
var TryAsking = []string{
	"How visible is my brand in AI answers, and which prompts am I losing?",
	"Who is ahead of us in AI answers, and which sites get cited instead of us?",
	"Give me a weekly summary of our AI visibility that I can share.",
}

// Setup is where an instance stands, told the same way to a model (the
// setup block of get_active_property) and to a person (the startup banner),
// so the two can never disagree.
type Setup struct {
	DataState   string `json:"data_state" jsonschema:"not_set_up, setup_incomplete, never_run, stale, settling or ready"`
	HasAnswers  bool   `json:"has_answers" jsonschema:"report Limelit figures only when this is true"`
	Prompts     int    `json:"prompts"`
	Competitors int    `json:"competitors"`
	Targets     int    `json:"targets" jsonschema:"engines configured to be asked; each needs a provider key"`
	Answers     int    `json:"answers" jsonschema:"every stored answer, all time"`
	// N is the answers in the metric tools' default window, which is what a
	// figure quoted without a days argument rests on.
	N             int    `json:"n" jsonschema:"answers in the last 30 days"`
	LowN          bool   `json:"low_n" jsonschema:"true when n is under 20: figures are a first look, not a trend"`
	LastAnswerAt  string `json:"last_answer_at,omitempty"`
	SuggestedDays int    `json:"suggested_days,omitempty" jsonschema:"when the last 30 days hold no answers, pass this as days to the metric tools"`
	Dashboard     string `json:"dashboard" jsonschema:"where setup, provider keys and runs live"`
	NextStep      string `json:"next_step" jsonschema:"one step to tell the user, in plain words"`
	// DataDir is returned only when nothing is set up, where it helps spot a
	// connection reading a different folder from the one serve uses.
	DataDir            string   `json:"data_dir,omitempty"`
	OnceSetUpYouCanAsk []string `json:"once_set_up_you_can_ask" jsonschema:"what Limelit answers, with no numbers; safe to show at any time"`
	TryAsking          []string `json:"try_asking,omitempty" jsonschema:"questions to offer the user next, present once there are answers"`
}

// Status reports where the instance stands. dashboard is serve's address, or
// "" when the caller cannot know it (a stdio connection).
func Status(ctx context.Context, db *store.DB, m *metrics.Service, dashboard string) (Setup, error) {
	if dashboard == "" {
		dashboard = defaultDashboard
	}
	s := Setup{Dashboard: dashboard, OnceSetUpYouCanAsk: OnceSetUpYouCanAsk}

	counts, err := db.Counts(ctx)
	if err != nil {
		return s, err
	}
	s.Prompts, s.Competitors, s.Targets, s.Answers = counts.Prompts, counts.Competitors, counts.Targets, counts.Chats
	s.LastAnswerAt = counts.LastChatAt

	if _, err := db.Property(ctx); errors.Is(err, store.ErrNotFound) {
		s.DataState = StateNotSetUp
		if dir, err := filepath.Abs(config.DataDir()); err == nil {
			s.DataDir = dir
		}
		s.NextStep = fmt.Sprintf("Limelit is running but not set up yet. Open %s and finish setup: your brand, "+
			"a few competitors, the prompts to track, and one key: a provider's, or a free Limelit Cloud key. "+
			"Keys go there, never in this chat. "+
			"If setup is already done there, this connection reads %s, which may not be the folder limelit serve "+
			"uses; add Limelit again with the exact line limelit serve printed.", dashboard, s.DataDir)
		return s, nil
	} else if err != nil {
		return s, err
	}

	switch {
	case s.Prompts == 0 || s.Targets == 0:
		s.DataState = StateSetupIncomplete
		var missing []string
		if s.Prompts == 0 {
			missing = append(missing, "the prompts to track")
		}
		if s.Targets == 0 {
			missing = append(missing, "a provider key or a free Limelit Cloud key, which picks the engines to ask")
		}
		s.NextStep = fmt.Sprintf("Setup is not finished: it still needs %s. Finish it at %s.",
			strings.Join(missing, " and "), dashboard)
		return s, nil
	case s.Answers == 0:
		s.DataState = StateNeverRun
		s.NextStep = fmt.Sprintf("Setup is done, but nothing has run yet. Press Run in the dashboard at %s. "+
			"One run asks each prompt of each tracked engine once, using the key you added.", dashboard)
		return s, nil
	}

	// Only answers the headline counts make a number: a failed call, a
	// surface that did not render or a branded prompt is stored but excluded.
	all, err := m.Overview(ctx, metrics.Window{})
	if err != nil {
		return s, err
	}
	if all.Answers == 0 {
		s.DataState = StateNeverRun
		s.NextStep = fmt.Sprintf("Runs have happened, but none has produced an answer Limelit can count yet: the "+
			"calls failed, or only branded prompts ran. Check Settings at %s, then press Run.", dashboard)
		return s, nil
	}

	s.HasAnswers = true
	s.TryAsking = TryAsking
	ov, err := m.Overview(ctx, metrics.Window{Days: defaultWindow})
	if err != nil {
		return s, err
	}
	s.N, s.LowN = ov.Answers, ov.LowN

	switch {
	case s.N == 0:
		s.DataState = StateStale
		s.SuggestedDays = 365
		for _, days := range []int{90, 180} {
			wider, err := m.Overview(ctx, metrics.Window{Days: days})
			if err != nil {
				return s, err
			}
			if wider.Answers > 0 {
				s.SuggestedDays = days
				break
			}
		}
		s.NextStep = fmt.Sprintf("The last %d days hold no answers. Ask with days=%d to see the figures from "+
			"the last run, and press Run at %s for fresh answers.", defaultWindow, s.SuggestedDays, dashboard)
	case s.LowN:
		s.DataState = StateSettling
		s.NextStep = fmt.Sprintf("There are %d answers in the last %d days. Under 20, read every figure as a "+
			"first look, not a trend.", s.N, defaultWindow)
	default:
		s.DataState = StateReady
		s.NextStep = fmt.Sprintf("%d answers in the last %d days, the newest at %s.", s.N, defaultWindow, s.LastAnswerAt)
	}
	return s, nil
}

// DashboardURL is the address a browser on this machine uses for a listen
// address: a listener on every interface is reached at localhost.
func DashboardURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}
