// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Evaluation statuses.
const (
	EvaluationRunning   = "running"
	EvaluationDone      = "done"
	EvaluationFailed    = "failed"
	EvaluationCancelled = "cancelled"
)

// Chat statuses. NoAnswerSurface is not a miss for the brand: the surface
// itself did not render, so these rows stay out of every metric denominator.
const (
	ChatOK              = "ok"
	ChatFailed          = "failed"
	ChatNoAnswerSurface = "no_answer_surface"
)

// StaleAfter is how long a running pass may go without a heartbeat before
// it is taken to belong to a process that is gone. The runner beats far more
// often than this, so only a dead (or suspended) process misses it.
const StaleAfter = 2 * time.Minute

// ErrEvaluationRunning is returned by ClaimEvaluation while another pass is
// live. The concrete error is an *EvaluationRunningError carrying its id.
var ErrEvaluationRunning = errors.New("store: an evaluation is already running")

// EvaluationRunningError names the live pass that refused a claim.
type EvaluationRunningError struct{ ID int64 }

func (e *EvaluationRunningError) Error() string {
	return fmt.Sprintf("evaluation %d is already running", e.ID)
}

// Is makes errors.Is(err, ErrEvaluationRunning) hold.
func (e *EvaluationRunningError) Is(target error) bool { return target == ErrEvaluationRunning }

// Evaluation is one pass over prompts and targets.
type Evaluation struct {
	ID         int64
	Status     string
	Planned    int
	Completed  int
	Failed     int
	StartedAt  string
	FinishedAt string
	// NoAnswerSurface is how many of Completed found no answer surface at
	// all; they are kept out of every metric.
	NoAnswerSurface int
	// Stale is true for a running pass whose heartbeat has stopped: the
	// process that ran it is gone, and the next claim or sweep closes it.
	Stale bool
	// Error is why a failed pass stopped before fetching anything, or "".
	Error string
}

// Done reports whether this evaluation has stopped, whatever the outcome.
func (e Evaluation) Done() bool { return e.Status != EvaluationRunning }

// Mention is one brand found in one answer.
type Mention struct {
	// CompetitorID is nil for the property's own mentions.
	CompetitorID *int64
	BrandKey     string
	BrandName    string
	OffsetStart  int
	OffsetEnd    int
	// ListRank is the 1-based rank of the enclosing list item, nil when the
	// mention is not inside a ranked list.
	ListRank *int
}

// Citation is one classified source an answer cited.
type Citation struct {
	URL        string
	Host       string
	Site       string
	Title      string
	Position   int
	SourceType string
}

// ChatRecord is one answer and everything derived from it, written together.
type ChatRecord struct {
	EvaluationID int64
	PromptID     int64
	TargetID     int64
	Status       string
	Text         string
	Model        string
	Error        string
	InputTokens  int
	OutputTokens int
	Calls        int
	Mentions     []Mention
	Citations    []Citation
	// FanOut is the searches the engine ran while grounding, in its order.
	// Empty means the provider did not report any, which is not the same as
	// the engine having searched for nothing.
	FanOut []string
	// CreatedAt overrides the timestamp when set, in SQLite datetime form
	// (YYYY-MM-DD HH:MM:SS). The runner leaves it empty and the row takes
	// now; an import of history that happened on other days sets it, or
	// every imported answer would land on the day of the import and the
	// trend would be one tall bar.
	CreatedAt string
}

