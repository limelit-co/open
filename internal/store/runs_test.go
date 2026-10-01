// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// seeded gives a property, one prompt and one target, so a chat has
// something to hang off.
func seeded(t *testing.T) *DB {
	t.Helper()
	db := openTemp(t)
	ctx := context.Background()
	if err := db.SaveProperty(ctx, Property{Name: "Acme", Domain: "acme.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddPrompt(ctx, Prompt{Text: "p", Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddTarget(ctx, Target{Spec: "chatgpt:openai", Engine: "chatgpt", Provider: "openai", Access: "api"}); err != nil {
		t.Fatal(err)
	}
	return db
}
func TestRecordChatHonoursAnImportedTimestamp(t *testing.T) {
	db := seeded(t)
	ctx := context.Background()

	id, err := db.RecordChat(ctx, ChatRecord{
		PromptID: 1, TargetID: 1, Status: ChatOK, Text: "x", Calls: 1,
		CreatedAt: "2026-07-13 09:00:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	var created, usageDay string
	if err := db.QueryRowContext(ctx, `SELECT created_at FROM chat WHERE id = ?`, id).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created, "2026-07-13") {
		t.Errorf("created_at = %q, want the imported day", created)
	}
	if err := db.QueryRowContext(ctx, `SELECT day FROM usage_day WHERE target_id = 1`).Scan(&usageDay); err != nil {
		t.Fatal(err)
	}
	if usageDay != "2026-07-13" {
		t.Errorf("usage day = %q, want the imported day, not today", usageDay)
	}

	// And the runner's path, with no timestamp, still takes now.
	id2, err := db.RecordChat(ctx, ChatRecord{PromptID: 1, TargetID: 1, Status: ChatOK, Text: "y"})
	if err != nil {
		t.Fatal(err)
	}
	var created2 string
	db.QueryRowContext(ctx, `SELECT created_at FROM chat WHERE id = ?`, id2).Scan(&created2)
	if strings.HasPrefix(created2, "2026-07-13") {
		t.Error("an unset CreatedAt inherited the imported date")
	}
}

// age moves a pass's heartbeat into the past, as if its process had stopped
// beating that long ago.
func age(t *testing.T, db *DB, id int64, ago string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`UPDATE evaluation SET heartbeat_at = datetime('now', ?) WHERE id = ?`, ago, id); err != nil {
		t.Fatal(err)
	}
}

// TestSweepClosesOnlyPassesWhoseHeartbeatStopped. `limelit serve` sweeps at
// startup; a pass `limelit mcp` is running at that moment has a fresh
// heartbeat and must be left alone.
func TestSweepClosesOnlyPassesWhoseHeartbeatStopped(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	// A pass whose process stopped beating ten minutes ago, still marked running.
	res, err := db.ExecContext(ctx, `INSERT INTO evaluation (status, planned, heartbeat_at)
		VALUES (?, 3, datetime('now', '-10 minutes'))`, EvaluationRunning)
	if err != nil {
		t.Fatal(err)
	}
	stale, _ := res.LastInsertId()
	// A pass with no heartbeat at all: written before the column existed.
	legacy, err := db.CreateEvaluation(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	// A live pass, beating now.
	if _, err := db.ExecContext(ctx, `INSERT INTO evaluation (status, planned, heartbeat_at)
		VALUES (?, 4, datetime('now'))`, EvaluationRunning); err != nil {
		t.Fatal(err)
	}
	fresh, err := db.RunningEvaluation(ctx)
	if err != nil {
		t.Fatal(err)
	}

	n, err := db.SweepOrphanedEvaluations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("swept %d passes, want the stale one and the one with no heartbeat", n)
	}
	for _, id := range []int64{stale, legacy} {
		if e, _ := db.Evaluation(ctx, id); e.Status != EvaluationFailed || e.FinishedAt == "" {
			t.Errorf("pass %d after the sweep = %+v, want failed", id, e)
		}
	}
	if e, _ := db.Evaluation(ctx, fresh.ID); e.Status != EvaluationRunning || e.Stale {
		t.Errorf("live pass = %+v, want running and not stale", e)
	}
}

// TestClaimRefusesWhileAPassIsLive and names it; a finished or stale pass
// does not block, and a stale one is closed by the claim.
func TestClaimRefusesWhileAPassIsLive(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	live, err := db.ClaimEvaluation(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ClaimEvaluation(ctx, 2)
	var running *EvaluationRunningError
	if !errors.As(err, &running) || running.ID != live || !errors.Is(err, ErrEvaluationRunning) {
		t.Fatalf("second claim = %v, want refused naming %d", err, live)
	}
	if e, err := db.RunningEvaluation(ctx); err != nil || e.ID != live {
		t.Errorf("RunningEvaluation = %+v, %v", e, err)
	}

	age(t, db, live, "-10 minutes")
	if _, err := db.RunningEvaluation(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("a stale pass still reads as live: %v", err)
	}
	next, err := db.ClaimEvaluation(ctx, 2)
	if err != nil {
		t.Fatalf("a stale pass blocked the claim: %v", err)
	}
	if e, _ := db.Evaluation(ctx, live); e.Status != EvaluationFailed || e.FinishedAt == "" {
		t.Errorf("stale pass after the claim = %+v, want failed", e)
	}
	if err := db.FinishEvaluation(ctx, next, EvaluationDone); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimEvaluation(ctx, 2); err != nil {
		t.Errorf("a finished pass blocked the claim: %v", err)
	}
}

// TestRacingClaimsLetOneThrough: two processes pressing run at once must
// not both start.
func TestRacingClaimsLetOneThrough(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	won, refused := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := db.ClaimEvaluation(ctx, 1)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, ErrEvaluationRunning):
				refused++
			default:
				t.Errorf("claim: %v", err)
			}
		}()
	}
	wg.Wait()
	if won != 1 || refused != 7 {
		t.Errorf("%d claims won and %d were refused, want 1 and 7", won, refused)
	}
}

// TestHeartbeatKeepsAPassLive.
func TestHeartbeatKeepsAPassLive(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	id, err := db.ClaimEvaluation(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	age(t, db, id, "-10 minutes")
	if err := db.Heartbeat(ctx, id); err != nil {
		t.Fatal(err)
	}
	if e, _ := db.Evaluation(ctx, id); e.Stale {
		t.Error("a pass that just beat reads as stale")
	}
}
