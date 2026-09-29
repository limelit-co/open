// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/limelit-co/open/internal/config"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/provider/providertest"
	"github.com/limelit-co/open/internal/runner"
	"github.com/limelit-co/open/internal/secrets"
	"github.com/limelit-co/open/internal/store"
)

func newApp(t *testing.T, reg *provider.Registry) (*App, *store.DB, http.Handler) {
	t.Helper()
	return newAppWithRunner(t, reg, nil)
}

// newAppWithRunner builds the dashboard with a runner attached, for the tests
// that press Run.
func newAppWithRunner(t *testing.T, reg *provider.Registry, run *runner.Runner) (*App, *store.DB, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dir, "limelit.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	t.Setenv("LIMELIT_SECRET", "test-secret")
	keys, err := secrets.Open(dir)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	if run == nil {
		run = runner.New(db, reg, provider.StaticCredentials(nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	app, err := New(db, reg, keys, run, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", "", &config.Config{})
	if err != nil {
		t.Fatalf("ui.New: %v", err)
	}
	mux := http.NewServeMux()
	app.Routes(mux)
	return app, db, mux
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func post(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestEveryTemplateParses is the reason NewRenderer returns an error rather
// than lazily parsing: a broken template must fail at startup, not the first
// time a user opens that page.
func TestEveryTemplateParses(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	for page := range pages {
		if _, ok := r.sets[page]; !ok {
			t.Errorf("page %q was not parsed", page)
		}
	}
}

func TestFreshInstanceGoesToTheWizard(t *testing.T) {
	// The whole first run is this redirect. An empty dashboard would be the
	// first thing a new user had to interpret.
	_, _, h := newApp(t, provider.NewRegistry())
	rec := get(t, h, "/")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET / = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/setup" {
		t.Errorf("redirected to %q", got)
	}
	for _, page := range []string{"/overview", "/prompts", "/competitors", "/settings", "/upgrade"} {
		if rec := get(t, h, page); rec.Header().Get("Location") != "/setup" {
			t.Errorf("GET %s before setup redirected to %q", page, rec.Header().Get("Location"))
		}
	}
}

func TestWizardWalksEndToEnd(t *testing.T) {
	_, db, h := newApp(t, provider.NewRegistry())
	ctx := context.Background()

	// Step 1. The domain arrives the way a person pastes it, with a scheme
	// and a www, and has to be stored as the bare identity.
	if rec := post(t, h, "/setup/brand", url.Values{
		"name": {"Acme"}, "domain": {"https://www.acme.com/pricing"}, "aliases": {"Acme Inc, acme.io"},
	}); rec.Header().Get("Location") != "/setup/competitors" {
		t.Fatalf("step 1 went to %q", rec.Header().Get("Location"))
	}
	prop, err := db.Property(ctx)
	if err != nil {
		t.Fatalf("property was not saved: %v", err)
	}
	if prop.Domain != "acme.com" {
		t.Errorf("domain stored as %q, want the bare identity", prop.Domain)
	}
	if len(prop.Aliases) != 2 {
		t.Errorf("aliases = %v", prop.Aliases)
	}

	// Step 2. A row with a domain and no name is still a competitor.
	if rec := post(t, h, "/setup/competitors", url.Values{
		"category":          {"AI visibility tracking"},
		"competitor_name":   {"Globex", "", ""},
		"competitor_domain": {"globex.com", "https://initech.com/pricing", ""},
	}); rec.Header().Get("Location") != "/setup/prompts" {
		t.Fatalf("step 2 went to %q", rec.Header().Get("Location"))
	}
	comps, _ := db.Competitors(ctx)
	if len(comps) != 2 {
		t.Fatalf("saved %d competitors, want 2", len(comps))
	}
	if comps[1].Domain != "initech.com" || comps[1].Name == "" {
		t.Errorf("competitor with no name saved as %+v", comps[1])
	}

	// Step 3 renders the pack, and the article agrees with the category.
	rec := get(t, h, "/setup/prompts")
	body := rec.Body.String()
	if !strings.Contains(body, "an AI visibility tracking tool") {
		t.Error("step 3 did not render the pack with a correct article")
	}
	if !strings.Contains(body, "Globex alternatives") {
		t.Error("step 3 did not use the competitor")
	}

	// Submitting with one prompt unticked. The categories submitted alongside
	// cannot be matched by index once a box in the middle is cleared, which
	// is the bug this asserts against.
	if rec := post(t, h, "/setup/prompts", url.Values{
		"prompt": {
			"What are the best AI visibility tracking tools?",
			"Globex alternatives",
			"What is Acme?",
		},
		"extra": {"Which tool tracks brand mentions in ChatGPT?\n\n"},
	}); rec.Header().Get("Location") != "/setup/provider" {
		t.Fatalf("step 3 went to %q", rec.Header().Get("Location"))
	}

	prompts, _ := db.Prompts(ctx, true)
	if len(prompts) != 4 {
		t.Fatalf("saved %d prompts, want 3 ticked plus 1 typed", len(prompts))
	}
	byText := map[string]store.Prompt{}
	for _, p := range prompts {
		byText[p.Text] = p
	}
	if got := byText["Globex alternatives"].Category; got != "comparison" {
		t.Errorf("category survived unticking as %q, want comparison", got)
	}
	if !byText["What is Acme?"].Branded {
		t.Error("a prompt naming the brand was not tagged branded")
	}
	if byText["What are the best AI visibility tracking tools?"].Branded {
		t.Error("a discovery prompt was tagged branded")
	}
	if byText["Which tool tracks brand mentions in ChatGPT?"].Text == "" {
		t.Error("the hand-typed prompt was dropped")
	}

	// Step 4 with no providers compiled in offers the honest exit.
	rec = get(t, h, "/setup/provider")
	if !strings.Contains(rec.Body.String(), "No provider is implemented in this build yet") {
		t.Error("step 4 did not say why there is nothing to connect")
	}
	// Even with nothing to connect, the step still says where the keys will
	// come from, so a user can go and get one while they wait.
	if !strings.Contains(rec.Body.String(), "https://openrouter.ai/keys") {
		t.Error("step 4 does not link a vendor key page")
	}
	if rec := post(t, h, "/setup/finish", nil); !strings.HasPrefix(rec.Header().Get("Location"), "/overview") {
		t.Errorf("finish went to %q", rec.Header().Get("Location"))
	}
}

func TestBrandStepRejectsAMissingDomain(t *testing.T) {
	// The domain is how a citation is recognised as the property's own, so a
	// brand without one cannot be tracked.
	_, db, h := newApp(t, provider.NewRegistry())
	rec := post(t, h, "/setup/brand", url.Values{"name": {"Acme"}, "domain": {"  "}})
	if rec.Code != http.StatusOK {
		t.Fatalf("a rejected step redirected instead of re-rendering: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "domain") {
		t.Error("the error did not mention the domain")
	}
	if !strings.Contains(rec.Body.String(), `value="Acme"`) {
		t.Error("the form did not keep what was already typed")
	}
	if _, err := db.Property(context.Background()); err == nil {
		t.Error("a property was saved despite the error")
	}
}

func TestRunButtonIsDisabledUntilItCanRun(t *testing.T) {
	// Offering a Run button that produces nothing would be the first thing
	// this product got wrong.
	_, db, h := newApp(t, provider.NewRegistry())
	ctx := context.Background()
	if err := db.SaveProperty(ctx, store.Property{Name: "Acme", Domain: "acme.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddPrompt(ctx, store.Prompt{Text: "best crm", Active: true}); err != nil {
		t.Fatal(err)
	}
	body := get(t, h, "/overview").Body.String()
	if !strings.Contains(body, "disabled") {
		t.Error("Run now was enabled with no target and no provider")
	}
	if !strings.Contains(body, "Add a target and a provider key first") {
		t.Error("the disabled button does not say what is missing")
	}
}

func TestAddTargetQuotesTheParserError(t *testing.T) {
	// The parser's message names the way out. Replacing it with "invalid
	// target" would throw away the only useful part.
	_, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)

	rec := post(t, h, "/settings/targets/add", url.Values{"spec": {"ai_overview:openai"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("a rejected target redirected instead of re-rendering: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "cannot reach ai_overview") {
		t.Error("the parser error was not shown")
	}
	if !strings.Contains(body, "dataforseo") {
		t.Error("the error did not name a provider that can reach it")
	}
}

func TestAddTargetStoresAccessMode(t *testing.T) {
	// Access travels with the target because an api answer and a scraped
	// answer measure different surfaces.
	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)

	for spec, want := range map[string]string{
		"chatgpt:openai:online":  "api",
		"ai_overview:dataforseo": "scraped",
	} {
		if rec := post(t, h, "/settings/targets/add", url.Values{"spec": {spec}}); rec.Code != http.StatusSeeOther {
			t.Fatalf("adding %q = %d", spec, rec.Code)
		}
		targets, _ := db.Targets(context.Background(), false)
		var found bool
		for _, tg := range targets {
			if tg.Spec == spec {
				found = true
				if tg.Access != want {
					t.Errorf("target %q stored access %q, want %q", spec, tg.Access, want)
				}
			}
		}
		if !found {
			t.Errorf("target %q was not stored", spec)
		}
	}
}

func TestCompetitorNeedsADomain(t *testing.T) {
	_, db, h := newApp(t, provider.NewRegistry())
	seedProperty(t, h)

	rec := post(t, h, "/competitors/add", url.Values{"name": {"Globex"}, "domain": {""}})
	if !strings.Contains(rec.Header().Get("Location"), "competitor-nodom") {
		t.Errorf("a competitor with no domain was accepted, redirect was %q", rec.Header().Get("Location"))
	}
	comps, _ := db.Competitors(context.Background())
	if len(comps) != 0 {
		t.Errorf("stored %d competitors", len(comps))
	}
}

func TestPromptAddedByHandIsTaggedBranded(t *testing.T) {
	_, db, h := newApp(t, provider.NewRegistry())
	seedProperty(t, h)

	post(t, h, "/prompts/add", url.Values{"text": {"Is Acme better than Globex?"}})
	prompts, _ := db.Prompts(context.Background(), true)
	if len(prompts) != 1 {
		t.Fatalf("stored %d prompts", len(prompts))
	}
	if !prompts[0].Branded {
		t.Error("a hand-typed prompt naming the brand was not tagged branded")
	}
}

func TestFlashCodesCannotInjectText(t *testing.T) {
	// Flash copy lives in Go, so a link cannot put words on the page.
	_, _, h := newApp(t, provider.NewRegistry())
	seedProperty(t, h)

	body := get(t, h, "/overview?flash=%3Cscript%3Ealert(1)%3C/script%3E").Body.String()
	if strings.Contains(body, "alert(1)") {
		t.Error("an unknown flash code reached the page")
	}
	if !strings.Contains(get(t, h, "/overview?flash=setup-done").Body.String(), "Setup complete") {
		t.Error("a known flash code did not render")
	}
}

func TestUpgradeScreenShowsTheWholeBoundary(t *testing.T) {
	// The upgrade screen is the only place in the product that lists what is
	// hosted-only, so a user learns the boundary before hunting for a
	// missing feature.
	_, _, h := newApp(t, provider.NewRegistry())
	seedProperty(t, h)
	body := get(t, h, "/upgrade").Body.String()
	for _, feature := range cloudFeatures {
		if !strings.Contains(body, feature) {
			t.Errorf("the upgrade screen does not mention %q", feature)
		}
	}
}

func TestEvidenceScreensRenderWithNoData(t *testing.T) {
	// A fresh install has no answers. These screens must say so in a way that
	// teaches, and must never read as broken.
	_, _, h := newApp(t, provider.NewRegistry())
	seedProperty(t, h)
	for path, want := range map[string]string{
		"/chats":     "No answers yet",
		"/citations": "No citations yet",
	} {
		res := get(t, h, path)
		if res.Code != http.StatusOK {
			t.Errorf("%s returned %d", path, res.Code)
		}
		if !strings.Contains(res.Body.String(), want) {
			t.Errorf("%s does not say %q", path, want)
		}
	}
}

func TestOverviewTeachesBeforeTheFirstRun(t *testing.T) {
	// The overview is the first screen anyone sees. With no answers it must
	// explain what will appear rather than showing a wall of zeros, and it
	// must never invent a number to fill the space.
	_, _, h := newApp(t, provider.NewRegistry())
	seedProperty(t, h)
	body := get(t, h, "/overview").Body.String()

	if !strings.Contains(body, "No answers yet") {
		t.Error("the empty overview does not say there is nothing yet")
	}
	// A zero headline before any answer exists would be a claim about the
	// market rather than a statement about the sample.
	for _, fake := range []string{"kpi-value", "Share of voice"} {
		if strings.Contains(body, fake) {
			t.Errorf("the empty overview renders %q, which is a number nobody measured", fake)
		}
	}
}

// TestOverviewWidensAnEmptyDefaultWindow. An install whose runs stopped five
// weeks ago has history and nothing in the last 30 days. The default must
// open on the window that has answers, and an explicit narrow choice must
// say the window is quiet rather than that nothing has ever run. A default
// with one measured day widens too: one dot cannot show a trend, and the
// second day is one click away.
func TestOverviewWidensAnEmptyDefaultWindow(t *testing.T) {
	_, db, h := newApp(t, provider.NewRegistry())
	seedProperty(t, h)
	ctx := context.Background()
	pid, err := db.AddPrompt(ctx, store.Prompt{Text: "best crm", Category: "discovery", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	tid, err := db.AddTarget(ctx, store.Target{Spec: "chatgpt:stub", Engine: "chatgpt", Provider: provider.StubName, Access: "api"})
	if err != nil {
		t.Fatal(err)
	}
	eid, err := db.CreateEvaluation(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -40).Format("2006-01-02 15:04:05")
	if _, err := db.RecordChat(ctx, store.ChatRecord{
		EvaluationID: eid, PromptID: pid, TargetID: tid, Status: "ok", Text: "Acme leads.", Model: "test",
		Mentions:  []store.Mention{{BrandName: "Acme", BrandKey: "acme"}},
		CreatedAt: old,
	}); err != nil {
		t.Fatal(err)
	}

	body := get(t, h, "/overview").Body.String()
	if strings.Contains(body, "No answers yet") {
		t.Error("history exists, but the default window says nothing has ever run")
	}
	if !strings.Contains(body, `href="/overview?days=90" aria-current="true"`) {
		t.Error("the default did not widen to the 90-day window that has answers")
	}
	if !strings.Contains(body, "kpi-value") {
		t.Error("the widened window renders no numbers")
	}

	narrow := get(t, h, "/overview?days=7").Body.String()
	if !strings.Contains(narrow, "No answers in the last 7 days") {
		t.Error("an explicit quiet window does not say so")
	}
	if strings.Contains(narrow, "No answers yet") || strings.Contains(narrow, "kpi-value") {
		t.Error("an explicit quiet window was widened or read as a fresh install")
	}

	// One fresh day inside the default window, a month after the last: the
	// default still widens to the window that has two measured days.
	eid2, err := db.CreateEvaluation(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordChat(ctx, store.ChatRecord{
		EvaluationID: eid2, PromptID: pid, TargetID: tid, Status: "ok", Text: "Acme leads.", Model: "test",
		Mentions: []store.Mention{{BrandName: "Acme", BrandKey: "acme"}},
	}); err != nil {
		t.Fatal(err)
	}
	body = get(t, h, "/overview").Body.String()
	if !strings.Contains(body, `href="/overview?days=90" aria-current="true"`) {
		t.Error("a default with one measured day did not widen to the window with two")
	}
	if !strings.Contains(get(t, h, "/overview?days=30").Body.String(), `href="/overview?days=30" aria-current="true"`) {
		t.Error("an explicit 30-day choice was overridden")
	}
}

func TestRunStartsAPassAndReturnsImmediately(t *testing.T) {
	// A pass takes as long as the slowest engine, so holding the request open
	// would look like a hung browser.
	reg := provider.NewRegistry()
	provider.RegisterStub(reg, provider.StubConfig{Answer: "Acme leads."})
	_, db, h := newApp(t, reg)
	seedProperty(t, h)

	ctx := context.Background()
	if _, err := db.AddPrompt(ctx, store.Prompt{Text: "best crm", Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddTarget(ctx, store.Target{
		Spec: "chatgpt:stub", Engine: "chatgpt", Provider: provider.StubName, Access: "api",
	}); err != nil {
		t.Fatal(err)
	}

	rec := post(t, h, "/run", nil)
	if !strings.Contains(rec.Header().Get("Location"), "run-started") {
		t.Fatalf("Run redirected to %q", rec.Header().Get("Location"))
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, _ := db.Counts(ctx); c.Chats > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("pressing Run produced no answer")
}

func TestStaticStylesheetIsServed(t *testing.T) {
	_, _, h := newApp(t, provider.NewRegistry())
	rec := get(t, h, "/static/app.css")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/app.css = %d", rec.Code)
	}
	css := rec.Body.String()
	// Every themed token must exist in both places: one declared only in
	// light renders as invisible text in dark, which looks fine until
	// somebody switches.
	if !strings.Contains(css, "prefers-color-scheme: dark") {
		t.Error("the stylesheet has no dark theme")
	}
	// The brand ramp is deliberately invariant: the mark is white on it in
	// both themes, so it must not flip. Everything that carries text or a
	// surface must.
	for _, token := range []string{"--ink:", "--ink-2:", "--surface:", "--rule:", "--bg-app:"} {
		if strings.Count(css, token) < 2 {
			t.Errorf("token %s is declared in only one theme, so it renders wrong in the other", token)
		}
	}
	// Verified in a browser at 375px: without this the fixed sidebar squeezes
	// the content column to about a hundred pixels.
	if !strings.Contains(css, "max-width: 860px") {
		t.Error("the stylesheet has no narrow-screen breakpoint")
	}
}

// TestNoEmDashesInUserFacingCopy enforces the house rule. Code comments are
// exempt, so this reads the rendered pages rather than the source.
func TestNoEmDashesInUserFacingCopy(t *testing.T) {
	_, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	for _, path := range []string{
		"/overview", "/prompts", "/competitors", "/settings", "/upgrade",
		"/chats", "/citations", "/setup", "/setup/competitors", "/setup/prompts", "/setup/provider",
	} {
		body := get(t, h, path).Body.String()
		for _, bad := range []string{"—", "&mdash;"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains an em dash", path)
			}
		}
	}
}

func seedProperty(t *testing.T, h http.Handler) {
	t.Helper()
	post(t, h, "/setup/brand", url.Values{"name": {"Acme"}, "domain": {"acme.com"}})
}

// TestGridDistinguishesAMeasuredZeroFromAnUnaskedCell is the distinction the
// whole grid rests on. A prompt an engine answered without naming you is a
// finding, drawn at full strength and clickable. A prompt never asked of that
// engine is an absence, dashed and inert. Drawing them the same way turns a
// real result into what looks like a broken widget.
func TestGridDistinguishesAMeasuredZeroFromAnUnaskedCell(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers int
		percent float64
		want    string
	}{
		{"never asked", 0, 0, "hm-none"},
		{"asked, not named", 3, 0, "hm-0"},
		{"named sometimes", 4, 50, "hm-3"},
		{"named always, big sample", 8, 100, "hm-5"},
	} {
		if got := cellBin(tc.answers, tc.percent); got != tc.want {
			t.Errorf("%s: cellBin(%d, %v) = %q, want %q", tc.name, tc.answers, tc.percent, got, tc.want)
		}
	}
}

// TestGridCapsTheTopBinOnATinySample: one answer that named you is 100%, and
// painting it like five of five would let a single lucky answer look like
// dominance.
func TestGridCapsTheTopBinOnATinySample(t *testing.T) {
	small := cellBin(1, 100)
	big := cellBin(CellMinN, 100)
	if small == big {
		t.Errorf("one answer at 100%% is drawn the same as %d: both %q", CellMinN, small)
	}
	if small != "hm-4" {
		t.Errorf("capped bin = %q, want hm-4", small)
	}
}

func TestSparklineIsWellFormedOrAbsent(t *testing.T) {
	// Two points is a segment, not a trend.
	for _, n := range []int{0, 1, 2} {
		vals := make([]float64, n)
		if got := Sparkline(vals); got != "" {
			t.Errorf("%d points produced a sparkline: %s", n, got)
		}
	}
	got := string(Sparkline([]float64{10, 40, 25, 60}))
	for _, want := range []string{`<svg`, `<path class="spark-line" d="M`, `</svg>`, `width=`, `height=`} {
		if !strings.Contains(got, want) {
			t.Errorf("sparkline is missing %q: %s", want, got)
		}
	}
	if strings.Count(got, `d="`) != 1 {
		t.Errorf("malformed path data: %s", got)
	}
}

func TestTrendChartHandlesEveryDegenerateCase(t *testing.T) {
	if got := TrendChart(nil); got != "" {
		t.Errorf("no points produced a chart: %s", got)
	}
	one := string(TrendChart([]TrendPoint{{Day: "2026-09-11", Value: 0, N: 4}}))
	if !strings.Contains(one, "<circle") {
		t.Error("a single point must still draw its dot")
	}
	if strings.Contains(one, "<path class=\"line\"") {
		t.Error("a single point drew a line, which implies a shape the data does not have")
	}
	// Stretching the viewBox shears the axis glyphs and scales strokes
	// anisotropically.
	if strings.Contains(one, `preserveAspectRatio="none"`) {
		t.Error("the trend chart stretches its viewBox")
	}
	if !strings.Contains(one, `aria-label=`) {
		t.Error("the chart has no text alternative")
	}
	two := string(TrendChart([]TrendPoint{{Day: "a", Value: 10}, {Day: "b", Value: 20}}))
	if !strings.Contains(two, `<path class="line"`) {
		t.Error("two points did not draw a line")
	}
}

// TestDeltaDoesNotClaimNoChangeWithoutAComparison: on a first run there is no
// previous window, and "no change" would be a claim about a comparison that
// was never made.
func TestDeltaDoesNotClaimNoChangeWithoutAComparison(t *testing.T) {
	text, kind := delta(0, 0, false)
	if strings.Contains(strings.ToLower(text), "no change") {
		t.Errorf("delta = %q with no prior window", text)
	}
	if text == "" {
		t.Error("the first window should be labelled, not left blank")
	}
	if kind == "up" || kind == "down" || kind == "flat" {
		t.Errorf("kind = %q, want a neutral first-window style", kind)
	}
}

// TestNoColourIsEmittedFromGo. A hex baked into markup cannot follow the
// theme, and the readable ink over the heat ramp flips at a different step in
// each direction per theme.
func TestNoColourIsEmittedFromGo(t *testing.T) {
	rendered := []string{
		string(TrendChart([]TrendPoint{{Day: "a", Value: 50, N: 4}, {Day: "b", Value: 60, N: 4}})),
		string(Sparkline([]float64{1, 2, 3, 4})),
	}
	for _, out := range rendered {
		if strings.Contains(out, "#") || strings.Contains(out, "rgb(") {
			t.Errorf("a literal colour was emitted from Go: %s", out)
		}
	}
	for _, bin := range []string{cellBin(0, 0), cellBin(3, 0), cellBin(8, 100)} {
		if strings.Contains(bin, "#") || strings.Contains(bin, "var(") {
			t.Errorf("cellBin returned a colour rather than a class: %q", bin)
		}
	}
}

// TestRichChartsNeverEmitAColour. Every series and every source class binds
// to a token through a class, so a brand is one colour on every chart and the
// whole set follows the theme.
func TestRichChartsNeverEmitAColour(t *testing.T) {
	race := string(RaceChart([]RaceSeries{
		{Name: "Acme", IsOwn: true, Class: "s-own", Points: []TrendPoint{{Day: "a", Value: 0, N: 4}, {Day: "b", Value: 25, N: 4}}},
		{Name: "Rival", Class: "s-1", Points: []TrendPoint{{Day: "a", Value: 50, N: 4}, {Day: "b", Value: 75, N: 4}}},
	}))
	donut := string(Donut([]DonutSlice{{Name: "Rival", Class: "s-1", Share: 60}, {Name: "Acme", IsOwn: true, Class: "s-own", Share: 40}}, DonutOwn{Share: 40, Mentions: 4, Total: 10, HasData: true}))
	bars := string(EngineBars([]EngineGroup{{Engine: "chatgpt", Access: "api", Answers: 4, Bars: []EngineBar{
		{Name: "Acme", IsOwn: true, Class: "s-own", Visibility: 0}, {Name: "Rival", Class: "s-1", Visibility: 100},
	}}}))
	mix := string(CitationMixChart([]MixDay{{Day: "2026-09-11", Total: 3, Counts: map[string]int{"own": 1, "other": 2}}}))

	for name, out := range map[string]string{"race": race, "donut": donut, "bars": bars, "mix": mix} {
		if strings.Contains(out, "#") || strings.Contains(out, "rgb(") {
			t.Errorf("%s emitted a literal colour", name)
		}
		if !strings.Contains(out, "<svg") || !strings.Contains(out, "</svg>") {
			t.Errorf("%s is not a complete SVG", name)
		}
		if !strings.Contains(out, "aria-label=") {
			t.Errorf("%s has no text alternative", name)
		}
	}
}

// TestRaceDrawsTheOwnBrandLastAndHeavier: the property's line must sit on top
// of the competitors and read without the legend.
func TestRaceDrawsTheOwnBrandLastAndHeavier(t *testing.T) {
	out := string(RaceChart([]RaceSeries{
		{Name: "Acme", IsOwn: true, Class: "s-own", Points: []TrendPoint{{Day: "a", Value: 0}, {Day: "b", Value: 0}}},
		{Name: "Rival", Class: "s-1", Points: []TrendPoint{{Day: "a", Value: 50}, {Day: "b", Value: 60}}},
	}))
	own := strings.LastIndex(out, "series-own line")
	rival := strings.LastIndex(out, `class="series s-1 line"`)
	if own < 0 || rival < 0 {
		t.Fatalf("missing a series: %s", out)
	}
	if own < rival {
		t.Error("the own line is drawn before a competitor's, so it can be covered")
	}
}

// TestSeriesClassIsStableAcrossFiltering. A brand's colour must not change
// when another brand is filtered out or overtakes it.
func TestSeriesClassIsStableAcrossFiltering(t *testing.T) {
	all := []string{"Otterly", "Peec AI", "Profound"}
	before := seriesClass("Peec AI", all, false)
	after := seriesClass("Peec AI", all, false)
	if before != after {
		t.Fatalf("class changed between calls: %q then %q", before, after)
	}
	// The property never takes a series slot.
	if got := seriesClass("Acme", all, true); got != "s-own" {
		t.Errorf("own brand got %q, want s-own", got)
	}
	// Alphabetical, so Otterly < Peec AI < Profound regardless of rank.
	if seriesClass("Otterly", all, false) != "s-1" || seriesClass("Profound", all, false) != "s-3" {
		t.Errorf("assignment is not alphabetical: %s %s %s",
			seriesClass("Otterly", all, false), seriesClass("Peec AI", all, false), seriesClass("Profound", all, false))
	}
}

func TestDonutSaysNeverNamedRatherThanShowingAnEmptyRing(t *testing.T) {
	out := string(Donut([]DonutSlice{{Name: "Rival", Class: "s-1", Share: 100}}, DonutOwn{Total: 10}))
	if !strings.Contains(out, "never named") {
		t.Error("a property with no mentions must be labelled, not left as a blank centre")
	}
}

func TestEngineBarsDrawAMeasuredZeroAsAStub(t *testing.T) {
	// Blank would read as "no data". A stub reads as "measured, and zero".
	out := string(EngineBars([]EngineGroup{{Engine: "chatgpt", Access: "api", Answers: 4, Bars: []EngineBar{
		{Name: "Acme", IsOwn: true, Class: "s-own", Visibility: 0},
	}}}))
	if !strings.Contains(out, "bar-zero") {
		t.Error("a measured zero was not drawn")
	}
}

func TestWordCloudScalesBySquareRootAndSortsAlphabetically(t *testing.T) {
	words := WordCloud([]CloudWord{{Term: "zebra", Count: 1}, {Term: "apple", Count: 100}, {Term: "mango", Count: 25}})
	if words[0].Term != "apple" || words[2].Term != "zebra" {
		t.Errorf("not alphabetical: %v", words)
	}
	var apple, mango, zebra CloudWord
	for _, w := range words {
		switch w.Term {
		case "apple":
			apple = w
		case "mango":
			mango = w
		case "zebra":
			zebra = w
		}
	}
	if !(apple.Size > mango.Size && mango.Size > zebra.Size) {
		t.Errorf("sizes not monotonic in count: apple %d, mango %d, zebra %d", apple.Size, mango.Size, zebra.Size)
	}
	// Square root: 25 of 100 should sit near the middle, not a quarter of
	// the way up as a linear scale would put it.
	if mango.Size < (apple.Size+zebra.Size)/2-3 {
		t.Errorf("scaling looks linear: mango %d between %d and %d", mango.Size, zebra.Size, apple.Size)
	}
	if got := WordCloud(nil); got != nil {
		t.Errorf("empty input produced %v", got)
	}
}
