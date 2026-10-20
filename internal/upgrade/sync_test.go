// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/limelit-co/open/internal/runner"
	"github.com/limelit-co/open/internal/store"
)

// fakeCloud stands in for POST /v1/import/open and keeps every payload.
// reply decides each answer by request number (1-based); nil is a 200.
type fakeCloud struct {
	mu    sync.Mutex
	got   []map[string]any
	reply func(n int) (int, string)
	srv   *httptest.Server
}

func newFakeCloud(t *testing.T) *fakeCloud {
	t.Helper()
	f := &fakeCloud{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Errorf("the payload is not valid JSON: %v", err)
		}
		f.mu.Lock()
		f.got = append(f.got, doc)
		n := len(f.got)
		f.mu.Unlock()

		status, body := http.StatusOK, ""
		if f.reply != nil {
			status, body = f.reply(n)
		}
		if status == http.StatusOK && body == "" {
			chats, _ := doc["chats"].([]any)
			body = `{"ok":true,"import":{"import_id":"imp","chats_imported":` + itoa(len(chats)) + `}}`
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCloud) requests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.got...)
}

func (f *fakeCloud) opts() Options {
	return Options{Key: "llk_test", Endpoint: f.srv.URL, Client: f.srv.Client()}
}

// Cloud's real error envelope: the code is a string, not an object.
const anotherInstanceReply = `{"error":"another_instance_syncing","message":"another Limelit Open instance already syncs into this workspace, so nothing was imported.","request_id":"r1"}`

// syncing is an instance with sync on and answers on the given days, one
// answer per entry.
func syncing(t *testing.T, days ...string) *store.DB {
	t.Helper()
	db := seeded(t)
	if err := SetSyncOn(context.Background(), db, true); err != nil {
		t.Fatal(err)
	}
	addAnswers(t, db, days...)
	return db
}

