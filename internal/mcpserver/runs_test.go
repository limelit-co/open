// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mcpserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/limelit-co/open/internal/metrics"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/runner"
	"github.com/limelit-co/open/internal/store"
)

// runFixture is a server with a real runner over the stub provider: two
// active prompts and two enabled targets, so a full pass plans four answers.
type runFixture struct {
	s       *mcp.ClientSession
	db      *store.DB
	run     *runner.Runner
	prompts []int64
	// release unblocks a pass held at its start; nil when nothing holds it.
	release func()
}

// runSession builds the fixture. ceiling is the daily limit the tools see.
// hold keeps every pass waiting at its start until release is called, so a
// test can look at a pass while it is running.
func runSession(t *testing.T, ceiling int, hold bool) *runFixture {
	t.Helper()
	ctx := context.Background()
	db := emptyDB(t)
	if err := db.SaveProperty(ctx, store.Property{Name: "Acme", Domain: "acme.com"}); err != nil {
		t.Fatal(err)
	}
	f := &runFixture{db: db}
	for _, text := range []string{"best widget tools", "widget tools for teams"} {
		id, err := db.AddPrompt(ctx, store.Prompt{Text: text, Active: true})
		if err != nil {
			t.Fatal(err)
		}
		f.prompts = append(f.prompts, id)
	}
	reg := provider.NewRegistry()
	provider.RegisterStub(reg, provider.StubConfig{Answer: "Acme is the best widget tool."})
	for _, spec := range []string{"chatgpt:stub", "perplexity:stub"} {
		if _, err := db.AddTarget(ctx, store.Target{
			Spec: spec, Engine: strings.SplitN(spec, ":", 2)[0], Provider: provider.StubName, Access: "api",
		}); err != nil {
			t.Fatal(err)
		}
	}

	f.run = runner.New(db, reg, provider.StaticCredentials(nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	gate := make(chan struct{})
	var once sync.Once
	f.release = func() { once.Do(func() { close(gate) }) }
	if !hold {
		f.release()
	}
	t.Cleanup(f.release)
	f.run.NewAnalyzer = func(c context.Context) (runner.Analyzer, error) {
		<-gate
		return runner.NewStoreAnalyzer(c, db)
	}

	srv, err := New(Deps{
		DB: db, Metrics: metrics.New(db),
		Runner: f.run, RunsPerDay: func(context.Context) int { return ceiling },
	})
	if err != nil {
		t.Fatal(err)
	}
	clientT, serverT := mcp.NewInMemoryTransports()
	go srv.Run(ctx, serverT)
	f.s, err = mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.s.Close() })
	return f
}

// out calls a tool that must succeed and returns its structured result.
func out(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res := call(t, s, name, args)
	if res.IsError {
		t.Fatalf("%s %v: %s", name, args, resultText(res))
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("%s: no structured content: %s", name, resultText(res))
	}
	return m
}

// refused calls a tool that must fail and returns the error text.
func refused(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res := call(t, s, name, args)
	if !res.IsError {
		t.Fatalf("%s %v succeeded, want a refusal: %s", name, args, resultText(res))
	}
	return resultText(res)
}

func num(m map[string]any, key string) int { f, _ := m[key].(float64); return int(f) }

// waitDone polls get_run_activity until the run is no longer running.
func waitDone(t *testing.T, s *mcp.ClientSession, id int) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a := out(t, s, "get_run_activity", map[string]any{"evaluation_id": id})
		if a["status"] != store.EvaluationRunning {
			return a
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %d was still running after 10s", id)
	return nil
}

// TestRunToolsNeedARunner: a server built without one registers none of the
// evaluation tools, rather than tools that always fail.
func TestRunToolsNeedARunner(t *testing.T) {
	s, _ := connect(t)
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		switch tool.Name {
		case "reevaluate_prompt", "reevaluate_all_prompts", "get_run_activity":
			t.Errorf("%s is registered without a runner", tool.Name)
		}
	}
	if _, err := New(Deps{DB: emptyDB(t), Runner: &runner.Runner{}}); err == nil {
		t.Error("a runner without a daily ceiling was accepted")
	}
}

// TestRunToolsUseCloudNamesAndRequiredArguments, so a skill written against
// Cloud calls the same tools here.
func TestRunToolsUseCloudNamesAndRequiredArguments(t *testing.T) {
	f := runSession(t, 100, false)
	res, err := f.s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	required := map[string][]string{}
	for _, tool := range res.Tools {
		schema, _ := tool.InputSchema.(map[string]any)
		var names []string
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				names = append(names, fmt.Sprint(r))
			}
		}
		required[tool.Name] = names
	}
	for name, want := range map[string]string{
		"reevaluate_prompt": "prompt_id", "reevaluate_all_prompts": "", "get_run_activity": "",
	} {
		got, ok := required[name]
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		if strings.Join(got, ",") != want {
			t.Errorf("%s requires %v, want Cloud's %q", name, got, want)
		}
	}
}

