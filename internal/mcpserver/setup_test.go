// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/limelit-co/open/internal/metrics"
	"github.com/limelit-co/open/internal/store"
)

// emptyDB is an instance before the wizard: nothing saved.
func emptyDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "limelit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func session(t *testing.T, db *store.DB, dashboard string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv, err := New(Deps{DB: db, Metrics: metrics.New(db), DashboardURL: dashboard})
	if err != nil {
		t.Fatal(err)
	}
	clientT, serverT := mcp.NewInMemoryTransports()
	go srv.Run(ctx, serverT)
	s, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// frontDoor calls get_active_property and decodes it. A tool error fails the
// test: the front door must answer every state, never error on one.
func frontDoor(t *testing.T, s *mcp.ClientSession) propertyOut {
	t.Helper()
	res := call(t, s, "get_active_property", nil)
	if res.IsError {
		t.Fatalf("get_active_property returned an error: %s", resultText(res))
	}
	var out propertyOut
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	return out
}

var digits = regexp.MustCompile(`\d+(\.\d+)?%`)

// TestFrontDoorOnAFreshInstance. Before the wizard, the first call used to
// return "store: not found". It has to be a normal answer that sends the user
// to the dashboard, names no figure, and never asks for a key in chat.
func TestFrontDoorOnAFreshInstance(t *testing.T) {
	out := frontDoor(t, session(t, emptyDB(t), "http://localhost:1515"))
	st := out.Setup
	if st.DataState != StateNotSetUp || st.HasAnswers {
		t.Fatalf("state = %q, has_answers = %v", st.DataState, st.HasAnswers)
	}
	if !strings.Contains(st.NextStep, "http://localhost:1515") || !strings.Contains(st.NextStep, "never in this chat") {
		t.Errorf("next_step does not send the user to the dashboard or warns nothing about keys: %q", st.NextStep)
	}
	if st.DataDir == "" || !strings.Contains(st.NextStep, st.DataDir) {
		t.Errorf("a fresh-looking instance must name the folder it reads, to catch a wrong one: %q", st.NextStep)
	}
	if len(st.OnceSetUpYouCanAsk) == 0 || len(st.TryAsking) != 0 {
		t.Errorf("before setup: once_set_up_you_can_ask %d, try_asking %d", len(st.OnceSetUpYouCanAsk), len(st.TryAsking))
	}
	for _, q := range st.OnceSetUpYouCanAsk {
		if digits.MatchString(q) {
			t.Errorf("a question shown before any answer carries a figure: %q", q)
		}
	}
}

// TestFrontDoorWhenSetupStopsHalfway. A brand saved with no prompts and no
// key cannot run; the step names what is missing.
func TestFrontDoorWhenSetupStopsHalfway(t *testing.T) {
	db := emptyDB(t)
	if err := db.SaveProperty(context.Background(), store.Property{Name: "Acme", Domain: "acme.com"}); err != nil {
		t.Fatal(err)
	}
	st := frontDoor(t, session(t, db, "")).Setup
	if st.DataState != StateSetupIncomplete || st.HasAnswers {
		t.Fatalf("state = %q", st.DataState)
	}
	for _, want := range []string{"prompts to track", "provider key", defaultDashboard} {
		if !strings.Contains(st.NextStep, want) {
			t.Errorf("next_step %q does not mention %q", st.NextStep, want)
		}
	}
	if st.DataDir != "" {
		t.Error("data_dir belongs only to the not-set-up state")
	}
}

