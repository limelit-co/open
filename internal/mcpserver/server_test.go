// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/limelit-co/open/internal/metrics"
	"github.com/limelit-co/open/internal/store"
)

// connect builds a server over an in-memory transport pair, so every test
// here exercises the real protocol without a process or a socket.
func connect(t *testing.T) (*mcp.ClientSession, *store.DB) {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "limelit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.SaveProperty(ctx, store.Property{Name: "Acme", Domain: "acme.com"}); err != nil {
		t.Fatal(err)
	}
	rival, err := db.AddCompetitor(ctx, store.Competitor{Name: "Rival", Domain: "rival.com"})
	if err != nil {
		t.Fatal(err)
	}
	promptID, err := db.AddPrompt(ctx, store.Prompt{Text: "best widget tools", Category: "discovery", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := db.AddTarget(ctx, store.Target{
		Spec: "chatgpt:openai:online", Engine: "chatgpt", Provider: "openai", Online: true, Access: "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	evalID, err := db.CreateEvaluation(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	rank := 2
	if _, err := db.RecordChat(ctx, store.ChatRecord{
		EvaluationID: evalID, PromptID: promptID, TargetID: targetID,
		Status: store.ChatOK, Text: "Rival leads, Acme is second.", Model: "test",
		Mentions: []store.Mention{
			{BrandName: "Acme", BrandKey: "acme", ListRank: &rank, OffsetStart: 13, OffsetEnd: 17},
			{CompetitorID: &rival, BrandName: "Rival", BrandKey: "rival"},
		},
		Citations: []store.Citation{{URL: "https://g2.com/x", Host: "g2.com", Site: "g2.com", Position: 1, SourceType: "informational"}},
		FanOut:    []string{"best widget tools 2026"},
	}); err != nil {
		t.Fatal(err)
	}

	srv, err := New(Deps{DB: db, Metrics: metrics.New(db)})
	if err != nil {
		t.Fatal(err)
	}
	clientT, serverT := mcp.NewInMemoryTransports()
	go srv.Run(ctx, serverT)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, db
}

func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// TestCatalogMatchesTheDocumentedNames guards the promise the whole project
// rests on: a conversation written against this server keeps working after
// limelit upgrade, because the tool names are Cloud's.
func TestCatalogMatchesTheDocumentedNames(t *testing.T) {
	s, _ := connect(t)
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %q has no description; a model picks tools by description", tool.Name)
		}
	}
	for _, want := range []string{
		"get_active_property", "list_competitors", "list_prompts",
		"get_overview_kpis", "get_kpi_history", "get_matrix",
		"list_chats", "get_chat", "list_top_sources", "list_source_urls",
		"list_targets", "get_usage",
	} {
		if !got[want] {
			t.Errorf("missing documented tool %q", want)
		}
	}
	// No stubs: a Cloud-only tool must be absent, so an agent discovers the
	// boundary by not finding it rather than by getting an approximation.
	for _, cloudOnly := range []string{
		"get_sentiment_series", "get_fanouts", "list_segments",
		"get_perception_summary", "list_opportunities", "gsc_findings",
	} {
		if got[cloudOnly] {
			t.Errorf("%q is a Cloud tool and must not be registered here", cloudOnly)
		}
	}
}

// TestCloudArgumentsAreRejectedNotIgnored is the rule that stops this server
// returning a confidently wrong number. Silently dropping a segment filter
// would answer for the whole property while the caller believed it was scoped.
func TestCloudArgumentsAreRejectedNotIgnored(t *testing.T) {
	s, _ := connect(t)
	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"get_overview_kpis", map[string]any{"segment": "emea"}, "segment"},
		{"get_kpi_history", map[string]any{"segment": "emea"}, "segment"},
		{"list_prompts", map[string]any{"segment": "emea"}, "segment"},
		{"list_chats", map[string]any{"segment": "emea"}, "segment"},
		{"list_top_sources", map[string]any{"segment": "emea"}, "segment"},
		{"get_matrix", map[string]any{"metric": "sentiment"}, "sentiment"},
	} {
		res := call(t, s, tc.tool, tc.args)
		if !res.IsError {
			t.Errorf("%s accepted %v, which would return a number for the wrong scope", tc.tool, tc.args)
			continue
		}
		text := resultText(res)
		if !strings.Contains(text, tc.want) {
			t.Errorf("%s error does not name %q: %s", tc.tool, tc.want, text)
		}
		if !strings.Contains(text, "Cloud") {
			t.Errorf("%s error does not say where the feature lives: %s", tc.tool, text)
		}
	}
}