// CreateEvaluation opens a pass with no heartbeat: an import or a seed that
// writes its answers at once. The runner claims its passes with
// ClaimEvaluation instead, so that only one is live at a time.
func (db *DB) CreateEvaluation(ctx context.Context, planned int) (int64, error) {
	res, err := db.ExecContext(ctx,
		`INSERT INTO evaluation (status, planned) VALUES (?, ?)`, EvaluationRunning, planned)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ClaimEvaluation opens a pass unless another one is live, in this process
// or any other on the same file.
//
// Running passes whose heartbeat is stale are closed first, as failed: their
// process is gone and they would otherwise block every claim. The insert
// itself is one statement that checks for a live pass, so two processes
// claiming at once cannot both succeed. A refusal is an
// *EvaluationRunningError naming the live pass.
func (db *DB) ClaimEvaluation(ctx context.Context, planned int) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// A write first, so the transaction holds the write lock before it reads.
	if _, err := tx.ExecContext(ctx, `
		UPDATE evaluation SET status = ?, finished_at = datetime('now')
		WHERE status = ? AND (heartbeat_at IS NULL OR heartbeat_at < datetime('now', ?))`,
		EvaluationFailed, EvaluationRunning, staleCutoff()); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO evaluation (status, planned, heartbeat_at)
		SELECT ?, ?, datetime('now')
		WHERE NOT EXISTS (SELECT 1 FROM evaluation WHERE status = ?)`,
		EvaluationRunning, planned, EvaluationRunning)
	if err != nil {
		return 0, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return 0, err
	} else if n == 0 {
		var live int64
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM evaluation WHERE status = ? ORDER BY id DESC LIMIT 1`, EvaluationRunning).Scan(&live); err != nil {
			return 0, err
		}
		return 0, &EvaluationRunningError{ID: live}
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// FailEvaluation closes a pass that could not run, with the reason.
func (db *DB) FailEvaluation(ctx context.Context, id int64, reason string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE evaluation SET status = ?, finished_at = datetime('now'), error = ? WHERE id = ?`,
		EvaluationFailed, reason, id)
	return err
}

// Heartbeat marks a running pass as still live.
func (db *DB) Heartbeat(ctx context.Context, id int64) error {
	_, err := db.ExecContext(ctx,
		`UPDATE evaluation SET heartbeat_at = datetime('now') WHERE id = ? AND status = ?`, id, EvaluationRunning)
	return err
}

// RunningEvaluation reads the live pass, or ErrNotFound when none is.
func (db *DB) RunningEvaluation(ctx context.Context) (Evaluation, error) {
	return db.scanEvaluation(db.QueryRowContext(ctx, evaluationSelect+`
		WHERE e.status = ? AND e.heartbeat_at >= datetime('now', ?)
		ORDER BY e.id DESC LIMIT 1`, staleCutoff(), EvaluationRunning, staleCutoff()))
}

// staleCutoff is StaleAfter as a SQLite datetime modifier.
func staleCutoff() string { return fmt.Sprintf("-%d seconds", int(StaleAfter/time.Second)) }

// FinishEvaluation closes a pass.
func (db *DB) FinishEvaluation(ctx context.Context, id int64, status string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE evaluation SET status = ?, finished_at = datetime('now') WHERE id = ?`, status, id)
	return err
}

// evaluationSelect reads a pass with its no-answer-surface count and whether
// its heartbeat has stopped. Its first placeholder is the stale cutoff.
const evaluationSelect = `
	SELECT e.id, e.status, e.planned, e.completed, e.failed, e.started_at, COALESCE(e.finished_at, ''),
		(SELECT COUNT(*) FROM chat c WHERE c.evaluation_id = e.id AND c.status = '` + ChatNoAnswerSurface + `'),
		e.status = '` + EvaluationRunning + `' AND (e.heartbeat_at IS NULL OR e.heartbeat_at < datetime('now', ?)),
		COALESCE(e.error, '')
	FROM evaluation e`

// Evaluation reads one pass.
func (db *DB) Evaluation(ctx context.Context, id int64) (Evaluation, error) {
	return db.scanEvaluation(db.QueryRowContext(ctx, evaluationSelect+` WHERE e.id = ?`, staleCutoff(), id))
}

// LatestEvaluation reads the most recent pass, or ErrNotFound before any.
func (db *DB) LatestEvaluation(ctx context.Context) (Evaluation, error) {
	return db.scanEvaluation(db.QueryRowContext(ctx, evaluationSelect+` ORDER BY e.id DESC LIMIT 1`, staleCutoff()))
}

func (db *DB) scanEvaluation(row *sql.Row) (Evaluation, error) {
	var e Evaluation
	err := row.Scan(&e.ID, &e.Status, &e.Planned, &e.Completed, &e.Failed, &e.StartedAt, &e.FinishedAt,
		&e.NoAnswerSurface, &e.Stale, &e.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return Evaluation{}, ErrNotFound
	}
	return e, err
}

