// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mcpserver

// The evaluation tools: start a pass and watch it.
//
// The names and required arguments are Cloud's (reevaluate_prompt takes a
// prompt_id, reevaluate_all_prompts and get_run_activity take nothing), so a
// skill written against either server calls the same tools. What this server
// adds is optional: a target filter, an evaluation_id, and dry_run, which
// defaults to true. A pass spends the user's own provider money, so the
// default answer to "run it" is the plan, and the run is a second call the
// agent makes only after the user has seen that plan and said yes.
//
// Every pass goes through runner.Start, the entry point the dashboard and the
// scheduler use, so the daily ceiling and the one-pass-at-a-time guard hold
// across all of them, including across processes on the same database.

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/limelit-co/open/internal/runner"
	"github.com/limelit-co/open/internal/store"
)

type reevaluatePromptArgs struct {
	PromptID int64  `json:"prompt_id" jsonschema:"the prompt to run, an id from list_prompts"`
	Target   string `json:"target,omitempty" jsonschema:"open-core: run only this target, a spec from list_targets; omit for every enabled target"`
	DryRun   *bool  `json:"dry_run,omitempty" jsonschema:"default true: report the plan and run nothing. Pass false only after the user has seen the plan and confirmed"`
}

type reevaluateAllArgs struct {
	Target string `json:"target,omitempty" jsonschema:"open-core: run only this target, a spec from list_targets; omit for every enabled target"`
	DryRun *bool  `json:"dry_run,omitempty" jsonschema:"default true: report the plan and run nothing. Pass false only after the user has seen the plan and confirmed"`
}

// runOut is a plan (dry_run true) or a started pass (dry_run false). Counts
// only, never currency, as in get_usage: the bill is the user's provider's.
type runOut struct {
	DryRun        bool `json:"dry_run"`
	Prompts       int  `json:"prompts" jsonschema:"active prompts in the run"`
	Targets       int  `json:"targets" jsonschema:"enabled targets in the run"`
	Planned       int  `json:"planned" jsonschema:"answers the run fetches: one per prompt per target, each a call to the user's own provider"`
	RunsToday     int  `json:"runs_today" jsonschema:"answers already fetched today, which count against runs_per_day"`
	RunsPerDay    int  `json:"runs_per_day" jsonschema:"the daily ceiling set in Settings; a run that would pass it is refused before anything is spent"`
	WithinCeiling bool `json:"within_ceiling" jsonschema:"whether runs_today plus planned fits under runs_per_day"`
	// RunningEvaluationID is a pass already in flight; a start would be
	// refused until it is done.
	RunningEvaluationID int64 `json:"running_evaluation_id,omitempty" jsonschema:"a run already in progress; another cannot start until it is done"`

	EvaluationID int64  `json:"evaluation_id,omitempty" jsonschema:"the started run; pass it to get_run_activity"`
	Status       string `json:"status,omitempty" jsonschema:"running once started"`
	// Cloud's result fields, so a skill that reads them keeps working.
	RunsEnqueued int `json:"runs_enqueued,omitempty" jsonschema:"same as planned; Cloud's name for it on reevaluate_prompt"`
	RunsPlanned  int `json:"runs_planned,omitempty" jsonschema:"same as planned; Cloud's name for it on reevaluate_all_prompts"`

	NextStep string `json:"next_step"`
}

type runActivityArgs struct {
	EvaluationID int64 `json:"evaluation_id,omitempty" jsonschema:"open-core: the run to report, from reevaluate_*; omit for the latest"`
}

type runActivityOut struct {
	HasRun          bool   `json:"has_run" jsonschema:"false before any run"`
	EvaluationID    int64  `json:"evaluation_id,omitempty"`
	Status          string `json:"status,omitempty" jsonschema:"running, done, failed or cancelled"`
	Planned         int    `json:"planned"`
	Completed       int    `json:"completed" jsonschema:"answers recorded, including no_answer_surface ones"`
	Failed          int    `json:"failed" jsonschema:"calls that failed; the provider's error is on each chat"`
	NoAnswerSurface int    `json:"no_answer_surface" jsonschema:"of completed, answers where the surface did not render (for example no AI Overview); excluded from every metric"`
	StartedAt       string `json:"started_at,omitempty" jsonschema:"UTC"`
	FinishedAt      string `json:"finished_at,omitempty" jsonschema:"UTC, empty while running"`
	Error           string `json:"error,omitempty" jsonschema:"why a failed run stopped before fetching anything, such as a provider with no key"`
	Note            string `json:"note,omitempty"`
}

// stdioCaveat is said in both start tools' descriptions. Over stdio this
// server is a child of the desktop app, and so is any pass it starts.
const stdioCaveat = " A run started over stdio stops if the desktop app that launched this server quits; " +
	"get_run_activity then reports it failed, and the answers it already recorded are kept."

const confirmFirst = " Call with dry_run=true first (the default) and show the user the plan: answers planned, " +
	"and today's answers against the daily limit. Each answer is a call to the user's own provider. " +
	"Call again with dry_run=false only after the user confirms. Then poll get_run_activity until status is done."