func TestOverviewCarriesItsDenominator(t *testing.T) {
	s, _ := connect(t)
	res := call(t, s, "get_overview_kpis", map[string]any{"days": 30})
	if res.IsError {
		t.Fatal(resultText(res))
	}
	out, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("no structured content: %s", resultText(res))
	}
	for _, field := range []string{"n", "low_n", "targets", "visibility_pct", "excluded"} {
		if _, ok := out[field]; !ok {
			t.Errorf("result is missing %q", field)
		}
	}
	// One answer is emphatically low n, and the result has to say so rather
	// than presenting 100% as a market position.
	if low, _ := out["low_n"].(bool); !low {
		t.Error("low_n is false on a single answer")
	}
	if n, _ := out["n"].(float64); n != 1 {
		t.Errorf("n = %v, want 1", out["n"])
	}
	targets, _ := out["targets"].([]any)
	if len(targets) != 1 {
		t.Fatalf("targets = %v", out["targets"])
	}
	if first, _ := targets[0].(map[string]any); first["access"] != "api" {
		t.Errorf("the access mode did not travel with the target: %v", targets[0])
	}
}

func TestMatrixMarksCellsThatNeverRan(t *testing.T) {
	s, db := connect(t)
	// A second prompt that was never asked of anything.
	if _, err := db.AddPrompt(context.Background(), store.Prompt{Text: "unasked", Active: true}); err != nil {
		t.Fatal(err)
	}
	res := call(t, s, "get_matrix", nil)
	out := res.StructuredContent.(map[string]any)
	rows, _ := out["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	var sawRan, sawUnrun bool
	for _, r := range rows {
		for _, c := range r.(map[string]any)["cells"].([]any) {
			if ran, _ := c.(map[string]any)["ran"].(bool); ran {
				sawRan = true
			} else {
				sawUnrun = true
			}
		}
	}
	if !sawRan || !sawUnrun {
		t.Error("the grid must distinguish a cell that ran from one that never did: an unasked prompt is not one the engine ignored")
	}
}

func TestGetChatReturnsTheEvidence(t *testing.T) {
	s, _ := connect(t)
	list := call(t, s, "list_chats", nil)
	chats := list.StructuredContent.(map[string]any)["chats"].([]any)
	if len(chats) == 0 {
		t.Fatal("no chats listed")
	}
	id := chats[0].(map[string]any)["id"]

	res := call(t, s, "get_chat", map[string]any{"id": id})
	if res.IsError {
		t.Fatal(resultText(res))
	}
	out := res.StructuredContent.(map[string]any)
	if text, _ := out["text"].(string); !strings.Contains(text, "Acme") {
		t.Errorf("the answer body is missing: %v", out["text"])
	}
	brands, _ := out["brands"].([]any)
	if len(brands) != 2 {
		t.Fatalf("brands = %d, want 2", len(brands))
	}
	// The offsets are the matcher's own, so a caller can quote exactly what
	// was counted.
	first := brands[0].(map[string]any)
	if _, ok := first["offset"]; !ok {
		t.Error("brand offsets are not returned; a caller cannot quote what was counted")
	}
	if fan, _ := out["fan_out"].([]any); len(fan) != 1 {
		t.Errorf("fan_out = %v, want the one recorded query", out["fan_out"])
	}
}

func TestListChatsRejectsAnUnknownTarget(t *testing.T) {
	s, _ := connect(t)
	res := call(t, s, "list_chats", map[string]any{"target": "gemini:google"})
	if !res.IsError {
		t.Fatal("an unknown target silently returned every chat")
	}
	if !strings.Contains(resultText(res), "chatgpt:openai:online") {
		t.Errorf("the error should name the configured targets: %s", resultText(res))
	}
}

func TestUsageCarriesNoCurrency(t *testing.T) {
	// The open core has no pricing table, and inventing one would be worse
	// than omitting it.
	s, _ := connect(t)
	res := call(t, s, "get_usage", nil)
	body := strings.ToLower(resultText(res))
	for _, forbidden := range []string{"cost", "cents", "usd", "$", "price"} {
		if strings.Contains(body, forbidden) && !strings.Contains(body, "no pricing") {
			t.Errorf("usage mentions %q: the open core carries no pricing", forbidden)
		}
	}
}

func TestInstructionsWarnAboutTheDenominators(t *testing.T) {
	// The handshake is the only chance to tell a model what these numbers
	// exclude, before it reports one.
	for _, want := range []string{"branded", "no AI Overview", "api", "scraped", "low_n", "Cloud"} {
		if !strings.Contains(instructions, want) {
			t.Errorf("the server instructions do not mention %q", want)
		}
	}
}

// ---- HTTP transport --------------------------------------------------------

func TestHTTPRefusesWithoutAToken(t *testing.T) {
	// A self-hosted instance is often on a box with a more generous firewall
	// than its owner assumes. With no token the endpoint must fail closed and
	// explain itself rather than serving every stored answer.
	srv, err := New(Deps{DB: openEmpty(t)})
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(srv, StaticToken(""), nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", res.Code)
	}
	if !strings.Contains(res.Body.String(), TokenEnv) {
		t.Errorf("the refusal does not say how to enable it: %s", res.Body.String())
	}
}

func TestHTTPRequiresTheRightToken(t *testing.T) {
	srv, err := New(Deps{DB: openEmpty(t)})
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(srv, StaticToken("secret"), nil)

	for _, header := range []string{"", "Bearer wrong", "secret", "Basic secret", "Bearer "} {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		h.ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Errorf("header %q got %d, want 401", header, res.Code)
		}
		if res.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("header %q: no challenge sent, so a client cannot tell what to send", header)
		}
	}
}