// setUpDB is an instance that finished the wizard: a brand, a prompt, a key.
func setUpDB(t *testing.T) (*store.DB, int64, int64) {
	t.Helper()
	ctx := context.Background()
	db := emptyDB(t)
	if err := db.SaveProperty(ctx, store.Property{Name: "Acme", Domain: "acme.com"}); err != nil {
		t.Fatal(err)
	}
	promptID, err := db.AddPrompt(ctx, store.Prompt{Text: "best widget tools", Category: "discovery", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := db.AddTarget(ctx, store.Target{
		Spec: "chatgpt:openai:online", Engine: "chatgpt", Provider: "openai", Online: true, Access: "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, promptID, targetID
}

func record(t *testing.T, db *store.DB, promptID, targetID int64, status, createdAt string) {
	t.Helper()
	ctx := context.Background()
	evalID, err := db.CreateEvaluation(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	rec := store.ChatRecord{
		EvaluationID: evalID, PromptID: promptID, TargetID: targetID,
		Status: status, Text: "Acme is a solid choice.", Model: "test", CreatedAt: createdAt,
		Mentions: []store.Mention{{BrandName: "Acme", BrandKey: "acme", OffsetStart: 0, OffsetEnd: 4}},
	}
	if status != store.ChatOK {
		rec.Text, rec.Mentions, rec.Error = "", nil, "provider refused"
	}
	if _, err := db.RecordChat(ctx, rec); err != nil {
		t.Fatal(err)
	}
}

// TestFrontDoorBeforeTheFirstRun. Set up and never run is the state a user is
// in right after the wizard; the step is to press Run, not to read a 0%.
func TestFrontDoorBeforeTheFirstRun(t *testing.T) {
	db, _, _ := setUpDB(t)
	st := frontDoor(t, session(t, db, "http://localhost:1515")).Setup
	if st.DataState != StateNeverRun || st.HasAnswers {
		t.Fatalf("state = %q, has_answers = %v", st.DataState, st.HasAnswers)
	}
	if !strings.Contains(st.NextStep, "Press Run") {
		t.Errorf("next_step %q does not say how to start a run", st.NextStep)
	}
}

// TestFrontDoorWhenNoAnswerCounts. Runs that only failed leave rows but no
// figure; has_answers stays false and the step points at Settings.
func TestFrontDoorWhenNoAnswerCounts(t *testing.T) {
	db, p, tg := setUpDB(t)
	record(t, db, p, tg, store.ChatFailed, "")
	st := frontDoor(t, session(t, db, "http://localhost:1515")).Setup
	if st.HasAnswers || st.DataState != StateNeverRun {
		t.Fatalf("state = %q, has_answers = %v", st.DataState, st.HasAnswers)
	}
	if !strings.Contains(st.NextStep, "Settings") {
		t.Errorf("next_step %q does not point at Settings", st.NextStep)
	}
}

// TestFrontDoorWithAFewAnswers. One answer is real but thin: the figures may
// be shown, as a first look, with questions to ask next.
func TestFrontDoorWithAFewAnswers(t *testing.T) {
	db, p, tg := setUpDB(t)
	record(t, db, p, tg, store.ChatOK, "")
	st := frontDoor(t, session(t, db, "")).Setup
	if !st.HasAnswers || st.DataState != StateSettling || st.N != 1 || !st.LowN {
		t.Fatalf("state = %q, has_answers = %v, n = %d, low_n = %v", st.DataState, st.HasAnswers, st.N, st.LowN)
	}
	if len(st.TryAsking) == 0 || st.SuggestedDays != 0 {
		t.Errorf("try_asking %d, suggested_days %d", len(st.TryAsking), st.SuggestedDays)
	}
}

// TestFrontDoorWhenTheWindowIsEmpty. Automatic runs are off by default, so a
// user who ran once and asks weeks later would get n=0 from the default 30
// days. The front door says so and hands over a window that has the answers.
func TestFrontDoorWhenTheWindowIsEmpty(t *testing.T) {
	db, p, tg := setUpDB(t)
	record(t, db, p, tg, store.ChatOK, time.Now().UTC().AddDate(0, 0, -60).Format("2006-01-02 15:04:05"))
	st := frontDoor(t, session(t, db, "")).Setup
	if !st.HasAnswers || st.DataState != StateStale || st.N != 0 {
		t.Fatalf("state = %q, has_answers = %v, n = %d", st.DataState, st.HasAnswers, st.N)
	}
	if st.SuggestedDays != 90 {
		t.Errorf("suggested_days = %d, want 90 (the smallest window holding the answer)", st.SuggestedDays)
	}
	if !strings.Contains(st.NextStep, "days=90") {
		t.Errorf("next_step %q does not pass the window on", st.NextStep)
	}
}

// TestInstructionsRouteToTheFrontDoor. With ten servers connected a user asks
// in plain words, so the handshake must say when Limelit applies, send the
// model to the front door first, and name no tool that does not exist.
func TestInstructionsRouteToTheFrontDoor(t *testing.T) {
	// Two servers: one without a runner (runs happen in the dashboard) and one
	// with (the run tools). Each one's instructions name only its own tools.
	for _, tc := range []struct {
		runs bool
		s    *mcp.ClientSession
		want string
	}{
		{false, session(t, emptyDB(t), ""), "Runs start in the dashboard"},
		{true, runSession(t, 100, false).s, "dry_run=true"},
	} {
		instructions := instructionsFor(tc.runs)
		for _, want := range []string{
			"get_active_property", "has_answers", "next_step", "Not for", "never ask for a key", tc.want,
			"ChatGPT", "Claude", "Perplexity", "Gemini", "Google AI Overviews", "Google AI Mode", "Bing Copilot",
		} {
			if !strings.Contains(instructions, want) {
				t.Errorf("runs=%v: the server instructions do not mention %q", tc.runs, want)
			}
		}
		// Claude Code keeps the first 2 KB of a server's instructions.
		if len(instructions) > 2048 {
			t.Errorf("runs=%v: instructions are %d bytes; clients may cut them past 2048", tc.runs, len(instructions))
		}
		if strings.ContainsAny(instructions, "—–") {
			t.Errorf("runs=%v: the instructions carry an em or en dash", tc.runs)
		}

		res, err := tc.s.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		registered := map[string]bool{}
		for _, tool := range res.Tools {
			registered[tool.Name] = true
		}
		for _, name := range regexp.MustCompile(`\b(?:get|list|reevaluate)_[a-z_]+\b`).FindAllString(instructions, -1) {
			if !registered[name] {
				t.Errorf("runs=%v: the instructions name %q, which is not a registered tool", tc.runs, name)
			}
		}
	}
}

func TestDashboardURLReachesAnyInterfaceAtLocalhost(t *testing.T) {
	for in, want := range map[string]string{
		":1515":           "http://localhost:1515",
		"0.0.0.0:1515":    "http://localhost:1515",
		"[::]:1515":       "http://localhost:1515",
		"127.0.0.1:15151": "http://127.0.0.1:15151",
		"not an address":  "",
	} {
		if got := DashboardURL(in); got != want {
			t.Errorf("DashboardURL(%q) = %q, want %q", in, got, want)
		}
	}
}