// SweepOrphanedEvaluations closes passes left running by a process that died.
//
// A running evaluation can only be advanced by the process that started it,
// so one whose process is gone will never finish. Left alone it would show
// as in flight forever. Only passes whose heartbeat is stale are closed: a
// fresh one belongs to another process on this file (`limelit mcp` beside
// `limelit serve`) that is still working on it. Chat rows are real and kept.
func (db *DB) SweepOrphanedEvaluations(ctx context.Context) (int, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE evaluation SET status = ?, finished_at = datetime('now')
		WHERE status = ? AND (heartbeat_at IS NULL OR heartbeat_at < datetime('now', ?))`,
		EvaluationFailed, EvaluationRunning, staleCutoff())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// RecordChat writes one answer, its mentions, its citations, its usage and
// the evaluation's counters in a single transaction.
//
// One transaction because a process killed between these writes would leave
// an answer with no mentions, which reads as a brand that was not talked
// about rather than as a run that did not finish. The counters live on the
// evaluation row rather than in memory for the same reason.
func (db *DB) RecordChat(ctx context.Context, rec ChatRecord) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO chat (evaluation_id, prompt_id, target_id, status, text, model, error, input_tokens, output_tokens, calls, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(?, datetime('now')))`,
		nullableID(rec.EvaluationID), rec.PromptID, rec.TargetID, rec.Status,
		rec.Text, rec.Model, rec.Error, rec.InputTokens, rec.OutputTokens, rec.Calls,
		nullIfBlank(rec.CreatedAt))
	if err != nil {
		return 0, fmt.Errorf("insert chat: %w", err)
	}
	chatID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	for _, m := range rec.Mentions {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO mention (chat_id, competitor_id, brand_key, brand_name, offset_start, offset_end, list_rank)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			chatID, m.CompetitorID, m.BrandKey, m.BrandName, m.OffsetStart, m.OffsetEnd, m.ListRank); err != nil {
			return 0, fmt.Errorf("insert mention: %w", err)
		}
	}
	for _, c := range rec.Citations {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO citation (chat_id, url, host, site, title, position, source_type)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			chatID, c.URL, c.Host, c.Site, c.Title, c.Position, c.SourceType); err != nil {
			return 0, fmt.Errorf("insert citation: %w", err)
		}
	}

	for i, q := range rec.FanOut {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO fanout (chat_id, query, position) VALUES (?, ?, ?)`,
			chatID, q, i+1); err != nil {
			return 0, fmt.Errorf("insert fanout: %w", err)
		}
	}

	// Usage is per target per day. Monthly totals are a query over this, so
	// the two grains cannot disagree.
	if rec.Calls > 0 || rec.InputTokens > 0 || rec.OutputTokens > 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_day (target_id, day, calls, input_tokens, output_tokens)
			VALUES (?, date(COALESCE(?, 'now')), ?, ?, ?)
			ON CONFLICT (target_id, day) DO UPDATE SET
				calls = calls + excluded.calls,
				input_tokens = input_tokens + excluded.input_tokens,
				output_tokens = output_tokens + excluded.output_tokens`,
			rec.TargetID, nullIfBlank(rec.CreatedAt), rec.Calls, rec.InputTokens, rec.OutputTokens); err != nil {
			return 0, fmt.Errorf("record usage: %w", err)
		}
	}

	if rec.EvaluationID > 0 {
		column := "completed"
		if rec.Status == ChatFailed {
			column = "failed"
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE evaluation SET `+column+` = `+column+` + 1 WHERE id = ?`, rec.EvaluationID); err != nil {
			return 0, fmt.Errorf("bump evaluation counter: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return chatID, nil
}

// UsageDay is one target's usage on one day.
type UsageDay struct {
	TargetID     int64
	TargetSpec   string
	Day          string
	Calls        int
	InputTokens  int
	OutputTokens int
}

// Usage returns per-target-per-day usage over a trailing window, newest first.
// There is no currency column anywhere: you reconcile these against your own
// provider bill.
func (db *DB) Usage(ctx context.Context, days int) ([]UsageDay, error) {
	if days <= 0 {
		days = 30
	}
	rows, err := db.QueryContext(ctx, `
		SELECT u.target_id, COALESCE(t.spec, ''), u.day, u.calls, u.input_tokens, u.output_tokens
		FROM usage_day u
		LEFT JOIN target t ON t.id = u.target_id
		WHERE u.day >= date('now', ?)
		ORDER BY u.day DESC, u.target_id`, fmt.Sprintf("-%d days", days-1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UsageDay
	for rows.Next() {
		var u UsageDay
		if err := rows.Scan(&u.TargetID, &u.TargetSpec, &u.Day, &u.Calls, &u.InputTokens, &u.OutputTokens); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func nullableID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

func nullIfBlank(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
