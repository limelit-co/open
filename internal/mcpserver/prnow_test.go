// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mcpserver

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/limelit-co/open/internal/prnow"
	"github.com/limelit-co/open/internal/prnow/prnowtest"
)

const prnowTestKey = "prnow_mcp_test_secret"

// prnowSession is a server with the PRNow tools, talking to a fake PRNow.
func prnowSession(t *testing.T) (*mcp.ClientSession, *prnowtest.Server) {
	t.Helper()
	fake := prnowtest.New(t, prnowTestKey)
	src := func(context.Context) (*prnow.Client, error) {
		c := prnow.New(prnowTestKey)
		c.BaseURL = fake.BaseURL()
		return c, nil
	}
	return connectDeps(t, Deps{DB: emptyDB(t), PRNow: src}), fake
}

func connectDeps(t *testing.T, d Deps) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv, err := New(d)
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

func decodeInto(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}

func goodRelease() map[string]any {
	return map[string]any{
		"title":      "Example Co opens its first office in Berlin today",
		"content":    strings.Repeat("Example Co said the new office will serve customers across Europe. ", 30),
		"categories": []string{"Business"},
		"country":    "Germany",
		"city":       "Berlin",
		"plan":       "standard",
	}
}

func toolNames(t *testing.T, s *mcp.ClientSession) map[string]bool {
	t.Helper()
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	return got
}

// TestPRNowToolsExistOnlyWithASource: a server with no PRNow source, such as
// a read-only demo, has no tool that could spend PRNow credits.
func TestPRNowToolsExistOnlyWithASource(t *testing.T) {
	without := toolNames(t, connectDeps(t, Deps{DB: emptyDB(t)}))
	s, _ := prnowSession(t)
	with := toolNames(t, s)
	for _, name := range []string{"prnow_submit_release", "prnow_release_status", "prnow_list_categories"} {
		if without[name] {
			t.Errorf("%s is registered with no PRNow source", name)
		}
		if !with[name] {
			t.Errorf("%s is missing with a PRNow source", name)
		}
	}
}

func TestThePublishInstructionNamesOnlyRegisteredTools(t *testing.T) {
	s, _ := prnowSession(t)
	registered := toolNames(t, s)
	instructions := instructionsFor(false, true)
	names := regexp.MustCompile(`\bprnow_[a-z_]+\b`).FindAllString(instructions, -1)
	if len(names) == 0 {
		t.Fatal("the instructions do not route to the press release tools")
	}
	for _, name := range names {
		if !registered[name] {
			t.Errorf("the instructions name %q, which is not registered", name)
		}
	}
	if !strings.Contains(instructions, "dry_run=true") {
		t.Error("the publish line does not say to check first")
	}
	if n := len(instructionsFor(true, true)); n > 2048 {
		t.Errorf("instructions with every tool are %d bytes; clients may cut them past 2048", n)
	}
	if strings.ContainsAny(instructionsFor(true, true), "—–") {
		t.Error("the instructions carry an em or en dash")
	}
	if strings.Contains(instructionsFor(true, false), "prnow_") {
		t.Error("a server without the PRNow tools names them")
	}
}

// TestSubmitDefaultsToADryRun: the default answer is the check and the
// credits, and nothing is sent.
func TestSubmitDefaultsToADryRun(t *testing.T) {
	s, fake := prnowSession(t)
	var out prnowSubmitOut
	decodeInto(t, call(t, s, "prnow_submit_release", goodRelease()), &out)
	if !out.DryRun || !out.Ready || out.Credits != 120 || out.Account != "Ada Example, Example Co" {
		t.Fatalf("dry run = %+v", out)
	}
	if out.ReleaseID != "" || fake.Count("POST", "/submit") != 0 {
		t.Fatal("a dry run submitted")
	}
	if !strings.Contains(out.NextStep, "dry_run=false") {
		t.Errorf("next_step = %q", out.NextStep)
	}
}

func TestADryRunListsEveryProblemWithoutFailing(t *testing.T) {
	s, fake := prnowSession(t)
	var out prnowSubmitOut
	decodeInto(t, call(t, s, "prnow_submit_release", map[string]any{"title": "Too short", "content": "A few words."}), &out)
	if out.Ready {
		t.Fatal("an incomplete release was ready")
	}
	all := strings.Join(out.Problems, "\n")
	for _, want := range []string{"title is 2 words", "content is", "category", "country", "city", "plan is required"} {
		if !strings.Contains(all, want) {
			t.Errorf("problems lack %q:\n%s", want, all)
		}
	}
	if fake.Count("POST", "/submit") != 0 {
		t.Fatal("submitted")
	}
}

func TestARealSubmitOfABadReleaseSendsNothing(t *testing.T) {
	s, fake := prnowSession(t)
	res := call(t, s, "prnow_submit_release", map[string]any{"title": "Too short", "content": "x", "dry_run": false})
	if !res.IsError || !strings.Contains(resultText(res), "nothing was sent") {
		t.Fatalf("result = %s", resultText(res))
	}
	if n := len(fake.Requests()); n != 0 {
		t.Fatalf("%d requests for a release that cannot pass", n)
	}
}