// TestDryRunReportsThePlanAndRunsNothing: the default answer to "run it" is
// the plan, with counts and the ceiling, and no evaluation is written.
func TestDryRunReportsThePlanAndRunsNothing(t *testing.T) {
	f := runSession(t, 100, false)
	for _, tc := range []struct {
		tool                      string
		args                      map[string]any
		prompts, targets, planned int
	}{
		{"reevaluate_all_prompts", map[string]any{}, 2, 2, 4},
		{"reevaluate_all_prompts", map[string]any{"dry_run": true}, 2, 2, 4},
		{"reevaluate_prompt", map[string]any{"prompt_id": f.prompts[0]}, 1, 2, 2},
		{"reevaluate_all_prompts", map[string]any{"target": "perplexity:stub"}, 2, 1, 2},
		{"reevaluate_prompt", map[string]any{"prompt_id": f.prompts[1], "target": "chatgpt:stub"}, 1, 1, 1},
	} {
		m := out(t, f.s, tc.tool, tc.args)
		if m["dry_run"] != true || num(m, "prompts") != tc.prompts || num(m, "targets") != tc.targets ||
			num(m, "planned") != tc.planned || num(m, "runs_today") != 0 || num(m, "runs_per_day") != 100 ||
			m["within_ceiling"] != true {
			t.Errorf("%s %v = %v", tc.tool, tc.args, m)
		}
		if _, ok := m["evaluation_id"]; ok {
			t.Errorf("%s %v started a run on a dry run", tc.tool, tc.args)
		}
	}
	if _, err := f.db.LatestEvaluation(context.Background()); err != store.ErrNotFound {
		t.Errorf("a dry run wrote an evaluation: %v", err)
	}
}

// TestUnknownPromptOrTargetIsAnError, on a dry run and on a real one.
func TestUnknownPromptOrTargetIsAnError(t *testing.T) {
	f := runSession(t, 100, false)
	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"reevaluate_prompt", map[string]any{"prompt_id": 999}, "no such active prompt"},
		{"reevaluate_prompt", map[string]any{"prompt_id": 999, "dry_run": false}, "no such active prompt"},
		{"reevaluate_all_prompts", map[string]any{"target": "gemini:stub"}, "no such enabled target"},
		{"reevaluate_all_prompts", map[string]any{"target": "gemini:stub", "dry_run": false}, "no such enabled target"},
	} {
		if text := refused(t, f.s, tc.tool, tc.args); !strings.Contains(text, tc.want) {
			t.Errorf("%s %v: %s, want %q", tc.tool, tc.args, text, tc.want)
		}
	}
}

// TestOverTheCeilingIsSaidOnTheDryRunAndRefusedOnTheRun, before anything is
// spent, with the runner's own explanation.
func TestOverTheCeilingIsSaidOnTheDryRunAndRefusedOnTheRun(t *testing.T) {
	f := runSession(t, 3, false)
	m := out(t, f.s, "reevaluate_all_prompts", map[string]any{})
	if m["within_ceiling"] != false || !strings.Contains(fmt.Sprint(m["next_step"]), "daily limit") {
		t.Errorf("dry run over the ceiling = %v", m)
	}
	text := refused(t, f.s, "reevaluate_all_prompts", map[string]any{"dry_run": false})
	if !strings.Contains(text, "ceiling of 3") || !strings.Contains(text, "nothing was run") {
		t.Errorf("refusal = %s", text)
	}
	if _, err := f.db.LatestEvaluation(context.Background()); err != store.ErrNotFound {
		t.Errorf("a refused run wrote an evaluation: %v", err)
	}
	// One prompt fits.
	if m := out(t, f.s, "reevaluate_prompt", map[string]any{"prompt_id": f.prompts[0]}); m["within_ceiling"] != true {
		t.Errorf("one prompt (2 answers) under a ceiling of 3 = %v", m)
	}
}

