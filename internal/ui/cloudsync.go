// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

// Sync to Limelit Cloud, in the Limelit Cloud key's card in Settings. It is
// opt-in: with it on, every run's answers are copied to the workspace the
// key belongs to, which is how ChatGPT gets to chart what this machine
// measures (ChatGPT cannot reach a self-hosted instance; it can reach the
// Limelit app at limelit.co/mcp/apps). The copying itself is upgrade.Sync,
// the same code the runner's hook calls after a pass.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/store"
	"github.com/limelit-co/open/internal/upgrade"
)

// uiSyncTimeout bounds a copy the Settings page waits for. Whatever does not
// make it goes with the next run.
const uiSyncTimeout = 10 * time.Minute

// cloudBrandMismatch says why a Limelit Cloud key must not take this
// instance's data: the account behind it already tracks a different brand,
// and Cloud imports into whichever account the key belongs to. outcome is
// what was therefore not done ("nothing was uploaded"). It returns "" when
// the brands match, when the account has no brand yet (one made just for the
// free key), and when Cloud cannot be asked: the import says so itself if
// the key is wrong.
func (a *App) cloudBrandMismatch(ctx context.Context, key, outcome string) string {
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	setup, err := provider.FetchLimelitSetup(fetchCtx, key)
	cancel()
	if err != nil || setup.Brand.Domain == "" {
		return ""
	}
	prop, err := a.db.Property(ctx)
	if err != nil || store.NormalizeDomain(setup.Brand.Domain) == store.NormalizeDomain(prop.Domain) {
		return ""
	}
	return fmt.Sprintf("That Limelit Cloud key belongs to %s (%s), which tracks a different brand, so %s. Create a key in the Cloud account for %s.",
		setup.Brand.Name, setup.Brand.Domain, outcome, prop.Domain)
}

// cloudSyncView reads the setting for the Limelit Cloud card.
func (a *App) cloudSyncView(ctx context.Context) CloudSyncView {
	v := CloudSyncView{Shown: true, KeySet: a.hasCloudKey(ctx)}
	st, err := upgrade.State(ctx, a.db)
	if err != nil {
		a.log.Error("reading the cloud sync setting", "error", err)
		return v
	}
	v.On = st.On
	v.Status, v.Problem = syncLines(st)
	v.RetryOff = !st.On && st.Last != nil && st.Last.Mode == upgrade.ModeSyncOff && !st.Last.OK
	return v
}

// syncLines says how the last attempt went: a status line, and the failure
// when there was one.
func syncLines(st upgrade.SyncState) (status, problem string) {
	last := st.Last
	if last == nil {
		if st.On {
			status = "Nothing copied yet. The first copy goes after the next run, or press Sync now."
		}
		return status, ""
	}
	if last.LastOKAt != "" {
		status = "Last copied " + shortStamp(last.LastOKAt) + "."
	}
	switch {
	case last.Mode == upgrade.ModeUpgrade:
		status = "Turned off by the move to Limelit Cloud on " + shortStamp(last.At) +
			". Limelit Cloud runs these prompts on its own schedule now."
	case !last.OK:
		problem = "Last attempt " + shortStamp(last.At) + ": " + last.Error
	case last.Mode == upgrade.ModeSyncOff:
		status = "Turned off " + shortStamp(last.At) + ". Limelit Cloud runs these prompts on its own schedule again."
	default:
		status = "Last copied " + shortStamp(last.At) + ": "
		if last.Chats == 0 && last.ChatsRederived == 0 {
			status += "nothing new."
		} else {
			status += fmt.Sprintf("%d new %s", last.Chats, plural(last.Chats, "answer", "answers"))
			if last.ChatsRederived > 0 {
				status += fmt.Sprintf(", %d already there brought up to date", last.ChatsRederived)
			}
			status += "."
		}
	}
	return status, problem
}