func TestSubmitWithDryRunFalseSubmitsOnceWithAKey(t *testing.T) {
	s, fake := prnowSession(t)
	args := goodRelease()
	args["dry_run"] = false
	var out prnowSubmitOut
	decodeInto(t, call(t, s, "prnow_submit_release", args), &out)
	if out.DryRun || out.ReleaseID != "G9E1HJK" || out.Status != "pending" || len(out.IdempotencyKey) != 36 {
		t.Fatalf("submit = %+v", out)
	}
	if n := fake.Count("POST", "/submit"); n != 1 {
		t.Fatalf("%d submits", n)
	}
	if got := fake.Requests()[len(fake.Requests())-1].IdempotencyKey; got != out.IdempotencyKey {
		t.Fatalf("sent key %q, returned %q", got, out.IdempotencyKey)
	}

	// A retry with the returned key reuses it, so PRNow charges once.
	args["idempotency_key"] = out.IdempotencyKey
	decodeInto(t, call(t, s, "prnow_submit_release", args), &out)
	if got := fake.Requests()[len(fake.Requests())-1].IdempotencyKey; got != args["idempotency_key"] {
		t.Fatalf("retry sent key %q", got)
	}
}

func TestSubmitFailuresSayWhatToDo(t *testing.T) {
	args := goodRelease()
	args["dry_run"] = false
	for _, tc := range []struct {
		reply prnowtest.Reply
		want  string
	}{
		{prnowtest.Reply{Status: 409, Fixture: "in_progress.json"}, "call again with idempotency_key"},
		{prnowtest.Reply{Status: 409, Fixture: "duplicate.json"}, "existing release_id is G9E1HJK"},
		{prnowtest.Reply{Status: 402, Fixture: "credits.json"}, "not have enough credits"},
	} {
		s, fake := prnowSession(t)
		fake.On("POST /submit", tc.reply)
		res := call(t, s, "prnow_submit_release", args)
		if !res.IsError || !strings.Contains(resultText(res), tc.want) {
			t.Errorf("%s: result = %s", tc.reply.Fixture, resultText(res))
		}
	}
}

func TestNoKeyPointsToLoginNotTheChat(t *testing.T) {
	s := connectDeps(t, Deps{DB: emptyDB(t), PRNow: PRNowFrom(func(string) string { return "" })})
	res := call(t, s, "prnow_submit_release", goodRelease())
	text := resultText(res)
	if !res.IsError || !strings.Contains(text, "limelit prnow login") || !strings.Contains(text, "Never ask for the key in chat") {
		t.Fatalf("result = %s", text)
	}
}

func TestStatusReportsTheOutlets(t *testing.T) {
	s, _ := prnowSession(t)
	var out prnowStatusOut
	decodeInto(t, call(t, s, "prnow_release_status", map[string]any{"release_id": "G9E1HJK"}), &out)
	if out.Status != "in_progress" || len(out.Outlets) != 3 || out.Outlets[0].LiveURL == "" {
		t.Fatalf("status = %+v", out)
	}
	if !strings.Contains(out.NextStep, "Live on 1 outlet") {
		t.Errorf("next_step = %q", out.NextStep)
	}
	if res := call(t, s, "prnow_release_status", map[string]any{}); !res.IsError {
		t.Error("a status with no release_id did not fail")
	}

	var cats prnowCategoriesOut
	decodeInto(t, call(t, s, "prnow_list_categories", nil), &cats)
	if len(cats.Categories) != 3 {
		t.Fatalf("categories = %v", cats.Categories)
	}
}

// TestThePRNowKeyNeverReachesAResult: every tool, success or failure, is
// read for the key.
func TestThePRNowKeyNeverReachesAResult(t *testing.T) {
	s, fake := prnowSession(t)
	submit := goodRelease()
	submit["dry_run"] = false
	calls := []struct {
		name string
		args map[string]any
	}{
		{"prnow_list_categories", nil},
		{"prnow_submit_release", goodRelease()},
		{"prnow_submit_release", submit},
		{"prnow_release_status", map[string]any{"release_id": "G9E1HJK"}},
	}
	for _, c := range calls {
		res := call(t, s, c.name, c.args)
		raw, _ := json.Marshal(res.StructuredContent)
		if strings.Contains(resultText(res)+string(raw), prnowTestKey) {
			t.Errorf("%s returned the PRNow key", c.name)
		}
	}
	fake.On("POST /submit", prnowtest.Reply{Status: 402, Fixture: "credits.json"})
	if res := call(t, s, "prnow_submit_release", submit); strings.Contains(resultText(res), prnowTestKey) {
		t.Error("an error returned the PRNow key")
	}
}
