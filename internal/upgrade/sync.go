// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package upgrade

// Sync: an instance that keeps measuring here and keeps a Limelit Cloud
// workspace current, so Cloud's charts, and the ChatGPT app that reads them,
// show what this machine measures. ChatGPT cannot reach a self-hosted
// instance; it can reach Cloud.
//
// It is opt-in, and it is the upgrade's import sent again after every run
// with mode "sync": the same payload, the same idempotency key, the same
// guarantee that a provider key never leaves. What it adds is a cursor, so a
// run sends its new answers rather than the whole history, and a signature,
// so that when this instance reads its stored answers again (a brand, a
// competitor or a matching rule changed) the mentions and citations Cloud
// already holds are brought in line too.
//
// The contract is docs/integrations/open-core-import.md in the Cloud
// repository, under "Sync".

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/limelit-co/open/internal/export"
	"github.com/limelit-co/open/internal/runner"
	"github.com/limelit-co/open/internal/store"
)

// Settings the sync keeps.
const (
	// SyncSetting is "on" while this instance syncs. Anything else is off.
	SyncSetting = "cloud_sync"
	// SyncSinceSetting is the cursor: the latest day of answers Cloud has
	// confirmed (YYYY-MM-DD). The next sync starts at that day, inclusive,
	// because a day is the export's granularity; Cloud skips the answers of
	// it that it already has.
	SyncSinceSetting = "cloud_sync_since"
	// SyncSignatureSetting is the analysis signature the answers in Cloud
	// were last sent under (runner.AnalysisSignatureSetting).
	SyncSignatureSetting = "cloud_sync_signature"
	// SyncLastSetting is the last attempt, as SyncStatus JSON.
	SyncLastSetting = "cloud_sync_last"
)

// ErrSyncOff is a sync asked for while sync is off.
var ErrSyncOff = errors.New("sync to Limelit Cloud is off")

// chatsPerRequest caps the answers one sync request carries. Big single
// writes have frozen Cloud's database before, so a first sync of a long
// history, or a full re-send, goes as a series of date windows instead.
// A variable so a test can make windows out of a dozen answers.
var chatsPerRequest = 2000

// syncRequestTimeout bounds one sync request when no client is injected.
const syncRequestTimeout = 10 * time.Minute

// syncSlot is held by whatever is talking to Cloud on this instance's
// behalf: a sync, turning sync off, or an upgrade. One at a time, so two
// syncs never send overlapping windows and none can turn the workspace's
// sync back on after an upgrade or a sync off.
var syncSlot = make(chan struct{}, 1)

func acquireSync(ctx context.Context) error {
	select {
	case syncSlot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("another upload to Limelit Cloud is still in progress: %w", ctx.Err())
	}
}

func releaseSync() { <-syncSlot }

// SyncStatus is the last attempt to talk to Cloud about sync.
type SyncStatus struct {
	// At is when it happened, UTC, "2006-01-02 15:04:05".
	At string `json:"at"`
	// Mode is ModeSync, ModeSyncOff, or ModeUpgrade when an upgrade turned
	// sync off.
	Mode string `json:"mode"`
	OK   bool   `json:"ok"`
	// Error is the failure in words a person can act on, "" when OK.
	Error string `json:"error,omitempty"`
	// Detail is the underlying error, for a bug report. Not for display:
	// it can quote a URL.
	Detail string `json:"detail,omitempty"`
	// LastOKAt is the last sync that went through, carried across failures.
	LastOKAt string `json:"last_ok_at,omitempty"`

	Requests       int  `json:"requests,omitempty"`
	Chats          int  `json:"chats,omitempty"`
	ChatsSkipped   int  `json:"chats_skipped,omitempty"`
	ChatsRederived int  `json:"chats_rederived,omitempty"`
	Resent         bool `json:"resent,omitempty"`
}

// SyncState is the sync setting as Settings shows it.
type SyncState struct {
	On bool
	// Since is the cursor, "" before the first sync.
	Since string
	// Last is the last attempt, nil when there has been none.
	Last *SyncStatus
}

// SyncResult is what one sync sent and what Cloud did with it.
type SyncResult struct {
	// Requests is how many windows went.
	Requests int
	// Resent is true for a full re-send: the first sync, or the first one
	// after the stored answers were read again.
	Resent                              bool
	Prompts, Chats, Mentions, Citations int
	ChatsSkipped, ChatsRederived        int
	WorkspaceURL                        string
}