func registerRuns(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "reevaluate_prompt",
		Description: "Evaluate one prompt again against every enabled target (or one, with target), " +
			"fetching a fresh answer from each engine." + confirmFirst + stdioCaveat,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in reevaluatePromptArgs) (*mcp.CallToolResult, runOut, error) {
		if in.PromptID <= 0 {
			return nil, runOut{}, errors.New("prompt_id is required: an id from list_prompts. To run every prompt, call reevaluate_all_prompts")
		}
		out, err := planOrStart(ctx, d, runner.Options{PromptID: in.PromptID, TargetSpec: in.Target}, in.DryRun)
		if err == nil && !out.DryRun {
			out.RunsEnqueued = out.Planned
		}
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "reevaluate_all_prompts",
		Description: "Evaluate every active prompt against every enabled target (or one, with target): " +
			"one fresh answer per prompt per engine, the same pass as Run now in the dashboard." + confirmFirst + stdioCaveat,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in reevaluateAllArgs) (*mcp.CallToolResult, runOut, error) {
		out, err := planOrStart(ctx, d, runner.Options{TargetSpec: in.Target}, in.DryRun)
		if err == nil && !out.DryRun {
			out.RunsPlanned = out.Planned
		}
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_run_activity",
		Description: "Status of the latest run (or the one named by evaluation_id): running, done, failed or " +
			"cancelled, with answers planned, completed and failed so far. Use it after reevaluate_prompt or " +
			"reevaluate_all_prompts, and when the user asks whether a run is still going or the numbers are current.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in runActivityArgs) (*mcp.CallToolResult, runActivityOut, error) {
		var e store.Evaluation
		var err error
		if in.EvaluationID > 0 {
			e, err = d.DB.Evaluation(ctx, in.EvaluationID)
		} else {
			e, err = d.DB.LatestEvaluation(ctx)
		}
		if errors.Is(err, store.ErrNotFound) {
			if in.EvaluationID > 0 {
				return nil, runActivityOut{}, fmt.Errorf("no run with evaluation_id %d", in.EvaluationID)
			}
			return nil, runActivityOut{Note: "Nothing has run yet. Start a run with reevaluate_all_prompts (dry_run first)."}, nil
		}
		if err != nil {
			return nil, runActivityOut{}, err
		}
		return nil, activityOut(e), nil
	})
}

// planOrStart works out the plan, and starts the pass unless this is a dry
// run. The plan is computed either way, so a started pass reports the same
// counts the dry run showed.
func planOrStart(ctx context.Context, d Deps, opts runner.Options, dryRun *bool) (runOut, error) {
	opts.RunsPerDay = d.RunsPerDay(ctx)
	plan, err := d.Runner.Plan(ctx, opts)
	if err != nil {
		return runOut{}, err
	}
	out := runOut{
		DryRun: dryRun == nil || *dryRun, Prompts: plan.Prompts, Targets: plan.Targets, Planned: plan.Planned,
		RunsToday: plan.RunsToday, RunsPerDay: plan.RunsPerDay, WithinCeiling: plan.WithinCeiling,
		RunningEvaluationID: plan.RunningEvaluationID,
	}
	if out.DryRun {
		switch {
		case plan.RunningEvaluationID > 0:
			out.NextStep = fmt.Sprintf("Run %d is still in progress, so a new one would be refused. "+
				"Poll get_run_activity with evaluation_id=%d until it is done.", plan.RunningEvaluationID, plan.RunningEvaluationID)
		case !plan.WithinCeiling:
			out.NextStep = fmt.Sprintf("This would pass the daily limit (%d today + %d planned > %d), so it would be refused. "+
				"Narrow it with prompt_id or target, wait until tomorrow (UTC), or raise the limit in Settings.",
				plan.RunsToday, plan.Planned, plan.RunsPerDay)
		default:
			out.NextStep = "Show the user this plan. If they confirm, call again with dry_run=false."
		}
		return out, nil
	}

	st, err := d.Runner.Start(ctx, opts)
	var live *runner.AlreadyRunningError
	switch {
	case errors.As(err, &live) && live.EvaluationID > 0:
		return runOut{}, fmt.Errorf("run %d is already in progress, and only one runs at a time. "+
			"Poll get_run_activity with evaluation_id=%d until it is done, then start again", live.EvaluationID, live.EvaluationID)
	case errors.Is(err, runner.ErrAlreadyRunning):
		return runOut{}, errors.New("a run is already starting, and only one runs at a time. Poll get_run_activity until it is done, then start again")
	case err != nil:
		return runOut{}, err
	}
	out.EvaluationID, out.Status, out.Planned = st.EvaluationID, store.EvaluationRunning, st.Planned
	out.RunningEvaluationID = 0
	out.NextStep = fmt.Sprintf("Started. Poll get_run_activity with evaluation_id=%d until status is done, then report the new numbers.", st.EvaluationID)
	return out, nil
}

// activityOut reports a pass. One whose heartbeat stopped is reported as
// failed: the process running it is gone and nothing will finish it, and the
// next start or sweep records it so.
func activityOut(e store.Evaluation) runActivityOut {
	out := runActivityOut{
		HasRun: true, EvaluationID: e.ID, Status: e.Status,
		Planned: e.Planned, Completed: e.Completed, Failed: e.Failed, NoAnswerSurface: e.NoAnswerSurface,
		StartedAt: e.StartedAt, FinishedAt: e.FinishedAt, Error: e.Error,
	}
	switch {
	case e.Error != "":
		out.Note = "The run stopped before fetching anything; error says why. Keys and targets are fixed in the " +
			"dashboard's Settings (never ask for a key in chat), then start the run again."
	case e.Stale:
		out.Status = store.EvaluationFailed
		out.Note = "The process running this stopped before it finished, for example because the desktop app that " +
			"started it quit. The answers it recorded are kept; start a new run for the rest."
	case e.Status == store.EvaluationRunning:
		out.Note = fmt.Sprintf("Still running: %d of %d answers so far. Poll again in a little while.",
			e.Completed+e.Failed, e.Planned)
	case e.Status == store.EvaluationDone && e.Failed > 0:
		out.Note = fmt.Sprintf("%d of %d calls failed; list_chats with show=failed has the provider's errors.", e.Failed, e.Planned)
	}
	return out
}