// TestARunReturnsAtOnceAndTheAnswersLand: the start returns the id while the
// pass is still running, the status walks to done, and the metric tools see
// the new answers.
func TestARunReturnsAtOnceAndTheAnswersLand(t *testing.T) {
	f := runSession(t, 100, true)
	m := out(t, f.s, "reevaluate_all_prompts", map[string]any{"dry_run": false})
	id := num(m, "evaluation_id")
	if id == 0 || m["status"] != "running" || num(m, "planned") != 4 || num(m, "runs_planned") != 4 || m["dry_run"] != false {
		t.Fatalf("start = %v", m)
	}
	// The pass is held at its start, so this proves the tool did not wait.
	if a := out(t, f.s, "get_run_activity", map[string]any{}); a["status"] != "running" || num(a, "evaluation_id") != id {
		t.Errorf("while held: %v", a)
	}

	f.release()
	a := waitDone(t, f.s, id)
	if a["status"] != store.EvaluationDone || num(a, "completed") != 4 || num(a, "failed") != 0 ||
		num(a, "planned") != 4 || a["finished_at"] == "" {
		t.Errorf("after the run: %v", a)
	}
	if k := out(t, f.s, "get_overview_kpis", map[string]any{"days": 30}); num(k, "n") != 4 {
		t.Errorf("get_overview_kpis n = %d after the run, want the 4 new answers", num(k, "n"))
	}
	// And the prompt tool reports Cloud's field.
	m = out(t, f.s, "reevaluate_prompt", map[string]any{"prompt_id": f.prompts[0], "dry_run": false})
	if num(m, "runs_enqueued") != 2 {
		t.Errorf("reevaluate_prompt = %v", m)
	}
	waitDone(t, f.s, num(m, "evaluation_id"))
}

// TestOnlyOneRunAtATime: a second start is refused with the id of the run in
// flight, and the dry run says so too.
func TestOnlyOneRunAtATime(t *testing.T) {
	f := runSession(t, 100, true)
	id := num(out(t, f.s, "reevaluate_all_prompts", map[string]any{"dry_run": false}), "evaluation_id")

	text := refused(t, f.s, "reevaluate_prompt", map[string]any{"prompt_id": f.prompts[0], "dry_run": false})
	if !strings.Contains(text, fmt.Sprintf("run %d is already in progress", id)) {
		t.Errorf("second start: %s", text)
	}
	if m := out(t, f.s, "reevaluate_all_prompts", map[string]any{}); num(m, "running_evaluation_id") != id {
		t.Errorf("dry run while running = %v", m)
	}
	f.release()
	waitDone(t, f.s, id)
}

// TestARunInAnotherProcessRefusesThisOne: `limelit serve` and `limelit mcp`
// share the database but not the runner's lock. A running row with a fresh
// heartbeat, written by some other process, must still refuse a start.
func TestARunInAnotherProcessRefusesThisOne(t *testing.T) {
	f := runSession(t, 100, false)
	ctx := context.Background()
	other, err := f.db.ClaimEvaluation(ctx, 7) // what the other process wrote
	if err != nil {
		t.Fatal(err)
	}
	if f.run.Running() {
		t.Fatal("this process thinks it is running; the test must exercise the database guard")
	}
	text := refused(t, f.s, "reevaluate_all_prompts", map[string]any{"dry_run": false})
	if !strings.Contains(text, fmt.Sprintf("run %d is already in progress", other)) {
		t.Errorf("start beside another process's run: %s", text)
	}

	// Once that process finishes, this one can start.
	if err := f.db.FinishEvaluation(ctx, other, store.EvaluationDone); err != nil {
		t.Fatal(err)
	}
	waitDone(t, f.s, num(out(t, f.s, "reevaluate_all_prompts", map[string]any{"dry_run": false}), "evaluation_id"))
}

// TestARunWhoseProcessDiedReadsAsFailed: a running row with no heartbeat
// (its process quit) is reported failed, and does not block a new run.
func TestARunWhoseProcessDiedReadsAsFailed(t *testing.T) {
	f := runSession(t, 100, false)
	ctx := context.Background()
	dead, err := f.db.CreateEvaluation(ctx, 4) // never beats
	if err != nil {
		t.Fatal(err)
	}
	a := out(t, f.s, "get_run_activity", map[string]any{"evaluation_id": dead})
	if a["status"] != store.EvaluationFailed || !strings.Contains(fmt.Sprint(a["note"]), "stopped before it finished") {
		t.Errorf("a dead run = %v", a)
	}
	id := num(out(t, f.s, "reevaluate_all_prompts", map[string]any{"dry_run": false}), "evaluation_id")
	waitDone(t, f.s, id)
	if e, _ := f.db.Evaluation(ctx, dead); e.Status != store.EvaluationFailed {
		t.Errorf("the dead run's row is %q after a new claim, want failed", e.Status)
	}
}

// TestRunActivityBeforeAnyRun answers rather than erroring, and names an
// unknown id as unknown.
func TestRunActivityBeforeAnyRun(t *testing.T) {
	f := runSession(t, 100, false)
	if a := out(t, f.s, "get_run_activity", map[string]any{}); a["has_run"] != false {
		t.Errorf("before any run: %v", a)
	}
	if text := refused(t, f.s, "get_run_activity", map[string]any{"evaluation_id": 42}); !strings.Contains(text, "42") {
		t.Errorf("unknown id: %s", text)
	}
}