// copiedSentence says what one copy did.
func copiedSentence(res upgrade.SyncResult) string {
	if res.Chats == 0 && res.ChatsRederived == 0 {
		return "Limelit Cloud already has every answer measured here."
	}
	s := fmt.Sprintf("Copied %d new %s to Limelit Cloud", res.Chats, plural(res.Chats, "answer", "answers"))
	if res.ChatsRederived > 0 {
		s += fmt.Sprintf(", and brought %d already there up to date", res.ChatsRederived)
	}
	return s + "."
}

// cloudSyncOptions is where a copy goes: the key a run would use (the
// environment, then the one saved here) and the endpoint override.
func (a *App) cloudSyncOptions(ctx context.Context) upgrade.Options {
	return upgrade.Options{
		Key:      a.credentials(ctx)(provider.LimelitKeyEnv),
		Endpoint: os.Getenv(upgrade.EndpointEnv),
	}
}

func (a *App) syncOnce(ctx context.Context) (upgrade.SyncResult, error) {
	ctx, cancel := context.WithTimeout(ctx, uiSyncTimeout)
	defer cancel()
	return upgrade.Sync(ctx, a.db, a.cloudSyncOptions(ctx))
}

// setCloudSync handles POST /settings/cloud-sync, sync=on or sync=off.
//
// Turning it on checks the key's Cloud account tracks this brand, then makes
// the first copy while the user watches: another instance may already hold
// the workspace, and that is worth hearing now rather than after every run.
func (a *App) setCloudSync(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.FormValue("sync") != "on" {
		note := Flash{Kind: "ok", Text: "Sync is off. What was already copied stays in Limelit Cloud."}
		if err := upgrade.SyncOff(ctx, a.db, a.cloudSyncOptions(ctx)); err != nil {
			note = Flash{Kind: "warn", Text: err.Error()}
		}
		a.settingsWithCloudSync(w, r, note)
		return
	}

	key := a.credentials(ctx)(provider.LimelitKeyEnv)
	if key == "" {
		a.settingsWithCloudSync(w, r, Flash{Kind: "error", Text: "Save a Limelit Cloud key first: sync copies to the workspace that key belongs to."})
		return
	}
	if msg := a.cloudBrandMismatch(ctx, key, "sync was not turned on"); msg != "" {
		a.settingsWithCloudSync(w, r, Flash{Kind: "error", Text: msg})
		return
	}
	if err := upgrade.SetSyncOn(ctx, a.db, true); err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := a.syncOnce(ctx)
	var note Flash
	switch {
	case errors.Is(err, upgrade.ErrAnotherInstanceSyncing), errors.Is(err, upgrade.ErrKeyRejected):
		// Cloud took nothing and holds no slot for this instance. Left on,
		// every run would be refused the same way.
		if err := upgrade.SetSyncOn(ctx, a.db, false); err != nil {
			a.fail(w, r, err)
			return
		}
		note = Flash{Kind: "error", Text: err.Error() + " Sync was left off."}
	case err != nil:
		note = Flash{Kind: "warn", Text: "Sync is on, but the first copy did not go through. " + err.Error()}
	default:
		note = Flash{Kind: "ok", Text: "Sync is on. " + copiedSentence(res)}
	}
	a.settingsWithCloudSync(w, r, note)
}

// syncCloudNow handles POST /settings/cloud-sync/now: copy without waiting
// for the next run.
func (a *App) syncCloudNow(w http.ResponseWriter, r *http.Request) {
	res, err := a.syncOnce(r.Context())
	note := Flash{Kind: "ok", Text: copiedSentence(res)}
	switch {
	case errors.Is(err, upgrade.ErrSyncOff):
		note = Flash{Kind: "warn", Text: "Sync is off. Turn it on first."}
	case err != nil:
		note = Flash{Kind: "warn", Text: err.Error()}
	}
	a.settingsWithCloudSync(w, r, note)
}

// settingsWithCloudSync re-renders Settings with the outcome inside the
// sync block, where the button was pressed.
func (a *App) settingsWithCloudSync(w http.ResponseWriter, r *http.Request, note Flash) {
	a.settingsWith(w, r, func(p *SettingsPage) { p.CloudSync.Note = &note })
}