func (r *SyncResult) add(res Result) {
	r.Requests++
	r.Prompts += res.Import.Prompts
	r.Chats += res.Import.Chats
	r.Mentions += res.Import.Mentions
	r.Citations += res.Import.Citations
	r.ChatsSkipped += res.Import.ChatsSkipped
	r.ChatsRederived += res.Import.ChatsRederived
	if res.WorkspaceURL != "" {
		r.WorkspaceURL = res.WorkspaceURL
	}
}

// SyncError is a failed sync: Error says it in words for Settings, Unwrap
// is the cause for a log.
type SyncError struct {
	Message string
	Err     error
}

func (e *SyncError) Error() string { return e.Message }
func (e *SyncError) Unwrap() error { return e.Err }

// SyncOn reports whether this instance syncs.
func SyncOn(ctx context.Context, db *store.DB) (bool, error) {
	v, err := db.Setting(ctx, SyncSetting)
	return v == "on", err
}

// SetSyncOn turns sync on or off here, without telling Cloud anything.
// Turning it off for good is SyncOff, which tells Cloud.
func SetSyncOn(ctx context.Context, db *store.DB, on bool) error {
	v := "off"
	if on {
		v = "on"
	}
	return db.SetSetting(ctx, SyncSetting, v)
}

// State reads the sync setting, its cursor and its last attempt.
func State(ctx context.Context, db *store.DB) (SyncState, error) {
	var st SyncState
	var err error
	if st.On, err = SyncOn(ctx, db); err != nil {
		return st, err
	}
	if st.Since, err = db.Setting(ctx, SyncSinceSetting); err != nil {
		return st, err
	}
	st.Last, err = lastStatus(ctx, db)
	return st, err
}

func lastStatus(ctx context.Context, db *store.DB) (*SyncStatus, error) {
	raw, err := db.Setting(ctx, SyncLastSetting)
	if err != nil || raw == "" {
		return nil, err
	}
	var st SyncStatus
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return nil, nil // an unreadable record is no record
	}
	return &st, nil
}

// record stores one attempt, carrying the last success forward.
func record(ctx context.Context, db *store.DB, st SyncStatus) error {
	st.At = time.Now().UTC().Format("2006-01-02 15:04:05")
	if st.OK && st.Mode == ModeSync {
		st.LastOKAt = st.At
	} else if prev, _ := lastStatus(ctx, db); prev != nil {
		st.LastOKAt = prev.LastOKAt
	}
	return db.SetSetting(ctx, SyncLastSetting, string(mustJSON(st)))
}

// failed records a failed attempt and returns its error.
func failed(ctx context.Context, db *store.DB, st SyncStatus, err *SyncError) error {
	st.OK, st.Error = false, err.Message
	if err.Err != nil {
		st.Detail = err.Err.Error()
	}
	_ = record(context.WithoutCancel(ctx), db, st)
	return err
}

// Sync copies what this instance measured since the last sync to the
// workspace opts.Key belongs to. It returns ErrSyncOff, and sends nothing,
// when sync is off.
//
// Answers go in date windows of at most about chatsPerRequest each, oldest
// first. The cursor advances only past a window Cloud answered 200, so a
// failure halfway is picked up from there by the next sync. When the stored
// answers have been read again since the last sync (the analysis signature
// changed), or there has never been one, everything is sent again with
// replace_derived, and the signature is recorded only once every window has
// gone through; a failure halfway starts that re-send over, which is safe
// because Cloud writes only what changed.
func Sync(ctx context.Context, db *store.DB, opts Options) (SyncResult, error) {
	if err := acquireSync(ctx); err != nil {
		return SyncResult{}, &SyncError{Message: "Another copy to Limelit Cloud is still in progress. Try again in a minute.", Err: err}
	}
	defer releaseSync()

	// Read under the slot: an upgrade or a sync off that held it may have
	// just turned sync off.
	if on, err := SyncOn(ctx, db); err != nil {
		return SyncResult{}, err
	} else if !on {
		return SyncResult{}, ErrSyncOff
	}

	st := SyncStatus{Mode: ModeSync}
	res, err := syncWindows(ctx, db, opts)
	st.Requests, st.Chats, st.ChatsSkipped, st.ChatsRederived, st.Resent =
		res.Requests, res.Chats, res.ChatsSkipped, res.ChatsRederived, res.Resent
	if errors.Is(err, ErrSyncOff) {
		return res, err
	}
	var serr *SyncError
	if err != nil && !errors.As(err, &serr) {
		serr = syncProblem(err)
	}
	if serr != nil {
		return res, failed(ctx, db, st, serr)
	}
	st.OK = true
	if err := record(ctx, db, st); err != nil {
		return res, err
	}
	return res, nil
}

