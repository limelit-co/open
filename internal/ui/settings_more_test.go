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
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/limelit-co/open/internal/mcpserver"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/provider/providertest"
	"github.com/limelit-co/open/internal/store"
	"github.com/limelit-co/open/internal/upgrade"
)

// ---- targets ---------------------------------------------------------------

func TestPausingATargetSkipsItWithoutLosingAnything(t *testing.T) {
	// Pause is the answer to "stop paying for this engine for a while". The
	// runner reads enabled targets only, so pausing is enough to skip it,
	// and the answers it recorded stay for the charts.
	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	post(t, h, "/settings/targets/track", url.Values{"engine": {"chatgpt"}, "provider": {"openrouter"}})
	targets, _ := db.Targets(context.Background(), false)
	id := targets[0].ID
	if _, err := db.RecordChat(context.Background(), store.ChatRecord{TargetID: id, PromptID: seedPrompt(t, db), Status: "ok", Text: "Acme leads."}); err != nil {
		t.Fatal(err)
	}

	rec := post(t, h, "/settings/targets/pause", url.Values{"id": {itoa(id)}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "target-paused") {
		t.Fatalf("pause redirected to %q", loc)
	}
	if enabled, _ := db.Targets(context.Background(), true); len(enabled) != 0 {
		t.Error("the runner would still see the paused target")
	}
	if c, _ := db.Counts(context.Background()); c.Chats != 1 {
		t.Errorf("pausing changed the chat count to %d", c.Chats)
	}
	body := get(t, h, "/settings").Body.String()
	if !strings.Contains(body, ">paused<") || !strings.Contains(body, "Resume") {
		t.Error("settings does not show the target as paused with a Resume button")
	}

	rec = post(t, h, "/settings/targets/resume", url.Values{"id": {itoa(id)}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "target-resumed") {
		t.Fatalf("resume redirected to %q", loc)
	}
	if enabled, _ := db.Targets(context.Background(), true); len(enabled) != 1 {
		t.Error("resuming did not put the target back")
	}
}

func TestReTrackingAPausedTargetSaysResumedNotAdded(t *testing.T) {
	// AddTarget silently re-enables a stored spec. The flash has to say what
	// actually happened, or a user who paused an engine on purpose would
	// read "Target added" and not realise it is running again.
	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	track := url.Values{"engine": {"chatgpt"}, "provider": {"openrouter"}}
	post(t, h, "/settings/targets/track", track)
	targets, _ := db.Targets(context.Background(), false)
	post(t, h, "/settings/targets/pause", url.Values{"id": {itoa(targets[0].ID)}})

	rec := post(t, h, "/settings/targets/track", track)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "target-resumed") {
		t.Errorf("re-tracking a paused target redirected to %q, want the resumed flash", loc)
	}
	rec = post(t, h, "/settings/targets/track", track)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "target-kept") {
		t.Errorf("re-tracking a live target redirected to %q, want the kept flash", loc)
	}
}

