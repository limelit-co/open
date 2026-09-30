// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package runner executes tracked prompts against tracked targets.
//
// One pass is an evaluation: every active prompt, against every enabled
// target, once. The runner owns everything a provider deliberately does not:
// concurrency, the daily ceiling, what counts as a failure, and writing the
// result down.
//
// Two rules shape the whole thing. The ceiling is checked before any work
// starts, because a guard that stops halfway has already spent the money it
// was meant to protect. And every answer is written in one transaction with
// whatever was derived from it, because a process killed between those writes
// would leave an answer with no mentions, which reads as a brand nobody
// talked about rather than as a run that did not finish.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/store"
	"github.com/limelit-co/open/internal/target"
)

// ErrOverCeiling is returned when a pass would exceed limits.runs_per_day.
// Nothing is written when it is.
var ErrOverCeiling = errors.New("runner: over the daily ceiling")

// ErrNothingToRun is returned when there is no prompt or no target.
var ErrNothingToRun = errors.New("runner: nothing to run")

// ErrAlreadyRunning is returned when a pass is already in flight. Two
// concurrent passes would double every count they contribute to.
var ErrAlreadyRunning = errors.New("runner: an evaluation is already running")

// Analyzer turns one answer into the rows derived from it.
//
// It is an interface so mention matching and citation classification can land
// separately from this package, and so a test can assert the runner stores
// whatever the analyzer produced without reimplementing either.
type Analyzer interface {
	Analyze(text string, citations []provider.Citation) ([]store.Mention, []store.Citation)
}

// Event is one thing that happened during a pass, for the dashboard.
type Event struct {
	EvaluationID int64
	// Kind is "started", "chat", or "finished".
	Kind      string
	PromptID  int64
	TargetID  int64
	Prompt    string
	Target    string
	Status    string
	Error     string
	Completed int
	Failed    int
	Planned   int
}

// Runner executes evaluations.
type Runner struct {
	db       *store.DB
	registry *provider.Registry
	creds    provider.CredentialSource
	log      *slog.Logger

	// NewAnalyzer builds the analyzer for one pass, so it sees the property
	// and competitors as they are at the moment the pass starts rather than
	// as they were when the process booted. Nil means no derived rows.
	NewAnalyzer func(context.Context) (Analyzer, error)

	// PerProvider caps how many answers are in flight against one provider.
	// Low on purpose: these are somebody else's rate limits, and a self-host
	// default that trips them would look like a broken tool.
	PerProvider int

	mu       sync.Mutex
	running  bool
	subs     map[int]chan Event
	nextSub  int
	lastRunc int64
}

// New builds a Runner.
func New(db *store.DB, registry *provider.Registry, creds provider.CredentialSource, log *slog.Logger) *Runner {
	return &Runner{
		db:          db,
		registry:    registry,
		creds:       creds,
		log:         log,
		PerProvider: 2,
		subs:        make(map[int]chan Event),
	}
}

// Options narrows a pass.
type Options struct {
	// TargetSpec runs only this target. Empty means every enabled target.
	TargetSpec string
	// PromptID runs only this prompt. Zero means every active prompt.
	PromptID int64
	// RunsPerDay is the ceiling. Zero means no ceiling, which is only ever
	// right in a test.
	RunsPerDay int
}

// Result is what one pass did.
type Result struct {
	EvaluationID int64
	Planned      int
	Completed    int
	Failed       int
	Duration     time.Duration
}

type unit struct {
	prompt store.Prompt
	target store.Target
	parsed target.Target
}