// syncWindows does the sending for Sync, which holds the slot.
func syncWindows(ctx context.Context, db *store.DB, opts Options) (SyncResult, error) {
	var res SyncResult
	if strings.TrimSpace(opts.Key) == "" {
		return res, &SyncError{Message: "No Limelit Cloud key is saved here, so nothing was copied. Save one in Settings, or run limelit login."}
	}
	c, err := connect(opts, syncRequestTimeout)
	if err != nil {
		return res, err
	}
	instance, err := InstanceID(ctx, db)
	if err != nil {
		return res, err
	}

	// The signature is read once, before anything is exported. An edit made
	// while the windows go out changes the stored one, and the next sync
	// re-sends under it.
	signature, err := db.Setting(ctx, runner.AnalysisSignatureSetting)
	if err != nil {
		return res, err
	}
	sent, err := db.Setting(ctx, SyncSignatureSetting)
	if err != nil {
		return res, err
	}
	everSent, err := db.SettingUpdatedAt(ctx, SyncSignatureSetting)
	if err != nil {
		return res, err
	}
	since, err := db.Setting(ctx, SyncSinceSetting)
	if err != nil {
		return res, err
	}
	res.Resent = everSent == "" || sent != signature
	if res.Resent {
		since = ""
	}

	windows, err := planWindows(ctx, db, since, chatsPerRequest)
	if err != nil {
		return res, err
	}
	cursor := ""
	for i, w := range windows {
		// The first check was made under the slot. A sync turned off
		// between two windows stops; what went already stays in Cloud.
		if i > 0 {
			if on, err := SyncOn(ctx, db); err != nil {
				return res, err
			} else if !on {
				return res, ErrSyncOff
			}
		}
		r, err := push(ctx, db, c, instance, Options{
			Since: w.since, Until: w.until, Mode: ModeSync, ReplaceDerived: res.Resent,
		})
		if err != nil {
			return res, err
		}
		res.add(r)
		if w.last == "" {
			continue
		}
		cursor = w.last
		if !res.Resent {
			if err := db.SetSetting(ctx, SyncSinceSetting, cursor); err != nil {
				return res, err
			}
		}
	}
	if res.Resent {
		if cursor != "" {
			if err := db.SetSetting(ctx, SyncSinceSetting, cursor); err != nil {
				return res, err
			}
		}
		if err := db.SetSetting(ctx, SyncSignatureSetting, signature); err != nil {
			return res, err
		}
	}
	return res, nil
}

// window is one sync request's share of the history: answers created on or
// after since and before until ("" is open on that side).
type window struct {
	since, until string
	// last is the latest day with answers in the window, "" when none.
	last  string
	chats int
}

// planWindows splits the answers from since on into consecutive windows of
// at most max answers, by whole days. A single day over max goes alone,
// because a day is the export's granularity. There is always at least one
// window, so a run that stored nothing still sends the prompts' active flags
// and keeps the workspace's sync slot. The windows are contiguous and the
// last is open-ended, so an answer stored while they go out is in one.
func planWindows(ctx context.Context, db *store.DB, since string, max int) ([]window, error) {
	q := `SELECT date(created_at) AS day, COUNT(*) FROM chat`
	var args []any
	if since != "" {
		q += ` WHERE date(created_at) >= ?`
		args = append(args, since)
	}
	rows, err := db.QueryContext(ctx, q+` GROUP BY day ORDER BY day`, args...)
	if err != nil {
		return nil, fmt.Errorf("planning the sync: %w", err)
	}
	defer rows.Close()

	var out []window
	cur := window{since: since}
	for rows.Next() {
		var day sql.NullString
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			return nil, err
		}
		if !day.Valid {
			continue
		}
		if cur.chats > 0 && cur.chats+n > max {
			cur.until = day.String
			out = append(out, cur)
			cur = window{since: day.String}
		}
		cur.chats += n
		cur.last = day.String
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return append(out, cur), nil
}