func TestTargetHealthIsOnThePage(t *testing.T) {
	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	post(t, h, "/settings/targets/track", url.Values{"engine": {"chatgpt"}, "provider": {"openrouter"}})
	body := get(t, h, "/settings").Body.String()
	if !strings.Contains(body, "never run") {
		t.Error("a fresh target does not say it has never run")
	}

	targets, _ := db.Targets(context.Background(), false)
	prompt := seedPrompt(t, db)
	if _, err := db.RecordChat(context.Background(), store.ChatRecord{TargetID: targets[0].ID, PromptID: prompt, Status: "ok", Text: "Acme."}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordChat(context.Background(), store.ChatRecord{TargetID: targets[0].ID, PromptID: prompt, Status: "failed", Error: "provider: no credit or quota left"}); err != nil {
		t.Fatal(err)
	}
	body = get(t, h, "/settings").Body.String()
	if !strings.Contains(body, "last answer ") || !strings.Contains(body, "last failure ") || !strings.Contains(body, "no credit or quota left") {
		t.Errorf("health line missing: %s", between(body, `class="hint target-health"`, "</span>"))
	}
}

func TestHealthLineReadsRight(t *testing.T) {
	cases := map[store.TargetHealth]string{
		{}:                              "never run",
		{LastOK: "2026-09-14 10:00:00"}: "last answer Sep 14, 10:00 UTC",
		{LastOK: "2026-09-14 10:00:00", LastFailed: "2026-09-13 10:00:00", LastError: "old"}:          "last answer Sep 14, 10:00 UTC",
		{LastOK: "2026-09-13 10:00:00", LastFailed: "2026-09-14 10:00:00", LastError: "rate limited"}: "last answer Sep 13, 10:00 UTC; last failure Sep 14, 10:00 UTC: rate limited",
		{LastFailed: "2026-09-14 10:00:00", LastError: "provider: credentials rejected"}:              "no answer yet; last failure Sep 14, 10:00 UTC: provider: credentials rejected",
		{LastFailed: "2026-09-14 10:00:00", LastError: strings.Repeat("x", 120)}:                      "no answer yet; last failure Sep 14, 10:00 UTC: " + strings.Repeat("x", 90) + "...",
	}
	for in, want := range cases {
		if got := healthLine(in); got != want {
			t.Errorf("healthLine(%+v)\n got %q\nwant %q", in, got, want)
		}
	}
}

// ---- keys ------------------------------------------------------------------

func TestSavingABadKeyReportsTheVendorsVerdictInline(t *testing.T) {
	// The acceptance line for this screen: a rejected key shows the
	// provider's own typed error, under the field, at the moment of saving.
	reg := provider.NewRegistry()
	reg.Register(provider.Registration{
		Name: "openai", Access: provider.AccessAPI, Engines: map[string]string{"chatgpt": ""},
		Credentials: []string{"OPENAI_API_KEY"},
		New: func(provider.CredentialSource) (provider.Provider, error) {
			return provider.NewStub(provider.StubConfig{TestErr: provider.ErrAuth}), nil
		},
	})
	_, db, h := newApp(t, reg)
	seedProperty(t, h)

	rec := post(t, h, "/settings/keys", url.Values{"provider": {"openai"}, "cred_OPENAI_API_KEY": {"sk-wrong"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "did not accept the key") || !strings.Contains(body, provider.ErrAuth.Error()) {
		t.Errorf("the typed auth error is not on the page: %s", flashOf(body))
	}
	// Inline means inside the provider's card, after its heading and before
	// its form, not in the page-level notice slot at the top.
	card := between(body, `<h3>OpenAI`, `class="provider-form"`)
	if !strings.Contains(card, "did not accept the key") {
		t.Error("the verdict is not inside the OpenAI card")
	}
	if strings.Contains(body, "sk-wrong") {
		t.Error("the rejected key was echoed back")
	}
	// It stays saved: the usual fix is on the vendor's side, and Forget is
	// right there for a key that was simply wrong.
	if stored, _ := db.Setting(context.Background(), credentialPrefix+"OPENAI_API_KEY"); stored == "" {
		t.Error("a rejected key was discarded rather than kept for the user to fix or forget")
	}
}

func TestForgetRemovesTheSavedKey(t *testing.T) {
	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	post(t, h, "/settings/keys", url.Values{"provider": {"openrouter"}, "cred_OPENROUTER_API_KEY": {"sk-or-v1-x"}})
	if !strings.Contains(get(t, h, "/settings").Body.String(), "Forget saved key") {
		t.Fatal("no Forget button after saving")
	}

	rec := post(t, h, "/settings/keys/forget", url.Values{"provider": {"openrouter"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Forgotten") {
		t.Fatalf("forget = %d, %s", rec.Code, flashOf(rec.Body.String()))
	}
	if stored, _ := db.Setting(context.Background(), credentialPrefix+"OPENROUTER_API_KEY"); stored != "" {
		t.Error("the key is still stored after Forget")
	}
	if strings.Contains(get(t, h, "/settings").Body.String(), "Forget saved key") {
		t.Error("Forget is still offered with nothing to forget")
	}
}

// TestKeysNeverReachTheMCPTools is the other acceptance line: a stored
// credential must not come back through list_targets or any other tool.
func TestKeysNeverReachTheMCPTools(t *testing.T) {
	app, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	post(t, h, "/settings/targets/track", url.Values{"engine": {"chatgpt"}, "provider": {"openrouter"}})
	const key = "sk-or-v1-never-returned"
	post(t, h, "/settings/keys", url.Values{"provider": {"openrouter"}, "cred_OPENROUTER_API_KEY": {key}})
	post(t, h, "/settings/mcp/rotate", nil)

	srv, err := mcpserver.New(mcpserver.Deps{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"list_targets", "get_usage", "export_data"} {
		out := callTool(t, srv, tool)
		if strings.Contains(out, key) || strings.Contains(out, app.MCPToken()) {
			t.Errorf("%s returned a credential", tool)
		}
		if strings.Contains(out, credentialPrefix) || strings.Contains(out, settingMCPToken) {
			t.Errorf("%s returned a credential setting key: %s", tool, out)
		}
	}
}

// ---- schedule --------------------------------------------------------------

func TestScheduleIsStoredAndReadBack(t *testing.T) {
	// The scheduler asks ScheduleMode on every wake, so this setting is the
	// schedule; the yaml value is only the default before it is set.
	app, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	app.cfg.Schedule = "daily"
	if got := app.ScheduleMode(context.Background()); got != "daily" {
		t.Fatalf("yaml default = %q", got)
	}

	rec := post(t, h, "/settings/schedule", url.Values{"schedule": {"hourly"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "schedule-saved") {
		t.Fatalf("saving the schedule redirected to %q", loc)
	}
	if got := app.ScheduleMode(context.Background()); got != "hourly" {
		t.Errorf("mode after save = %q", got)
	}
	body := get(t, h, "/settings").Body.String()
	if !strings.Contains(body, `value="hourly" selected`) {
		t.Error("the select does not show the stored mode")
	}
	if !strings.Contains(body, "first pass runs shortly") {
		t.Error("the page does not say when the next pass is")
	}

	rec = post(t, h, "/settings/schedule", url.Values{"schedule": {"weekly"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "daily, hourly or off") {
		t.Errorf("an unknown mode was not refused: %d %s", rec.Code, flashOf(rec.Body.String()))
	}
	if got := app.ScheduleMode(context.Background()); got != "hourly" {
		t.Errorf("a refused mode changed the setting to %q", got)
	}
	post(t, h, "/settings/schedule", url.Values{"schedule": {"off"}})
	if got := app.ScheduleMode(context.Background()); got != "off" {
		t.Errorf("mode after off = %q", got)
	}
	if strings.Contains(get(t, h, "/settings").Body.String(), "Next pass") {
		t.Error("an off schedule still promises a next pass")
	}
}

// ---- MCP token -------------------------------------------------------------

func TestMCPTokenIsShownOnceAndEnforcedAtOnce(t *testing.T) {
	app, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	t.Setenv(mcpserver.TokenEnv, "")

	if app.MCPToken() != "" {
		t.Fatal("a fresh instance has a token")
	}
	body := get(t, h, "/settings").Body.String()
	if !strings.Contains(body, "Generate token") || strings.Contains(body, "Rotate token") {
		t.Error("a fresh instance does not offer Generate")
	}

	rec := post(t, h, "/settings/mcp/rotate", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("generate = %d", rec.Code)
	}
	token := app.MCPToken()
	if len(token) < 32 {
		t.Fatalf("token = %q", token)
	}
	if !strings.Contains(rec.Body.String(), token) || !strings.Contains(rec.Body.String(), `"Authorization":"Bearer `+token) {
		t.Error("the generating response does not show the token and its client snippet")
	}
	// Stored sealed, and never shown again.
	if sealed, _ := db.Setting(context.Background(), settingMCPToken); sealed == "" || strings.Contains(sealed, token) {
		t.Error("the token is not stored sealed")
	}
	body = get(t, h, "/settings").Body.String()
	if strings.Contains(body, token) {
		t.Error("the token is shown on a later page load")
	}
	if !strings.Contains(body, "Rotate token") || !strings.Contains(body, "Forget token") || !strings.Contains(body, "saved here") {
		t.Error("a saved token does not offer Rotate and Forget")
	}

	// The HTTP endpoint honours it now, through the same source the server
	// mounts, and stops honouring it the moment it is rotated.
	srv, err := mcpserver.New(mcpserver.Deps{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	mcp := mcpserver.Handler(srv, app.MCPToken, nil)
	if code := bearer(mcp, token); code == http.StatusUnauthorized || code == http.StatusServiceUnavailable {
		t.Errorf("the generated token was refused: %d", code)
	}
	post(t, h, "/settings/mcp/rotate", nil)
	if code := bearer(mcp, token); code != http.StatusUnauthorized {
		t.Errorf("the rotated-away token still works: %d", code)
	}
	if app.MCPToken() == token {
		t.Error("rotate did not change the token")
	}
	post(t, h, "/settings/mcp/forget", nil)
	if app.MCPToken() != "" {
		t.Error("forget left a token in force")
	}
	if code := bearer(mcp, app.MCPToken()); code != http.StatusServiceUnavailable {
		t.Errorf("after forget = %d, want the endpoint closed", code)
	}
}

func TestEnvironmentMCPTokenWinsAndIsNotRotatable(t *testing.T) {
	t.Setenv(mcpserver.TokenEnv, "from-env")
	app, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	if app.MCPToken() != "from-env" {
		t.Fatal("the environment does not win")
	}
	body := get(t, h, "/settings").Body.String()
	if !strings.Contains(body, "set in the environment") || strings.Contains(body, "Generate token") {
		t.Error("the page offers to generate a token the environment would override")
	}
	rec := post(t, h, "/settings/mcp/rotate", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), mcpserver.TokenEnv) {
		t.Errorf("rotate with an environment token = %d, %s", rec.Code, flashOf(rec.Body.String()))
	}
	if app.MCPToken() != "from-env" {
		t.Error("rotate changed a token it does not own")
	}
}

// ---- upgrade ---------------------------------------------------------------

func TestUpgradeFormPushesToCloudAndShowsWhatArrived(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"import": map[string]any{"import_id": "imp_1", "prompts_imported": 1, "competitors_imported": 0,
				"chats_imported": 1, "mentions_imported": 1, "citations_imported": 0, "chats_skipped": 2},
			"workspace_url": "https://limelit.co/w/acme",
			"mcp_url":       "https://api.limelit.co/mcp",
		})
	}))
	defer cloud.Close()
	t.Setenv(upgrade.EndpointEnv, cloud.URL)
	t.Setenv(upgrade.KeyEnv, "")

	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	post(t, h, "/settings/targets/track", url.Values{"engine": {"chatgpt"}, "provider": {"openrouter"}})
	targets, _ := db.Targets(context.Background(), false)
	if _, err := db.RecordChat(context.Background(), store.ChatRecord{TargetID: targets[0].ID, PromptID: seedPrompt(t, db), Status: "ok", Text: "Acme leads."}); err != nil {
		t.Fatal(err)
	}

	page := get(t, h, "/upgrade").Body.String()
	if strings.Contains(page, "not built yet") {
		t.Error("the upgrade screen still says the command is not built")
	}
	if !strings.Contains(page, `action="/upgrade"`) || !strings.Contains(page, "1 prompts") {
		t.Error("the upgrade screen has no form, or does not say what would move")
	}

	rec := post(t, h, "/upgrade", url.Values{"key": {"llc_test_key"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("upgrade = %d", rec.Code)
	}
	if gotAuth != "Bearer llc_test_key" {
		t.Errorf("Cloud saw Authorization %q", gotAuth)
	}
	if _, ok := gotBody["source_instance"]; !ok {
		t.Error("the payload has no instance id, so a re-run could not be idempotent")
	}
	body := rec.Body.String()
	for _, want := range []string{"Imported into Limelit Cloud", "1 prompts", "2 answers were already there", "https://limelit.co/w/acme", `"url":"https://api.limelit.co/mcp"`, "still has everything"} {
		if !strings.Contains(body, want) {
			t.Errorf("result page lacks %q", want)
		}
	}
	if strings.Contains(body, "llc_test_key") {
		t.Error("the Cloud key was rendered back")
	}
	if raw, _ := json.Marshal(gotBody); strings.Contains(string(raw), credentialPrefix) {
		t.Error("the payload carried a stored credential")
	}
}

func TestUpgradeFormShowsCloudsRefusalInline(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"unauthorized","message":"bad key"}}`, http.StatusUnauthorized)
	}))
	defer cloud.Close()
	t.Setenv(upgrade.EndpointEnv, cloud.URL)
	t.Setenv(upgrade.KeyEnv, "")
	_, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)

	rec := post(t, h, "/upgrade", url.Values{"key": {"wrong"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "rejected the API key") {
		t.Errorf("a refused upgrade = %d, %s", rec.Code, flashOf(rec.Body.String()))
	}
	rec = post(t, h, "/upgrade", url.Values{"key": {""}})
	if !strings.Contains(rec.Body.String(), "API key is required") {
		t.Errorf("a blank key was not refused: %s", flashOf(rec.Body.String()))
	}
	rec = post(t, h, "/upgrade", url.Values{"key": {"k"}, "since": {"yesterday"}})
	if !strings.Contains(rec.Body.String(), "2026-01-31") {
		t.Errorf("a bad date was not refused: %s", flashOf(rec.Body.String()))
	}
}

// ---- helpers ---------------------------------------------------------------

func seedPrompt(t *testing.T, db *store.DB) int64 {
	t.Helper()
	id, err := db.AddPrompt(context.Background(), store.Prompt{Text: "best widget tools", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func between(body, from, to string) string {
	i := strings.Index(body, from)
	if i < 0 {
		return "(" + from + " not found)"
	}
	tail := body[i:]
	if j := strings.Index(tail, to); j > 0 {
		return tail[:j]
	}
	return tail[:min(len(tail), 400)]
}

func bearer(h http.Handler, token string) int {
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	h.ServeHTTP(res, req)
	return res.Code
}

// callTool invokes one MCP tool in-process and returns its result as text.
func callTool(t *testing.T, srv *mcp.Server, name string) string {
	t.Helper()
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go srv.Run(ctx, serverT)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	if res.StructuredContent != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		b.Write(raw)
	}
	return b.String()
}
