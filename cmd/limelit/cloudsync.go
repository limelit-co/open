// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/limelit-co/open/internal/credentials"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/runner"
	"github.com/limelit-co/open/internal/secrets"
	"github.com/limelit-co/open/internal/store"
	"github.com/limelit-co/open/internal/upgrade"
)

// syncAfter is how a finished pass copies its answers to Limelit Cloud when
// the user turned sync on in Settings.
type syncAfter int

const (
	// syncInBackground is for serve and mcp: the process keeps running, so
	// the copy goes in its own goroutine and the pass never waits for it.
	syncInBackground syncAfter = iota
	// syncBeforeExit is for run: the process exits right after its pass,
	// which would kill a background copy, so the copy happens first.
	syncBeforeExit
)

const (
	// backgroundSyncTimeout bounds a copy after a pass in serve or mcp.
	backgroundSyncTimeout = 30 * time.Minute
	// runSyncTimeout bounds the copy `limelit run` waits for before exiting.
	// Whatever does not make it goes with the next run: the cursor only
	// moves past what Cloud confirmed.
	runSyncTimeout = 10 * time.Minute
)

// cloudSyncHook is the runner's OnFinished. It reads the setting when the
// pass ends, not when the process started, so turning sync on or off in
// Settings applies from the next pass. A copy that fails is logged and
// recorded for Settings; it never fails the pass.
func cloudSyncHook(db *store.DB, keys *secrets.Keyring, log *slog.Logger, mode syncAfter) func(context.Context, runner.Result) {
	copyNow := func(ctx context.Context) {
		opts := upgrade.Options{
			Key:      credentials.Source(ctx, db, keys, log)(provider.LimelitKeyEnv),
			Endpoint: os.Getenv(upgrade.EndpointEnv),
		}
		res, err := upgrade.Sync(ctx, db, opts)
		switch {
		case errors.Is(err, upgrade.ErrSyncOff):
		case err != nil:
			log.Warn("copying to limelit cloud did not go through; the next run tries again",
				"error", err, "cause", errors.Unwrap(err))
		default:
			log.Info("copied to limelit cloud", "requests", res.Requests, "answers_imported", res.Chats,
				"answers_already_there", res.ChatsSkipped, "answers_brought_up_to_date", res.ChatsRederived,
				"full_resend", res.Resent)
		}
	}
	return func(ctx context.Context, _ runner.Result) {
		if on, err := upgrade.SyncOn(context.WithoutCancel(ctx), db); err != nil || !on {
			return
		}
		if mode == syncBeforeExit {
			// The pass's own context: Ctrl-C stops the copy too, and the
			// next run sends what this one did not. A pass already stopped
			// that way copies nothing, rather than recording a failure.
			if ctx.Err() != nil {
				return
			}
			c, cancel := context.WithTimeout(ctx, runSyncTimeout)
			defer cancel()
			copyNow(c)
			return
		}
		go func() {
			c, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundSyncTimeout)
			defer cancel()
			copyNow(c)
		}()
	}
}