// SyncOff turns sync off: here first, so nothing more leaves this machine
// whatever happens next, then on Cloud, which then schedules the prompts
// this instance brought on its own again. What was copied stays in Cloud.
//
// The payload carries no rows. When Cloud cannot be told, sync is still off
// here and the error says so; turning it off again retries.
func SyncOff(ctx context.Context, db *store.DB, opts Options) error {
	if err := SetSyncOn(ctx, db, false); err != nil {
		return err
	}
	// Wait out a sync in flight: it checks the setting before each window,
	// so it stops at the next one, and Cloud hears "off" after its last.
	if err := acquireSync(ctx); err != nil {
		return failed(ctx, db, SyncStatus{Mode: ModeSyncOff}, &SyncError{
			Message: "Sync is off here, but a copy to Limelit Cloud was still in progress, so Cloud was not told yet. Turn sync off again to tell it.",
			Err:     err,
		})
	}
	defer releaseSync()

	st := SyncStatus{Mode: ModeSyncOff}
	if strings.TrimSpace(opts.Key) == "" {
		return failed(ctx, db, st, &SyncError{Message: "Sync is off here. No Limelit Cloud key is saved, so Cloud was not told and still leaves these prompts to this machine. Save the key and turn sync off again to tell it."})
	}
	c, err := connect(opts, time.Minute)
	if err != nil {
		return err
	}
	instance, err := InstanceID(ctx, db)
	if err != nil {
		return err
	}
	body := mustJSON(struct {
		SchemaVersion  int    `json:"schema_version"`
		SourceInstance string `json:"source_instance"`
		Mode           string `json:"mode"`
	}{export.SchemaVersion, instance, ModeSyncOff})
	if _, err := post(ctx, c, body); err != nil {
		p := syncProblem(err)
		p.Message = "Sync is off here, but Limelit Cloud was not told, so it still leaves these prompts to this machine. Turn sync off again to tell it. " + p.Message
		return failed(ctx, db, st, p)
	}
	st.OK = true
	return record(ctx, db, st)
}

// endSyncAfterUpgrade turns sync off here once an upgrade went through.
// Cloud has already turned it off for this instance.
func endSyncAfterUpgrade(ctx context.Context, db *store.DB) error {
	on, err := SyncOn(ctx, db)
	if err != nil || !on {
		return err
	}
	if err := SetSyncOn(ctx, db, false); err != nil {
		return err
	}
	return record(ctx, db, SyncStatus{Mode: ModeUpgrade, OK: true})
}

// syncProblem says what a failed sync request means for the user's data,
// without quoting a URL or a flag that only the upgrade command has.
func syncProblem(err error) *SyncError {
	var ce *CloudError
	var unreachable *unreachableError
	var msg string
	switch {
	case errors.Is(err, ErrAnotherInstanceSyncing):
		msg = "Another Limelit Open instance already syncs into this Limelit Cloud workspace, so nothing was copied. " +
			"Only one can at a time: turn sync off in that one's Settings, or this one takes over once that one has gone 30 days without a sync."
	case errors.Is(err, ErrKeyRejected):
		msg = "Limelit Cloud did not accept the saved key, so nothing was copied. Make a new key at limelit.co and save it in Settings."
	case errors.As(err, &ce) && ce.Code == "unsupported_mode":
		msg = "This Limelit Cloud does not take syncs yet, so nothing was copied."
	case errors.As(err, &ce) && ce.Status == 413:
		msg = "One copy was too large for Limelit Cloud to take, so nothing in it was imported."
	case errors.As(err, &ce):
		msg = fmt.Sprintf("Limelit Cloud refused the copy (%d", ce.Status)
		if ce.Code != "" {
			msg += " " + ce.Code
		}
		msg += "), so nothing in it was imported. The next run tries again."
	case errors.As(err, &unreachable) && errors.Is(err, context.DeadlineExceeded):
		msg = "Limelit Cloud did not answer in time. Whatever was not copied goes with the next run."
	case errors.As(err, &unreachable):
		msg = "Could not reach Limelit Cloud. Whatever was not copied goes with the next run."
	default:
		msg = "The copy could not be made: " + err.Error()
	}
	return &SyncError{Message: msg, Err: err}
}