func addAnswers(t *testing.T, db *store.DB, days ...string) {
	t.Helper()
	ctx := context.Background()
	targets, err := db.Targets(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) == 0 {
		id, err := db.AddTarget(ctx, store.Target{Spec: "chatgpt:openai", Engine: "chatgpt", Provider: "openai", Access: "api"})
		if err != nil {
			t.Fatal(err)
		}
		targets = []store.Target{{ID: id}}
	}
	prompts, err := db.Prompts(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, day := range days {
		if _, err := db.RecordChat(ctx, store.ChatRecord{
			PromptID: prompts[0].ID, TargetID: targets[0].ID, Status: store.ChatOK,
			Text: "Acme leads on " + day, CreatedAt: day + " 09:00:00", Calls: 1,
			Mentions:  []store.Mention{{BrandName: "Acme", BrandKey: "acme"}},
			Citations: []store.Citation{{URL: "https://acme.com/a", Host: "acme.com", Site: "acme.com", Position: 1, SourceType: "own"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func smallWindows(t *testing.T, n int) {
	t.Helper()
	was := chatsPerRequest
	chatsPerRequest = n
	t.Cleanup(func() { chatsPerRequest = was })
}

func chatsIn(doc map[string]any) []string {
	rows, _ := doc["chats"].([]any)
	var out []string
	for _, r := range rows {
		row := r.(map[string]any)
		out = append(out, row["created_at"].(string)[:10])
	}
	return out
}

func setting(t *testing.T, db *store.DB, key string) string {
	t.Helper()
	v, err := db.Setting(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestSyncSplitsHistoryIntoWindowsOfAboutTwoThousand. A first sync of a
// long history in one request is the big write that has frozen Cloud's
// database before. Every answer goes exactly once, oldest first, no request
// over the cap unless one day alone is, and that day goes alone.
func TestSyncSplitsHistoryIntoWindowsOfAboutTwoThousand(t *testing.T) {
	smallWindows(t, 3)
	days := []string{
		"2026-03-01", "2026-03-01",
		"2026-03-02", "2026-03-02",
		"2026-03-03",
		"2026-03-04", "2026-03-04", "2026-03-04", "2026-03-04", // one day over the cap
		"2026-03-05",
	}
	db := syncing(t, days...)
	cloud := newFakeCloud(t)

	res, err := Sync(context.Background(), db, cloud.opts())
	if err != nil {
		t.Fatal(err)
	}
	reqs := cloud.requests()
	want := [][]string{
		{"2026-03-01", "2026-03-01"},
		{"2026-03-02", "2026-03-02", "2026-03-03"},
		{"2026-03-04", "2026-03-04", "2026-03-04", "2026-03-04"},
		{"2026-03-05"},
	}
	if len(reqs) != len(want) || res.Requests != len(want) {
		t.Fatalf("sent %d requests (result says %d), want %d", len(reqs), res.Requests, len(want))
	}
	var all []string
	for i, req := range reqs {
		got := chatsIn(req)
		if strings.Join(got, ",") != strings.Join(want[i], ",") {
			t.Errorf("request %d carried %v, want %v", i+1, got, want[i])
		}
		all = append(all, got...)
		if req["mode"] != ModeSync {
			t.Errorf("request %d mode = %v", i+1, req["mode"])
		}
		if req["source_instance"] == nil {
			t.Errorf("request %d has no source_instance", i+1)
		}
		// Each table hanging off an answer travels in the answer's window.
		if m, _ := req["mentions"].([]any); len(m) != len(want[i]) {
			t.Errorf("request %d carried %d mentions for %d answers", i+1, len(m), len(want[i]))
		}
	}
	sort.Strings(all)
	sorted := append([]string(nil), days...)
	sort.Strings(sorted)
	if strings.Join(all, ",") != strings.Join(sorted, ",") {
		t.Errorf("the windows carried %v between them, want every answer once: %v", all, sorted)
	}
	// Windows are contiguous: each until is the next since, the first has
	// no lower bound and the last no upper one.
	if reqs[0]["since"] != nil || reqs[len(reqs)-1]["until"] != nil {
		t.Errorf("the outer windows are bounded: first since %v, last until %v", reqs[0]["since"], reqs[len(reqs)-1]["until"])
	}
	for i := 1; i < len(reqs); i++ {
		if reqs[i-1]["until"] != reqs[i]["since"] {
			t.Errorf("window %d ends at %v but window %d starts at %v", i, reqs[i-1]["until"], i+1, reqs[i]["since"])
		}
	}
	if res.Chats != len(days) {
		t.Errorf("result counts %d answers imported, want %d", res.Chats, len(days))
	}
	if got := setting(t, db, SyncSinceSetting); got != "2026-03-05" {
		t.Errorf("cursor = %q, want the last day sent", got)
	}
}

// TestSyncSendsOnlyWhatIsNewUntilTheAnswersAreReadAgain. A normal sync starts
// at the cursor's day; a change of analysis signature (a brand or competitor
// edit made every stored answer be read again) re-sends everything with
// replace_derived, so the mentions already in Cloud match the ones here.
func TestSyncSendsOnlyWhatIsNewUntilTheAnswersAreReadAgain(t *testing.T) {
	ctx := context.Background()
	db := syncing(t, "2026-04-01", "2026-04-02")
	if err := db.SetSetting(ctx, runner.AnalysisSignatureSetting, "sig-1"); err != nil {
		t.Fatal(err)
	}
	cloud := newFakeCloud(t)

	// The first sync is a full send under replace_derived: answers imported
	// earlier by an upgrade may have been read under older rules.
	first, err := Sync(ctx, db, cloud.opts())
	if err != nil {
		t.Fatal(err)
	}
	if !first.Resent || cloud.requests()[0]["replace_derived"] != true {
		t.Errorf("the first sync was not a full re-send: resent %v, replace_derived %v", first.Resent, cloud.requests()[0]["replace_derived"])
	}
	if got := setting(t, db, SyncSignatureSetting); got != "sig-1" {
		t.Errorf("signature recorded = %q, want sig-1", got)
	}

	addAnswers(t, db, "2026-04-02", "2026-04-03")
	second, err := Sync(ctx, db, cloud.opts())
	if err != nil {
		t.Fatal(err)
	}
	req := cloud.requests()[1]
	if second.Resent || req["replace_derived"] != nil {
		t.Errorf("an unchanged signature re-sent everything: resent %v, replace_derived %v", second.Resent, req["replace_derived"])
	}
	if req["since"] != "2026-04-02" {
		t.Errorf("the second sync started at %v, want the cursor's day 2026-04-02", req["since"])
	}
	if got := strings.Join(chatsIn(req), ","); got != "2026-04-02,2026-04-02,2026-04-03" {
		t.Errorf("the second sync carried %s, want the cursor's day and after", got)
	}

	if err := db.SetSetting(ctx, runner.AnalysisSignatureSetting, "sig-2"); err != nil {
		t.Fatal(err)
	}
	third, err := Sync(ctx, db, cloud.opts())
	if err != nil {
		t.Fatal(err)
	}
	req = cloud.requests()[2]
	if !third.Resent || req["replace_derived"] != true || req["since"] != nil || len(chatsIn(req)) != 4 {
		t.Errorf("after the answers were read again the sync was not a full re-send: resent %v, replace_derived %v, since %v, %d answers",
			third.Resent, req["replace_derived"], req["since"], len(chatsIn(req)))
	}
	if got := setting(t, db, SyncSignatureSetting); got != "sig-2" {
		t.Errorf("signature recorded = %q, want sig-2", got)
	}
}

// TestSyncCursorAdvancesOnlyPastWhatCloudConfirmed. A window Cloud refused
// must be sent again, so the cursor stops before it; a full re-send that
// fails halfway records no signature, so the next sync re-sends in full.
func TestSyncCursorAdvancesOnlyPastWhatCloudConfirmed(t *testing.T) {
	ctx := context.Background()
	smallWindows(t, 2)

	// A full re-send that fails on its second window.
	db := syncing(t, "2026-05-01", "2026-05-01", "2026-05-02", "2026-05-02")
	cloud := newFakeCloud(t)
	cloud.reply = func(n int) (int, string) {
		if n == 2 {
			return http.StatusInternalServerError, `{"error":"import_failed","message":"the import could not be completed and nothing was written; re-running it is safe"}`
		}
		return http.StatusOK, ""
	}
	if _, err := Sync(ctx, db, cloud.opts()); err == nil {
		t.Fatal("a refused window was reported as a sync")
	}
	if got := setting(t, db, SyncSignatureSetting); got != "" {
		t.Errorf("a half-done re-send recorded signature %q", got)
	}
	if at, _ := db.SettingUpdatedAt(ctx, SyncSignatureSetting); at != "" {
		t.Error("a half-done re-send marked the answers as sent")
	}
	if got := setting(t, db, SyncSinceSetting); got != "" {
		t.Errorf("a half-done re-send moved the cursor to %q", got)
	}
	st, _ := State(ctx, db)
	if st.Last == nil || st.Last.OK || !strings.Contains(st.Last.Error, "refused the copy (500 import_failed)") {
		t.Errorf("the failure was not recorded in words: %+v", st.Last)
	}

	// Now a clean full sync, then new answers over two windows of which the
	// second fails: the cursor stops at the first window's last day.
	cloud.reply = nil
	if _, err := Sync(ctx, db, cloud.opts()); err != nil {
		t.Fatal(err)
	}
	if got := setting(t, db, SyncSinceSetting); got != "2026-05-02" {
		t.Fatalf("cursor after a clean sync = %q", got)
	}
	// From the cursor's day on, windows of two: [05-02] (the cursor's day,
	// sent again), [05-03], [05-04 05-04]. The third is refused.
	addAnswers(t, db, "2026-05-03", "2026-05-04", "2026-05-04")
	sent := len(cloud.requests())
	cloud.reply = func(n int) (int, string) {
		if n == sent+3 {
			return http.StatusServiceUnavailable, `{"error":"unavailable","message":"try later"}`
		}
		return http.StatusOK, ""
	}
	if _, err := Sync(ctx, db, cloud.opts()); err == nil {
		t.Fatal("a refused window was reported as a sync")
	}
	if got := setting(t, db, SyncSinceSetting); got != "2026-05-03" {
		t.Errorf("cursor = %q, want the last day of the window Cloud confirmed, 2026-05-03", got)
	}
	st, _ = State(ctx, db)
	if st.Last == nil || st.Last.LastOKAt == "" {
		t.Errorf("a failure lost the time of the last sync that went through: %+v", st.Last)
	}

	// The next sync picks up from there.
	cloud.reply = nil
	if _, err := Sync(ctx, db, cloud.opts()); err != nil {
		t.Fatal(err)
	}
	reqs := cloud.requests()
	if first := reqs[sent+3]; first["since"] != "2026-05-03" {
		t.Errorf("the retry started at %v, want 2026-05-03", first["since"])
	}
	if got := setting(t, db, SyncSinceSetting); got != "2026-05-04" {
		t.Errorf("cursor after the retry = %q", got)
	}
}

// TestSyncNamesAnotherInstanceHoldingTheWorkspace. Cloud's 409 carries its
// code as a string; it has to come out as the reason, not as a status.
func TestSyncNamesAnotherInstanceHoldingTheWorkspace(t *testing.T) {
	db := syncing(t, "2026-06-01")
	cloud := newFakeCloud(t)
	cloud.reply = func(int) (int, string) { return http.StatusConflict, anotherInstanceReply }

	_, err := Sync(context.Background(), db, cloud.opts())
	if !errors.Is(err, ErrAnotherInstanceSyncing) {
		t.Fatalf("err = %v, want ErrAnotherInstanceSyncing", err)
	}
	for _, want := range []string{"Another Limelit Open instance", "nothing was copied", "turn sync off in that one's Settings", "30 days"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message %q does not say %q", err, want)
		}
	}
	if got := setting(t, db, SyncSinceSetting); got != "" {
		t.Errorf("a refused sync moved the cursor to %q", got)
	}
	// The upgrade's own wording for the same refusal.
	var ce *CloudError
	if e := cloudError(http.StatusConflict, []byte(anotherInstanceReply)); !errors.As(e, &ce) || ce.Code != "another_instance_syncing" {
		t.Errorf("the string code was not read: %#v", e)
	}
}

// TestSyncOffSendsNoRows. Turning sync off tells Cloud and nothing else: the
// payload is the envelope, the mode and this instance's id.
func TestSyncOffSendsNoRows(t *testing.T) {
	ctx := context.Background()
	db := syncing(t, "2026-07-01")
	cloud := newFakeCloud(t)

	if err := SyncOff(ctx, db, cloud.opts()); err != nil {
		t.Fatal(err)
	}
	reqs := cloud.requests()
	if len(reqs) != 1 {
		t.Fatalf("sync off sent %d requests", len(reqs))
	}
	var keys []string
	for k := range reqs[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if got := strings.Join(keys, ","); got != "mode,schema_version,source_instance" {
		t.Errorf("sync off carried %s, want only mode, schema_version and source_instance", got)
	}
	if reqs[0]["mode"] != ModeSyncOff {
		t.Errorf("mode = %v", reqs[0]["mode"])
	}
	if on, _ := SyncOn(ctx, db); on {
		t.Error("sync is still on here")
	}
	if _, err := Sync(ctx, db, cloud.opts()); !errors.Is(err, ErrSyncOff) {
		t.Errorf("a sync after turning it off = %v, want ErrSyncOff", err)
	}
	if len(cloud.requests()) != 1 {
		t.Error("a sync went out after sync was turned off")
	}
}

// TestSyncOffIsOffHereEvenWhenCloudIsNotTold. Nothing more leaves this
// machine from the moment the user says off, whatever Cloud answers.
func TestSyncOffIsOffHereEvenWhenCloudIsNotTold(t *testing.T) {
	ctx := context.Background()
	db := syncing(t)
	cloud := newFakeCloud(t)
	cloud.reply = func(int) (int, string) { return http.StatusBadGateway, `{"error":"bad_gateway","message":"upstream"}` }

	err := SyncOff(ctx, db, cloud.opts())
	if err == nil || !strings.Contains(err.Error(), "Sync is off here") || !strings.Contains(err.Error(), "Turn sync off again") {
		t.Fatalf("err = %v, want it to say sync is off here and how to retry", err)
	}
	if on, _ := SyncOn(ctx, db); on {
		t.Error("a sync off Cloud did not hear left sync on here")
	}
	st, _ := State(ctx, db)
	if st.Last == nil || st.Last.Mode != ModeSyncOff || st.Last.OK {
		t.Errorf("the undelivered sync off was not recorded: %+v", st.Last)
	}
}

// TestUpgradeTurnsSyncOffHere. Cloud turns the instance's sync off on an
// upgrade; if this side stayed on, its next run would take the slot back.
func TestUpgradeTurnsSyncOffHere(t *testing.T) {
	ctx := context.Background()
	db := syncing(t, "2026-08-01")

	refused, _, _ := cloudStub(t, http.StatusInternalServerError, `{"error":"import_failed","message":"boom"}`)
	if _, err := Run(ctx, db, Options{Key: "k", Endpoint: refused.URL, Client: refused.Client()}); err == nil {
		t.Fatal("a refused upgrade was reported as one")
	}
	if on, _ := SyncOn(ctx, db); !on {
		t.Error("a failed upgrade turned sync off")
	}

	srv, body, _ := cloudStub(t, http.StatusOK, okReply)
	if _, err := Run(ctx, db, Options{Key: "k", Endpoint: srv.URL, Client: srv.Client()}); err != nil {
		t.Fatal(err)
	}
	if on, _ := SyncOn(ctx, db); on {
		t.Error("sync is still on after an upgrade")
	}
	st, _ := State(ctx, db)
	if st.Last == nil || st.Last.Mode != ModeUpgrade {
		t.Errorf("the upgrade's turning sync off was not recorded: %+v", st.Last)
	}
	// The upgrade's payload is the one every Cloud already takes: no mode.
	var sent map[string]any
	json.Unmarshal(*body, &sent)
	if _, ok := sent["mode"]; ok {
		t.Errorf("the upgrade payload carries mode %v", sent["mode"])
	}
}

// TestSyncWithNothingNewStillSends. The request carries the prompts' active
// flags and keeps the workspace's sync slot from going quiet.
func TestSyncWithNothingNewStillSends(t *testing.T) {
	ctx := context.Background()
	db := syncing(t)
	cloud := newFakeCloud(t)
	if _, err := Sync(ctx, db, cloud.opts()); err != nil {
		t.Fatal(err)
	}
	reqs := cloud.requests()
	if len(reqs) != 1 {
		t.Fatalf("sent %d requests, want 1", len(reqs))
	}
	prompts, _ := reqs[0]["prompts"].([]any)
	if len(prompts) == 0 || prompts[0].(map[string]any)["active"] != true {
		t.Errorf("the prompts and their active flags did not go: %v", reqs[0]["prompts"])
	}
}

// TestSyncNeedsAKeyAndSendsNoProviderKey.
func TestSyncNeedsAKeyAndSendsNoProviderKey(t *testing.T) {
	ctx := context.Background()
	db := syncing(t, "2026-09-01")
	if err := db.SetSetting(ctx, "credential:OPENAI_API_KEY", "sk-super-secret-value"); err != nil {
		t.Fatal(err)
	}
	cloud := newFakeCloud(t)

	opts := cloud.opts()
	opts.Key = ""
	if _, err := Sync(ctx, db, opts); err == nil || !strings.Contains(err.Error(), "No Limelit Cloud key") {
		t.Errorf("a sync with no key = %v", err)
	}
	if _, err := Sync(ctx, db, cloud.opts()); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cloud.requests())
	for _, secret := range []string{"sk-super-secret-value", "OPENAI_API_KEY", "credential:"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("a sync carried %q", secret)
		}
	}
}

// TestSyncWordingFollowsTheCopyRules. These sentences are shown in Settings:
// no em dash, and neither of the two words the dashboard never uses.
func TestSyncWordingFollowsTheCopyRules(t *testing.T) {
	banned := regexp.MustCompile(`(?i)\b(api|scraped)\b|—|&mdash;`)
	errs := []error{
		&CloudError{Status: 409, Code: "another_instance_syncing"},
		&CloudError{Status: 401},
		&CloudError{Status: 413},
		&CloudError{Status: 400, Code: "unsupported_mode"},
		&CloudError{Status: 500, Code: "import_failed"},
		&unreachableError{err: errors.New(`Post "https://api.limelit.co/v1/import/open": dial tcp: no such host`)},
		&unreachableError{err: context.DeadlineExceeded},
	}
	for _, e := range errs {
		if msg := syncProblem(e).Message; banned.MatchString(msg) {
			t.Errorf("%q breaks the copy rules", msg)
		}
	}
}

func TestBuildPayloadSplicesTheMode(t *testing.T) {
	raw, err := buildPayload(context.Background(), seeded(t), "open-abc",
		Options{Mode: ModeSync, ReplaceDerived: true, Since: "2026-01-01", Until: "2026-02-01"})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the spliced payload is not valid JSON: %v\n%s", err, raw)
	}
	if doc["mode"] != ModeSync || doc["replace_derived"] != true || doc["source_instance"] != "open-abc" {
		t.Errorf("mode %v, replace_derived %v, source_instance %v", doc["mode"], doc["replace_derived"], doc["source_instance"])
	}
	if doc["since"] != "2026-01-01" || doc["until"] != "2026-02-01" {
		t.Errorf("since %v, until %v", doc["since"], doc["until"])
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