// Run executes one pass and returns when it is done.
func (r *Runner) Run(ctx context.Context, opts Options) (Result, error) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return Result{}, ErrAlreadyRunning
	}
	r.running = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	units, err := r.plan(ctx, opts)
	if err != nil {
		return Result{}, err
	}

	// The ceiling is checked before anything runs. A guard that stops halfway
	// has already spent what it was meant to protect.
	if opts.RunsPerDay > 0 {
		today, err := r.db.RunsToday(ctx)
		if err != nil {
			return Result{}, err
		}
		if today+len(units) > opts.RunsPerDay {
			return Result{}, fmt.Errorf("%w: %d answers today plus %d planned would pass the ceiling of %d, so nothing was run",
				ErrOverCeiling, today, len(units), opts.RunsPerDay)
		}
	}

	started := time.Now()
	evalID, err := r.db.CreateEvaluation(ctx, len(units))
	if err != nil {
		return Result{}, err
	}
	r.publish(Event{EvaluationID: evalID, Kind: "started", Planned: len(units)})

	providers, err := r.providersFor(units)
	if err != nil {
		r.db.FinishEvaluation(ctx, evalID, store.EvaluationFailed)
		return Result{}, err
	}

	// Answers stored under older rules or brands are read again before new
	// ones join them, which also records what the new ones are read under.
	if _, _, err := r.ensureAnalyzed(ctx); err != nil && r.log != nil {
		r.log.Error("reading stored answers again before the pass", "error", err)
	}
	var analyzer Analyzer
	if r.NewAnalyzer != nil {
		if analyzer, err = r.NewAnalyzer(ctx); err != nil {
			r.db.FinishEvaluation(ctx, evalID, store.EvaluationFailed)
			return Result{}, err
		}
	}

	r.execute(ctx, evalID, units, providers, analyzer)

	status := store.EvaluationDone
	if ctx.Err() != nil {
		status = store.EvaluationCancelled
	}
	if err := r.db.FinishEvaluation(ctx, evalID, status); err != nil && ctx.Err() == nil {
		return Result{}, err
	}

	// Counters are read back rather than tallied in memory, so the number
	// reported is the number stored even if the process died mid-pass.
	final, err := r.db.Evaluation(context.WithoutCancel(ctx), evalID)
	if err != nil {
		return Result{}, err
	}
	// A brand or competitor edited while the pass ran could not re-read the
	// stored answers then (the lock was held), so it happens here.
	if _, _, err := r.ensureAnalyzed(context.WithoutCancel(ctx)); err != nil && r.log != nil {
		r.log.Error("reading stored answers again after the pass", "error", err)
	}
	r.publish(Event{
		EvaluationID: evalID, Kind: "finished", Status: status,
		Planned: final.Planned, Completed: final.Completed, Failed: final.Failed,
	})

	return Result{
		EvaluationID: evalID,
		Planned:      final.Planned,
		Completed:    final.Completed,
		Failed:       final.Failed,
		Duration:     time.Since(started),
	}, nil
}

// plan expands prompts by targets into the units of work.
func (r *Runner) plan(ctx context.Context, opts Options) ([]unit, error) {
	prompts, err := r.db.Prompts(ctx, false)
	if err != nil {
		return nil, err
	}
	targets, err := r.db.Targets(ctx, true)
	if err != nil {
		return nil, err
	}

	var units []unit
	for _, p := range prompts {
		if opts.PromptID > 0 && p.ID != opts.PromptID {
			continue
		}
		for _, t := range targets {
			if opts.TargetSpec != "" && t.Spec != opts.TargetSpec {
				continue
			}
			parsed, err := target.Parse(t.Spec)
			if err != nil {
				// A stored target that no longer parses is a configuration
				// problem, not a per-answer failure, so it is skipped loudly
				// rather than recorded as an answer nobody gave.
				r.log.Warn("skipping a stored target that no longer parses", "target", t.Spec, "error", err)
				continue
			}
			units = append(units, unit{prompt: p, target: t, parsed: parsed})
		}
	}
	if len(units) == 0 {
		return nil, fmt.Errorf("%w: %d active prompts and %d enabled targets", ErrNothingToRun, len(prompts), len(targets))
	}
	return units, nil
}

// providersFor constructs one provider per distinct provider name, so a pass
// builds each client once rather than per answer.
func (r *Runner) providersFor(units []unit) (map[string]provider.Provider, error) {
	out := map[string]provider.Provider{}
	for _, u := range units {
		name := u.parsed.Provider
		if _, ok := out[name]; ok {
			continue
		}
		p, err := r.registry.New(name, r.creds)
		if err != nil {
			// Naming the provider matters: with several configured, "invalid
			// key" alone does not say which one to go and fix.
			return nil, fmt.Errorf("provider %s: %w", name, err)
		}
		out[name] = p
	}
	return out, nil
}

