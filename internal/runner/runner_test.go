// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package runner

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/store"
)

func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "limelit.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// seed puts a property, prompts and one stub target in place, and returns the
// runner and the stub behind it.
func seed(t *testing.T, prompts int, cfg provider.StubConfig) (*Runner, *store.DB, *provider.Stub) {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.SaveProperty(ctx, store.Property{Name: "Acme", Domain: "acme.com"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < prompts; i++ {
		if _, err := db.AddPrompt(ctx, store.Prompt{Text: promptText(i), Active: true}); err != nil {
			t.Fatal(err)
		}
	}

	reg := provider.NewRegistry()
	stub := provider.RegisterStub(reg, cfg)
	if _, err := db.AddTarget(ctx, store.Target{
		Spec: "chatgpt:stub", Engine: "chatgpt", Provider: provider.StubName, Access: "api",
	}); err != nil {
		t.Fatal(err)
	}

	return New(db, reg, provider.StaticCredentials(nil), quietLog()), db, stub
}

func promptText(i int) string {
	return "prompt " + string(rune('a'+i))
}

func TestRunProducesOneChatPerPromptAndTarget(t *testing.T) {
	r, db, stub := seed(t, 4, provider.StubConfig{Answer: "Acme leads."})
	ctx := context.Background()

	// A second target on the same engine, which must produce its own rows
	// rather than collapsing into the first.
	if _, err := db.AddTarget(ctx, store.Target{
		Spec: "chatgpt:stub:online", Engine: "chatgpt", Provider: provider.StubName, Online: true, Access: "api",
	}); err != nil {
		t.Fatal(err)
	}

	res, err := r.Run(ctx, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Planned != 8 || res.Completed != 8 || res.Failed != 0 {
		t.Errorf("result = %+v, want 4 prompts by 2 targets", res)
	}
	if stub.Calls() != 8 {
		t.Errorf("the provider was asked %d times, want exactly once per unit", stub.Calls())
	}

	counts, _ := db.Counts(ctx)
	if counts.Chats != 8 {
		t.Errorf("stored %d chats", counts.Chats)
	}
}

func TestCeilingRefusesBeforeAnythingIsSpent(t *testing.T) {
	// A guard that stops halfway has already spent what it was meant to
	// protect, so nothing at all may be written.
	r, db, stub := seed(t, 5, provider.StubConfig{})
	_, err := r.Run(context.Background(), Options{RunsPerDay: 3})
	if !errors.Is(err, ErrOverCeiling) {
		t.Fatalf("error = %v, want ErrOverCeiling", err)
	}
	if !strings.Contains(err.Error(), "ceiling of 3") {
		t.Errorf("the error does not say what the ceiling is: %v", err)
	}
	if stub.Calls() != 0 {
		t.Errorf("the provider was called %d times despite the refusal", stub.Calls())
	}
	counts, _ := db.Counts(context.Background())
	if counts.Chats != 0 {
		t.Errorf("wrote %d chats despite the refusal", counts.Chats)
	}
	if _, err := db.LatestEvaluation(context.Background()); err != store.ErrNotFound {
		t.Error("an evaluation row was opened for a refused pass")
	}
}

func TestCeilingCountsWhatAlreadyRanToday(t *testing.T) {
	r, _, _ := seed(t, 2, provider.StubConfig{})
	ctx := context.Background()

	if _, err := r.Run(ctx, Options{RunsPerDay: 3}); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	// Two answers are already on the clock, so a second pass of two would
	// reach four against a ceiling of three.
	if _, err := r.Run(ctx, Options{RunsPerDay: 3}); !errors.Is(err, ErrOverCeiling) {
		t.Errorf("second pass = %v, want ErrOverCeiling", err)
	}
}

func TestFailuresAreRecordedNotDropped(t *testing.T) {
	// A failed answer is evidence about the run, not about the brand, and
	// losing it would make a broken key look like a quiet week.
	r, db, _ := seed(t, 3, provider.StubConfig{Err: provider.ErrRateLimited})
	res, err := r.Run(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failed != 3 || res.Completed != 0 {
		t.Errorf("result = %+v, want three failures", res)
	}
	counts, _ := db.Counts(context.Background())
	if counts.Chats != 3 {
		t.Errorf("stored %d chats, want the failures kept", counts.Chats)
	}
}

func TestNoAnswerSurfaceIsItsOwnStatus(t *testing.T) {
	// A Google surface that did not render says nothing about the brand, so
	// it must not be stored as a failure or as an answer.
	r, db, _ := seed(t, 2, provider.StubConfig{Err: provider.ErrNoAnswerSurface})
	if _, err := r.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chat WHERE status = ?`, store.ChatNoAnswerSurface).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("%d rows have the no_answer_surface status, want 2", n)
	}
}

func TestUsageIsRecordedPerTargetPerDay(t *testing.T) {
	r, db, _ := seed(t, 3, provider.StubConfig{Answer: "one two three four five"})
	if _, err := r.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	usage, err := db.Usage(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 {
		t.Fatalf("usage rows = %d, want one target on one day", len(usage))
	}
	if usage[0].Calls != 3 {
		t.Errorf("calls = %d, want one per answer", usage[0].Calls)
	}
	if usage[0].OutputTokens == 0 {
		t.Error("no tokens were recorded")
	}
	if usage[0].TargetSpec != "chatgpt:stub" {
		t.Errorf("usage is not attributed to the target: %q", usage[0].TargetSpec)
	}
}

type countingAnalyzer struct {
	mu   sync.Mutex
	seen []string
}

func (c *countingAnalyzer) Analyze(text string, citations []provider.Citation) ([]store.Mention, []store.Citation) {
	c.mu.Lock()
	c.seen = append(c.seen, text)
	c.mu.Unlock()
	return []store.Mention{{BrandKey: "acme", BrandName: "Acme", OffsetStart: 0, OffsetEnd: 4}},
		[]store.Citation{{URL: "https://acme.com/x", Host: "acme.com", Site: "acme.com", Position: 1, SourceType: "own"}}
}

func TestAnalyzerOutputIsStoredWithTheAnswer(t *testing.T) {
	// The answer and everything derived from it land in one transaction, so a
	// kill between them cannot leave an answer that looks unmentioned.
	r, db, _ := seed(t, 2, provider.StubConfig{Answer: "Acme is good"})
	analyzer := &countingAnalyzer{}
	r.NewAnalyzer = func(context.Context) (Analyzer, error) { return analyzer, nil }

	if _, err := r.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"mention": 2, "citation": 2} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%s rows = %d, want %d", table, n, want)
		}
	}
	if len(analyzer.seen) != 2 {
		t.Errorf("the analyzer saw %d answers", len(analyzer.seen))
	}
}

func TestAnalyzerIsNotRunOnAFailure(t *testing.T) {
	// There is no text to read, and inventing mentions for a failed call
	// would put a brand in an answer nobody gave.
	r, db, _ := seed(t, 2, provider.StubConfig{Err: provider.ErrAuth})
	r.NewAnalyzer = func(context.Context) (Analyzer, error) { return &countingAnalyzer{}, nil }
	if _, err := r.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM mention`).Scan(&n)
	if n != 0 {
		t.Errorf("%d mentions were derived from failed answers", n)
	}
}

func TestNothingToRun(t *testing.T) {
	db := newTestDB(t)
	reg := provider.NewRegistry()
	provider.RegisterStub(reg, provider.StubConfig{})
	r := New(db, reg, provider.StaticCredentials(nil), quietLog())

	if _, err := r.Run(context.Background(), Options{}); !errors.Is(err, ErrNothingToRun) {
		t.Errorf("error = %v, want ErrNothingToRun", err)
	}
}

func TestMissingCredentialNamesTheProvider(t *testing.T) {
	// With several providers configured, "invalid key" alone does not say
	// which one to go and fix.
	db := newTestDB(t)
	ctx := context.Background()
	db.AddPrompt(ctx, store.Prompt{Text: "q", Active: true})
	db.AddTarget(ctx, store.Target{Spec: "chatgpt:openai", Engine: "chatgpt", Provider: "openai", Access: "api"})

	r := New(db, provider.Default(), provider.StaticCredentials(nil), quietLog())
	_, err := r.Run(ctx, Options{})
	if err == nil || !strings.Contains(err.Error(), "openai") {
		t.Errorf("error = %v, want the provider named", err)
	}
	counts, _ := db.Counts(ctx)
	if counts.Chats != 0 {
		t.Errorf("wrote %d chats despite having no key", counts.Chats)
	}
}

func TestOnlyOnePassAtATime(t *testing.T) {
	// Two concurrent passes would double every count they contribute to.
	r, _, _ := seed(t, 2, provider.StubConfig{})
	release := make(chan struct{})
	r.NewAnalyzer = func(context.Context) (Analyzer, error) {
		<-release
		return nil, nil
	}

	var first error
	done := make(chan struct{})
	go func() {
		_, first = r.Run(context.Background(), Options{})
		close(done)
	}()

	waitFor(t, r.Running)
	if _, err := r.Run(context.Background(), Options{}); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("the second pass returned %v, want ErrAlreadyRunning", err)
	}
	close(release)
	<-done
	if first != nil {
		t.Fatalf("the first pass failed: %v", first)
	}
}

func TestTargetFilter(t *testing.T) {
	r, db, _ := seed(t, 2, provider.StubConfig{})
	ctx := context.Background()
	db.AddTarget(ctx, store.Target{Spec: "chatgpt:stub:online", Engine: "chatgpt", Provider: provider.StubName, Online: true, Access: "api"})

	res, err := r.Run(ctx, Options{TargetSpec: "chatgpt:stub:online"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Planned != 2 {
		t.Errorf("planned = %d, want only the named target", res.Planned)
	}
}

func TestEventsReportProgress(t *testing.T) {
	r, _, _ := seed(t, 3, provider.StubConfig{})
	events, stop := r.Subscribe()
	defer stop()

	if _, err := r.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}

	kinds := map[string]int{}
	for len(events) > 0 {
		kinds[(<-events).Kind]++
	}
	if kinds["started"] != 1 || kinds["finished"] != 1 || kinds["chat"] != 3 {
		t.Errorf("events = %v, want one start, three answers and one finish", kinds)
	}
}

func TestSweepClosesEvaluationsLeftByADeadProcess(t *testing.T) {
	// A running evaluation can only be advanced by the process that started
	// it, so one that survives a restart would show as in flight forever.
	db := newTestDB(t)
	ctx := context.Background()
	id, err := db.CreateEvaluation(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	n, err := db.SweepOrphanedEvaluations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("swept %d evaluations", n)
	}
	got, _ := db.Evaluation(ctx, id)
	if got.Status != store.EvaluationFailed || got.FinishedAt == "" {
		t.Errorf("evaluation after the sweep = %+v", got)
	}
}

func TestCancelledPassKeepsWhatItAlreadyPaidFor(t *testing.T) {
	// An answer already fetched must not be thrown away because the user
	// pressed stop a moment later.
	r, db, _ := seed(t, 6, provider.StubConfig{})
	r.PerProvider = 1
	ctx, cancel := context.WithCancel(context.Background())

	events, stop := r.Subscribe()
	defer stop()
	go func() {
		for ev := range events {
			if ev.Kind == "chat" && ev.Completed >= 2 {
				cancel()
				return
			}
		}
	}()

	if _, err := r.Run(ctx, Options{}); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	counts, _ := db.Counts(context.Background())
	if counts.Chats == 0 {
		t.Error("a cancelled pass lost every answer it had already fetched")
	}
	if counts.Chats == 6 {
		t.Error("cancellation did not stop the pass")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

func TestStartReturnsOnceThePassIsClaimed(t *testing.T) {
	// The dashboard, the scheduler and the MCP tools hand out the id at once
	// and let the pass run on.
	r, db, _ := seed(t, 2, provider.StubConfig{})
	gate := make(chan struct{})
	r.NewAnalyzer = func(context.Context) (Analyzer, error) {
		<-gate
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	st, err := r.Start(ctx, Options{})
	if err != nil {
		t.Fatal(err)
	}
	cancel() // the caller going away must not abandon the pass
	if st.EvaluationID == 0 || st.Planned != 2 {
		t.Fatalf("started = %+v", st)
	}
	if e, _ := db.Evaluation(context.Background(), st.EvaluationID); e.Status != store.EvaluationRunning || e.Stale {
		t.Errorf("while held: %+v", e)
	}
	close(gate)
	res, err := st.Wait()
	if err != nil || res.Completed != 2 {
		t.Fatalf("wait = %+v, %v", res, err)
	}
	if e, _ := db.Evaluation(context.Background(), st.EvaluationID); e.Status != store.EvaluationDone {
		t.Errorf("after the pass: %q, want done even though the caller's context ended", e.Status)
	}
}

func TestStartRefusesWhileAnotherProcessRuns(t *testing.T) {
	// The runner's lock is per process; the claim is what another process
	// on the same file sees.
	r, db, _ := seed(t, 1, provider.StubConfig{})
	other, err := db.ClaimEvaluation(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Start(context.Background(), Options{})
	var running *AlreadyRunningError
	if !errors.As(err, &running) || running.EvaluationID != other || !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("start = %v, want refused naming evaluation %d", err, other)
	}
	if r.Running() {
		t.Error("a refused start left the lock held")
	}
}

func TestPlanCountsWithoutClaiming(t *testing.T) {
	r, db, _ := seed(t, 3, provider.StubConfig{})
	p, err := r.Plan(context.Background(), Options{RunsPerDay: 2})
	if err != nil {
		t.Fatal(err)
	}
	if p.Prompts != 3 || p.Targets != 1 || p.Planned != 3 || p.RunsPerDay != 2 || p.WithinCeiling {
		t.Errorf("plan = %+v", p)
	}
	if _, err := db.LatestEvaluation(context.Background()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a plan wrote an evaluation: %v", err)
	}
}

func TestUnknownPromptOrTargetIsNamed(t *testing.T) {
	r, _, _ := seed(t, 1, provider.StubConfig{})
	if _, err := r.Run(context.Background(), Options{PromptID: 999}); !errors.Is(err, ErrUnknownPrompt) {
		t.Errorf("unknown prompt: %v", err)
	}
	if _, err := r.Run(context.Background(), Options{TargetSpec: "gemini:stub"}); !errors.Is(err, ErrUnknownTarget) {
		t.Errorf("unknown target: %v", err)
	}
}