func TestAuthorizedIsCaseInsensitiveOnTheScheme(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "bearer secret")
	if !authorized(req, "secret") {
		t.Error("the scheme is case-insensitive per RFC 7235")
	}
}

func openEmpty(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "limelit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestPromptTemplatesMatchTheDocumentedCatalog. The templates are the walks
// worth repeating, and their ids match Cloud so a client that has learned one
// keeps it after an upgrade.
func TestPromptTemplatesMatchTheDocumentedCatalog(t *testing.T) {
	s, _ := connect(t)
	res, err := s.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Prompt{}
	for _, p := range res.Prompts {
		got[p.Name] = p
	}
	for _, want := range []string{"limelit_weekly_pulse", "limelit_competitor_radar", "limelit_why_not_cited"} {
		p, ok := got[want]
		if !ok {
			t.Errorf("missing documented prompt %q", want)
			continue
		}
		if p.Description == "" {
			t.Errorf("%q has no description", want)
		}
	}
}

func TestCompetitorRadarDefaultsAndWalk(t *testing.T) {
	s, _ := connect(t)
	ctx := context.Background()

	// With no arguments the walk still has to be complete and specific.
	res, err := s.GetPrompt(ctx, &mcp.GetPromptParams{Name: "limelit_competitor_radar"})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, m := range res.Messages {
		if tc, ok := m.Content.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	for _, want := range []string{"list_competitors", "get_overview_kpis", "list_source_urls", "get_matrix", "30 days", "10 percentage points"} {
		if !strings.Contains(text, want) {
			t.Errorf("the walk does not mention %q:\n%s", want, text)
		}
	}
	// Every tool it names must actually exist, or the walk sends the model
	// at something that is not there.
	tools, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, tool := range tools.Tools {
		registered[tool.Name] = true
	}
	for _, named := range []string{"list_competitors", "get_overview_kpis", "list_source_urls", "get_matrix"} {
		if !registered[named] {
			t.Errorf("the walk names %q, which is not a registered tool", named)
		}
	}

	// Arguments are honoured.
	res, err = s.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "limelit_competitor_radar",
		Arguments: map[string]string{"window_days": "7", "threshold_pp": "3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text = ""
	for _, m := range res.Messages {
		if tc, ok := m.Content.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if !strings.Contains(text, "7 days") || !strings.Contains(text, "3 percentage points") {
		t.Errorf("arguments were not interpolated:\n%s", text)
	}
}

// TestPromptTemplatesDoNotPromiseCloudFeatures: a walk that tells the model to
// explain WHY a number moved would send it somewhere this instance cannot
// measure, and it would answer from guesswork.
func TestPromptTemplatesDoNotPromiseCloudFeatures(t *testing.T) {
	s, _ := connect(t)
	ctx := context.Background()
	for _, name := range []string{"limelit_weekly_pulse", "limelit_competitor_radar", "limelit_why_not_cited"} {
		res, err := s.GetPrompt(ctx, &mcp.GetPromptParams{Name: name, Arguments: map[string]string{"prompt_id": "1"}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var text string
		for _, m := range res.Messages {
			if tc, ok := m.Content.(*mcp.TextContent); ok {
				text += strings.ToLower(tc.Text)
			}
		}
		for _, cloudOnly := range []string{"get_sentiment", "list_segments", "get_perception", "get_fanouts"} {
			if strings.Contains(text, cloudOnly) {
				t.Errorf("%s sends the model at the Cloud-only tool %q", name, cloudOnly)
			}
		}
	}
}

func TestExportDataReturnsAWholePayload(t *testing.T) {
	s, _ := connect(t)
	res := call(t, s, "export_data", nil)
	if res.IsError {
		t.Fatal(resultText(res))
	}
	out := res.StructuredContent.(map[string]any)

	if complete, _ := out["complete"].(bool); !complete {
		t.Fatalf("a one-answer instance should fit inline: %v", out["note"])
	}
	counts, _ := out["counts"].(map[string]any)
	for table, want := range map[string]float64{
		"competitors": 1, "prompts": 1, "targets": 1, "chats": 1,
		"mentions": 2, "citations": 1, "fanout": 1,
	} {
		if counts[table] != want {
			t.Errorf("counts[%s] = %v, want %v", table, counts[table], want)
		}
	}
	if _, ok := out["payload"]; !ok {
		t.Error("a complete export carries its payload")
	}
}

// TestExportDataRefusesCSVRatherThanReturningNothing: a directory of files
// cannot come back through a tool call, and saying so with the command to run
// beats returning an empty success.
func TestExportDataRefusesCSVRatherThanReturningNothing(t *testing.T) {
	s, _ := connect(t)
	res := call(t, s, "export_data", map[string]any{"format": "csv"})
	if !res.IsError {
		t.Fatal("csv was accepted through a tool call")
	}
	if !strings.Contains(resultText(res), "limelit export --format csv") {
		t.Errorf("the refusal does not say what to run: %s", resultText(res))
	}
}

func TestExportDataHonoursSince(t *testing.T) {
	s, _ := connect(t)
	res := call(t, s, "export_data", map[string]any{"since": "2099-01-01"})
	out := res.StructuredContent.(map[string]any)
	counts, _ := out["counts"].(map[string]any)

	if counts["chats"] != float64(0) {
		t.Errorf("chats = %v past the since date, want 0", counts["chats"])
	}
	// Configuration is not time series and survives, or an export scoped to
	// last week would describe an instance that tracks nothing.
	if counts["prompts"] == float64(0) {
		t.Error("prompts were filtered away by a date filter")
	}
}

// TestUpgradeWithoutAKeyExplainsRatherThanFailing. An agent that reaches for
// this tool is usually answering "can you do sentiment", and the useful reply
// is the boundary plus how to cross it, not an error.
func TestUpgradeWithoutAKeyExplainsRatherThanFailing(t *testing.T) {
	s, _ := connect(t)
	res := call(t, s, "upgrade_to_cloud", nil)
	if res.IsError {
		t.Fatalf("calling it without a key errored: %s", resultText(res))
	}
	out := res.StructuredContent.(map[string]any)

	if moved, _ := out["moved"].(bool); moved {
		t.Fatal("it reported moving data without a key")
	}
	adds, _ := out["cloud_adds"].([]any)
	if len(adds) == 0 {
		t.Error("it does not say what Cloud adds, which is the whole reason to call it")
	}
	if howTo, _ := out["how_to"].(string); !strings.Contains(howTo, "limelit.co/settings") {
		t.Errorf("it does not say where to get a key: %q", out["how_to"])
	}
	// Nothing about a move should appear when nothing moved.
	for _, absent := range []string{"chats_imported", "workspace_url", "switch_to"} {
		if _, present := out[absent]; present {
			t.Errorf("%q is set on a call that moved nothing", absent)
		}
	}
}

// TestUpgradeToolNamesTheBoundaryHonestly: the list is what an agent reads
// when a user asks for something this server cannot measure, so it must match
// the tools that are genuinely absent.
func TestUpgradeToolNamesTheBoundaryHonestly(t *testing.T) {
	joined := strings.ToLower(strings.Join(cloudAdds, " | "))
	for _, want := range []string{"sentiment", "segments", "portfolios", "search console", "teams"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the Cloud list does not mention %q, which this server has no tool for", want)
		}
	}
	// Fan-out capture shipped in the open core, so the list must not claim
	// it as Cloud-only.
	if strings.Contains(joined, "fan-out capture") {
		t.Error("the list claims fan-out capture is Cloud-only; the open core captures it")
	}
}

func TestHTTPTokenSourceIsReadPerRequest(t *testing.T) {
	// Rotating the token in Settings must take effect on the next request.
	// A handler that copied the token at construction would keep honouring
	// the old one after the screen said it was gone.
	srv, err := New(Deps{DB: openEmpty(t)})
	if err != nil {
		t.Fatal(err)
	}
	current := ""
	h := Handler(srv, func() string { return current }, nil)

	try := func(bearer string) int {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		h.ServeHTTP(res, req)
		return res.Code
	}
	if got := try("first"); got != http.StatusServiceUnavailable {
		t.Fatalf("with no token yet = %d, want 503", got)
	}
	current = "first"
	if got := try("first"); got == http.StatusUnauthorized || got == http.StatusServiceUnavailable {
		t.Fatalf("the generated token was refused: %d", got)
	}
	current = "second"
	if got := try("first"); got != http.StatusUnauthorized {
		t.Errorf("the rotated-away token still works: %d", got)
	}
	if got := try("second"); got == http.StatusUnauthorized || got == http.StatusServiceUnavailable {
		t.Errorf("the new token was refused: %d", got)
	}
	current = ""
	if got := try("second"); got != http.StatusServiceUnavailable {
		t.Errorf("after forgetting the token = %d, want 503", got)
	}
}