// execute fans the units out, bounded per provider.
func (r *Runner) execute(ctx context.Context, evalID int64, units []unit, providers map[string]provider.Provider, analyzer Analyzer) {
	slots := map[string]chan struct{}{}
	for name := range providers {
		slots[name] = make(chan struct{}, r.perProvider())
	}

	var wg sync.WaitGroup
	for _, u := range units {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(u unit) {
			defer wg.Done()
			slot := slots[u.parsed.Provider]
			select {
			case slot <- struct{}{}:
				defer func() { <-slot }()
			case <-ctx.Done():
				return
			}
			r.runOne(ctx, evalID, u, providers[u.parsed.Provider], analyzer)
		}(u)
	}
	wg.Wait()
}

func (r *Runner) perProvider() int {
	if r.PerProvider > 0 {
		return r.PerProvider
	}
	return 2
}

// runOne asks one engine one prompt and writes the result down.
func (r *Runner) runOne(ctx context.Context, evalID int64, u unit, p provider.Provider, analyzer Analyzer) {
	req, err := u.parsed.Request(r.registry, u.prompt.Text, u.prompt.LocationCountry, "")
	if err != nil {
		r.record(ctx, evalID, u, store.ChatFailed, provider.Response{}, err, analyzer)
		return
	}

	resp, err := p.Run(ctx, req)
	switch {
	case err == nil:
		r.record(ctx, evalID, u, store.ChatOK, resp, nil, analyzer)
	case errors.Is(err, provider.ErrNoAnswerSurface):
		// Not a miss for the brand: the surface did not render at all, so
		// this row exists to be excluded from every denominator rather than
		// counted as an answer that ignored the brand.
		r.record(ctx, evalID, u, store.ChatNoAnswerSurface, provider.Response{}, err, analyzer)
	case ctx.Err() != nil:
		// A cancelled pass is not a provider failure, and recording it as one
		// would leave a permanent failed row for a run the user stopped.
		return
	default:
		r.record(ctx, evalID, u, store.ChatFailed, provider.Response{}, err, analyzer)
	}
}

func (r *Runner) record(ctx context.Context, evalID int64, u unit, status string, resp provider.Response, cause error, analyzer Analyzer) {
	rec := store.ChatRecord{
		EvaluationID: evalID,
		PromptID:     u.prompt.ID,
		TargetID:     u.target.ID,
		Status:       status,
		Text:         resp.Text,
		Model:        resp.Model,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		Calls:        resp.Calls,
	}
	if cause != nil {
		rec.Error = cause.Error()
	}
	if status == store.ChatOK && analyzer != nil {
		rec.Mentions, rec.Citations = analyzer.Analyze(resp.Text, resp.Citations)
		// Fan-out is kept even when the answer failed to mention anyone: what
		// the engine searched for is evidence in its own right, and it is the
		// only evidence available when the brand is absent.
		rec.FanOut = resp.FanOut
	}

	// The write outlives cancellation: an answer already paid for must not be
	// lost because the user pressed stop a moment later.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()

	if _, err := r.db.RecordChat(writeCtx, rec); err != nil {
		r.log.Error("could not record an answer", "prompt", u.prompt.ID, "target", u.target.Spec, "error", err)
		return
	}

	ev := Event{
		EvaluationID: evalID, Kind: "chat",
		PromptID: u.prompt.ID, TargetID: u.target.ID,
		Prompt: u.prompt.Text, Target: u.target.Spec, Status: status,
	}
	if cause != nil {
		ev.Error = cause.Error()
	}
	if e, err := r.db.Evaluation(writeCtx, evalID); err == nil {
		ev.Completed, ev.Failed, ev.Planned = e.Completed, e.Failed, e.Planned
	}
	r.publish(ev)
}

// Running reports whether a pass is in flight in this process.
func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// Subscribe returns a channel of events and a function to stop listening.
// The channel is buffered and drops rather than blocks: progress is a
// courtesy, and a slow reader must never hold up a paid-for answer.
func (r *Runner) Subscribe() (<-chan Event, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.nextSub
	r.nextSub++
	ch := make(chan Event, 64)
	r.subs[id] = ch
	return ch, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if c, ok := r.subs[id]; ok {
			delete(r.subs, id)
			close(c)
		}
	}
}

func (r *Runner) publish(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ch := range r.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
