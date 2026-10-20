// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/limelit-co/open/internal/provider/providertest"
	"github.com/limelit-co/open/internal/store"
	"github.com/limelit-co/open/internal/upgrade"
)

// fakeImport stands in for POST /v1/import/open and keeps each payload's
// mode. status is what it answers; 200 by default.
type fakeImport struct {
	mu     sync.Mutex
	modes  []string
	status int
	reply  string
}

func newFakeImport(t *testing.T) *fakeImport {
	t.Helper()
	f := &fakeImport{status: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		mode, _ := doc["mode"].(string)
		f.mu.Lock()
		f.modes = append(f.modes, mode)
		status, reply := f.status, f.reply
		f.mu.Unlock()
		if reply == "" {
			chats, _ := doc["chats"].([]any)
			b, _ := json.Marshal(map[string]any{"ok": true, "import": map[string]any{"import_id": "imp", "chats_imported": len(chats)}})
			reply = string(b)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(upgrade.EndpointEnv, srv.URL)
	return f
}

func (f *fakeImport) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.modes...)
}

// syncApp is a configured acme.com instance with one answer, a Limelit
// Cloud account behind the key that tracks cloudDomain, and a fake import.
func syncApp(t *testing.T, cloudDomain string) (*store.DB, http.Handler, *fakeImport) {
	t.Helper()
	fakeCloudSetup(t, cloudDomain)
	imports := newFakeImport(t)
	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	post(t, h, "/settings/targets/track", url.Values{"engine": {"chatgpt"}, "provider": {"openrouter"}})
	targets, _ := db.Targets(context.Background(), false)
	if _, err := db.RecordChat(context.Background(), store.ChatRecord{TargetID: targets[0].ID, PromptID: seedPrompt(t, db), Status: "ok", Text: "Acme leads."}); err != nil {
		t.Fatal(err)
	}
	return db, h, imports
}

func syncIsOn(t *testing.T, db *store.DB) bool {
	t.Helper()
	on, err := upgrade.SyncOn(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return on
}

// TestCloudSyncToggle. On copies at once and says what went; Sync now copies
// again; off tells Cloud with the sync_off payload and says the copies stay.
func TestCloudSyncToggle(t *testing.T) {
	db, h, imports := syncApp(t, "acme.com")

	page := get(t, h, "/settings").Body.String()
	block := between(page, `id="cloud-sync"`, "<h2>Schedule</h2>")
	for _, want := range []string{"Sync to Limelit Cloud", "Turn sync on", "https://limelit.co/mcp/apps", "Provider keys never leave this machine", "stays in Limelit Cloud", "ChatGPT"} {
		if !strings.Contains(block, want) {
			t.Errorf("the sync setting does not say %q", want)
		}
	}
	// The owner's copy rules, on everything the setting renders.
	if bad := regexp.MustCompile(`(?i)\b(api|scraped)\b|—|&mdash;|\$|€|£|pric`); bad.MatchString(block) {
		t.Errorf("the sync setting breaks the copy rules: %q", bad.FindString(block))
	}
	if strings.Count(page, `name="cred_LIMELIT_CLOUD_KEY"`) != 1 {
		t.Error("the sync setting added a second Cloud key field")
	}

	body := post(t, h, "/settings/cloud-sync", url.Values{"sync": {"on"}}).Body.String()
	if !strings.Contains(body, "Sync is on. Copied 1 new answer to Limelit Cloud.") {
		t.Errorf("turning sync on said %s", flashOf(between(body, `id="cloud-sync"`, "btn-row")))
	}
	if !syncIsOn(t, db) {
		t.Fatal("sync is not on")
	}
	if got := strings.Join(imports.sent(), ","); got != "sync" {
		t.Errorf("turning sync on sent %q, want one sync", got)
	}
	if !strings.Contains(body, "Turn sync off") || !strings.Contains(body, "Sync now") || !strings.Contains(body, "Last copied") {
		t.Error("with sync on the card does not offer Sync now and Turn sync off, or say when it last copied")
	}

	body = post(t, h, "/settings/cloud-sync/now", nil).Body.String()
	if !strings.Contains(body, "Limelit Cloud already has every answer measured here.") && !strings.Contains(body, "Copied") {
		t.Errorf("Sync now said %s", flashOf(between(body, `id="cloud-sync"`, "btn-row")))
	}
	if n := len(imports.sent()); n != 2 {
		t.Errorf("Sync now sent nothing: %d requests", n)
	}

	body = post(t, h, "/settings/cloud-sync", url.Values{"sync": {"off"}}).Body.String()
	if !strings.Contains(body, "Sync is off. What was already copied stays in Limelit Cloud.") {
		t.Errorf("turning sync off said %s", flashOf(between(body, `id="cloud-sync"`, "btn-row")))
	}
	if syncIsOn(t, db) {
		t.Error("sync is still on")
	}
	if got := imports.sent(); got[len(got)-1] != "sync_off" {
		t.Errorf("turning sync off sent %v, want sync_off last", got)
	}
}

// TestCloudSyncRefusesAKeyForAnotherBrand. Cloud imports into whichever
// account the key belongs to; one that tracks a different brand would mix
// this brand's answers into that brand's charts.
func TestCloudSyncRefusesAKeyForAnotherBrand(t *testing.T) {
	db, h, imports := syncApp(t, "otherbrand.com")

	body := post(t, h, "/settings/cloud-sync", url.Values{"sync": {"on"}}).Body.String()
	for _, want := range []string{"belongs to", "otherbrand.com", "a different brand", "sync was not turned on", "acme.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not say %q: %s", want, flashOf(between(body, `id="cloud-sync"`, "btn-row")))
		}
	}
	if syncIsOn(t, db) {
		t.Error("sync was turned on for another brand's account")
	}
	if n := len(imports.sent()); n != 0 {
		t.Errorf("%d imports went to another brand's account", n)
	}

	// The upgrade page refuses the same key with the same check.
	body = post(t, h, "/upgrade", url.Values{"key": {""}}).Body.String()
	if !strings.Contains(body, "a different brand, so nothing was uploaded") {
		t.Errorf("the upgrade did not refuse the key: %s", flashOf(body))
	}
}

// TestCloudSyncLeftOffWhenAnotherInstanceHoldsTheWorkspace. Cloud took
// nothing; left on, every run would be refused the same way.
func TestCloudSyncLeftOffWhenAnotherInstanceHoldsTheWorkspace(t *testing.T) {
	db, h, imports := syncApp(t, "acme.com")
	imports.status = http.StatusConflict
	imports.reply = `{"error":"another_instance_syncing","message":"another Limelit Open instance already syncs into this workspace","request_id":"r"}`

	body := post(t, h, "/settings/cloud-sync", url.Values{"sync": {"on"}}).Body.String()
	if !strings.Contains(body, "Another Limelit Open instance already syncs") || !strings.Contains(body, "Sync was left off.") {
		t.Errorf("the refusal said %s", flashOf(between(body, `id="cloud-sync"`, "btn-row")))
	}
	if syncIsOn(t, db) {
		t.Error("sync was left on although Cloud refused it")
	}
}

// TestCloudSyncNeedsAKey. Without a key there is no workspace to copy to.
func TestCloudSyncNeedsAKey(t *testing.T) {
	t.Setenv("LIMELIT_CLOUD_KEY", "")
	imports := newFakeImport(t)
	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)

	page := get(t, h, "/settings").Body.String()
	if !strings.Contains(page, "Save a Limelit Cloud key above to turn sync on.") || !strings.Contains(page, "Turn sync on</button>") {
		t.Error("the setting does not say it needs a key")
	}
	body := post(t, h, "/settings/cloud-sync", url.Values{"sync": {"on"}}).Body.String()
	if !strings.Contains(body, "Save a Limelit Cloud key first") {
		t.Errorf("sync on without a key said %s", flashOf(between(body, `id="cloud-sync"`, "btn-row")))
	}
	if syncIsOn(t, db) || len(imports.sent()) != 0 {
		t.Error("sync was turned on, or something was sent, without a key")
	}
}

// TestCloudSyncIsNotShownOnADemo. A public demo shows no credential surface
// and starts nothing.
func TestCloudSyncIsNotShownOnADemo(t *testing.T) {
	_, h := demoApp(t)
	if strings.Contains(get(t, h, "/settings").Body.String(), "Sync to Limelit Cloud") {
		t.Error("the demo shows the sync setting")
	}
}

// TestUpgradeFormTurnsSyncOff. After the move Cloud schedules the prompts;
// this instance's next run must not take the workspace's sync slot back.
func TestUpgradeFormTurnsSyncOff(t *testing.T) {
	db, h, _ := syncApp(t, "acme.com")
	post(t, h, "/settings/cloud-sync", url.Values{"sync": {"on"}})
	if !syncIsOn(t, db) {
		t.Fatal("sync did not turn on")
	}
	if rec := post(t, h, "/upgrade", url.Values{"key": {""}}); !strings.Contains(rec.Body.String(), "Imported into Limelit Cloud") {
		t.Fatalf("the upgrade did not go through: %s", flashOf(rec.Body.String()))
	}
	if syncIsOn(t, db) {
		t.Error("sync is still on after the upgrade")
	}
	if !strings.Contains(get(t, h, "/settings").Body.String(), "Turned off by the move to Limelit Cloud") {
		t.Error("Settings does not say the upgrade turned sync off")
	}
}
