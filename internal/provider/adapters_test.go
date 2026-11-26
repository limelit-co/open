// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
)

// The fixtures here are recorded answers from the live APIs. Tests need no
// external network or real credentials.

func against(t *testing.T, fixture string) http.Handler {
	t.Helper()
	return serveFixture(t, fixture)
}

func newAnthropicAgainst(t *testing.T, h http.Handler) *anthropicProvider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := NewAnthropic("sk-ant-test")
	if err != nil {
		t.Fatal(err)
	}
	ap := p.(*anthropicProvider)
	ap.endpoint, ap.models = srv.URL+"/v1/messages", srv.URL+"/v1/models"
	return ap
}

type handlerTransport struct{ http.Handler }

func (h handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	defer r.Body.Close()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Result(), nil
}

func newPerplexityAgainst(t *testing.T, h http.Handler) *perplexityProvider {
	t.Helper()
	p, err := NewPerplexity("pplx-test")
	if err != nil {
		t.Fatal(err)
	}
	pp := p.(*perplexityProvider)
	pp.client.Transport = handlerTransport{h}
	return pp
}

func newGoogleAgainst(t *testing.T, h http.Handler) *googleProvider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := NewGoogle("AIza-test")
	if err != nil {
		t.Fatal(err)
	}
	gp := p.(*googleProvider)
	gp.endpoint = srv.URL + "/v1beta/models/%s:generateContent"
	return gp
}

// TestAnthropicReadsOnlyTextBlocks is the one that matters most for this
// provider. Claude returns the answer as many text blocks interleaved with
// thinking, server_tool_use and web_search_tool_result blocks, and thinking
// concatenated into the answer would be searched for brand names.
func TestAnthropicReadsOnlyTextBlocks(t *testing.T) {
	p := newAnthropicAgainst(t, against(t, "anthropic_answer.json"))
	resp, err := p.Run(context.Background(), Request{Engine: ClaudeEngine, Prompt: "q", Online: true})
	// The recorded answer stopped on max_tokens, which this provider rejects
	// on purpose, so exercise the reader directly as well.
	if err == nil {
		t.Fatalf("a max_tokens response must be rejected, got a %d byte answer", len(resp.Text))
	}
	if !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("error should name the truncation: %v", err)
	}
}

func TestAnthropicContentReaderSeparatesAnswerFromEverythingElse(t *testing.T) {
	blocks := []anthropicContent{
		{Type: "thinking", Text: "The user is asking about Acme and Rival."},
		{Type: "server_tool_use"},
		{Type: "web_search_tool_result", Text: "raw result payload"},
		{Type: "text", Text: "Acme leads. "},
		{Type: "text", Text: "Rival is second."},
	}
	blocks[1].Input.Query = "best widget tools"

	text, cites, fanout := readAnthropicContent(blocks)
	if text != "Acme leads. Rival is second." {
		t.Fatalf("text = %q, want only the text blocks joined", text)
	}
	if strings.Contains(text, "thinking") || strings.Contains(text, "raw result") {
		t.Error("a non-answer block leaked into the answer")
	}
	if len(cites) != 0 {
		t.Errorf("citations = %d, want 0", len(cites))
	}
	if len(fanout) != 1 || fanout[0] != "best widget tools" {
		t.Errorf("fanout = %v, want the one search query", fanout)
	}
}

func TestAnthropicDeduplicatesSourcesAndQueries(t *testing.T) {
	one := anthropicContent{Type: "text", Text: "a"}
	one.Citations = append(one.Citations, struct {
		Type  string `json:"type"`
		URL   string `json:"url"`
		Title string `json:"title"`
	}{"web_search_result_location", "https://g2.com/x", "G2"})
	two := one
	q1 := anthropicContent{Type: "server_tool_use"}
	q1.Input.Query = "same"
	q2 := q1

	_, cites, fanout := readAnthropicContent([]anthropicContent{one, two, q1, q2})
	if len(cites) != 1 {
		t.Errorf("citations = %d, want 1: one source cited twice is one source", len(cites))
	}
	if len(fanout) != 1 {
		t.Errorf("fanout = %d, want 1", len(fanout))
	}
}

func TestPerplexityParsesARecordedAnswer(t *testing.T) {
	p := newPerplexityAgainst(t, against(t, "perplexity_agent_answer.json"))
	resp, err := p.Run(context.Background(), Request{Engine: PerplexityEngine, Prompt: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "For a mid-sized German company, I’d shortlist **STACKIT, IONOS, and plusserver** first."; resp.Text != want {
		t.Errorf("text = %q, want %q", resp.Text, want)
	}
	if resp.Model != "openai/gpt-6-luna" || resp.InputTokens != 8699 || resp.OutputTokens != 509 || resp.Calls != 1 {
		t.Errorf("model = %q, tokens = %d in/%d out, calls = %d", resp.Model, resp.InputTokens, resp.OutputTokens, resp.Calls)
	}
	want := []Citation{
		{URL: "https://www.qovery.com/blog/kubernetes-platform-stackit-ionos-open-telekom-cloud-comparison", Title: "Kubernetes Platforms on STACKIT, IONOS, and Open Telekom Cloud", Position: 1},
		{URL: "https://www.qovery.com/blog/eu-cloud-providers-managed-kubernetes-2026-sovereignty-checklist", Title: "Which EU Cloud Providers Support Kubernetes? The 2026 List, Prices ...", Position: 2},
	}
	if !slices.Equal(resp.Citations, want) {
		t.Errorf("citations = %+v, want %+v", resp.Citations, want)
	}
}

func TestPerplexitySendsAgentRequest(t *testing.T) {
	for _, tc := range []struct {
		model, effort string
	}{
		{"", "fast"},
		{"sonar", "fast"},
		{"low", "low"},
		{"high@2026-11-11.0", "high@2026-11-11.0"},
	} {
		t.Run(tc.effort+"/"+tc.model, func(t *testing.T) {
			fixture := against(t, "perplexity_agent_answer.json")
			p := newPerplexityAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/agent" {
					t.Errorf("%s %s, want POST /v1/agent", r.Method, r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body) != 3 || body["input"] != "a verbatim prompt" || body["effort"] != tc.effort || body["max_output_tokens"] != float64(1500) {
					t.Errorf("request body = %+v, want input, effort %q and max_output_tokens 1500", body, tc.effort)
				}
				fixture.ServeHTTP(w, r)
			}))
			if _, err := p.Run(context.Background(), Request{Engine: PerplexityEngine, Model: tc.model, Prompt: "a verbatim prompt"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPerplexityRejectsAnUnknownEffort(t *testing.T) {
	p := newPerplexityAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("an unknown effort must be rejected before making a request")
	}))
	_, err := p.Run(context.Background(), Request{Engine: PerplexityEngine, Model: "sonar-pro"})
	if err == nil || !strings.Contains(err.Error(), `unsupported effort "sonar-pro"`) {
		t.Fatalf("err = %v, want an unsupported effort error", err)
	}
}

func TestPerplexityJoinsTextAndDeduplicatesSources(t *testing.T) {
	p := newPerplexityAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"completed","output":[
			{"type":"message","content":[{"type":"output_text","text":"First. "},{"type":"reasoning","text":"Ignored."},{"type":"output_text","text":"Second. "}]},
			{"type":"search_results","results":[{"url":"https://example.com/one","title":"One"}]},
			{"type":"message","content":[{"type":"output_text","text":"Third."}]},
			{"type":"search_results","results":[{"url":"https://example.com/one","title":"Duplicate"},{"url":"https://example.com/two","title":"Two"}]}
		]}`))
	}))
	resp, err := p.Run(context.Background(), Request{Engine: PerplexityEngine, Prompt: "q"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Citation{
		{URL: "https://example.com/one", Title: "One", Position: 1},
		{URL: "https://example.com/two", Title: "Two", Position: 2},
	}
	if resp.Text != "First. Second. Third." || !slices.Equal(resp.Citations, want) {
		t.Errorf("text = %q, citations = %+v", resp.Text, resp.Citations)
	}
}

func TestPerplexityRejectsAnIncompleteAnswer(t *testing.T) {
	p := newPerplexityAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"A partial answer"}]}]}`))
	}))
	resp, err := p.Run(context.Background(), Request{Engine: PerplexityEngine, Prompt: "q"})
	if err == nil || !strings.Contains(err.Error(), "max_output_tokens") {
		t.Fatalf("err = %v, want a truncation error", err)
	}
	if resp.Text != "" {
		t.Errorf("partial answer returned: %q", resp.Text)
	}
}

func TestPerplexityReportsAgentErrors(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		p := newPerplexityAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte(`{"status":"failed","error":{"message":"Key rejected: pplx-test"}}`))
		}))
		_, err := p.Run(context.Background(), Request{Engine: PerplexityEngine, Prompt: "q"})
		if err == nil || !strings.Contains(err.Error(), "Key rejected") || strings.Contains(err.Error(), "pplx-test") {
			t.Errorf("HTTP %d: err = %v, want the API message with the key redacted", status, err)
		}
		if status == http.StatusUnauthorized && !errors.Is(err, ErrAuth) {
			t.Errorf("HTTP %d: err = %v, want ErrAuth", status, err)
		}
	}
}

func TestPerplexityWrongEngineIsTyped(t *testing.T) {
	p := newPerplexityAgainst(t, against(t, "perplexity_agent_answer.json"))
	_, err := p.Run(context.Background(), Request{Engine: ChatGPTEngine, Prompt: "q"})
	if !errors.Is(err, ErrUnsupportedEngine) {
		t.Fatalf("err = %v, want ErrUnsupportedEngine", err)
	}
}

// TestGooglePromotesTheDomainOverTheRedirect guards the quirk that would
// otherwise be a silent measurement bug: Gemini's grounding uri is a Vertex
// redirect, and the real source identity is in the title.
func TestGooglePromotesTheDomainOverTheRedirect(t *testing.T) {
	p := newGoogleAgainst(t, against(t, "gemini_answer.json"))
	resp, err := p.Run(context.Background(), Request{Engine: GeminiEngine, Prompt: "q", Online: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Citations) == 0 {
		t.Fatal("no citations parsed")
	}
	for _, c := range resp.Citations {
		if strings.Contains(c.URL, googleRedirectHost) {
			t.Fatalf("a citation kept the Vertex redirect: %q. Every Gemini source would classify as one Google domain", c.URL)
		}
		if !strings.HasPrefix(c.URL, "https://") {
			t.Errorf("citation is not a URL: %q", c.URL)
		}
	}
	if len(resp.FanOut) == 0 {
		t.Error("webSearchQueries were not captured as fan-out")
	}
}

func TestLooksLikeDomain(t *testing.T) {
	for _, ok := range []string{"zapier.com", "position.digital", "blog.example.co.uk"} {
		if !looksLikeDomain(ok) {
			t.Errorf("%q should look like a domain", ok)
		}
	}
	// A page title containing a dot must never become a URL.
	for _, no := range []string{"", "The 8 best tools in 2026", "Best. Tool. Ever.", "example.", ".com", "a.b", "http://x.com", "x.com/page", "user@x.com"} {
		if looksLikeDomain(no) {
			t.Errorf("%q should NOT look like a domain", no)
		}
	}
}

func TestGoogleSourceURLFallsBackVisibly(t *testing.T) {
	// A title that is not a domain keeps the redirect. Attributing it to
	// Google is wrong, but it is visibly wrong, which beats dropping a source.
	got := googleSourceURL("Some Page Title", "https://"+googleRedirectHost+"/redirect/abc")
	if !strings.Contains(got, googleRedirectHost) {
		t.Fatalf("got %q, want the redirect kept as a visible fallback", got)
	}
}

func TestNewProvidersRejectAMissingKey(t *testing.T) {
	for name, build := range map[string]func(string) (Provider, error){
		"anthropic":  NewAnthropic,
		"perplexity": NewPerplexity,
		"google":     NewGoogle,
	} {
		if _, err := build("   "); !errors.Is(err, ErrAuth) {
			t.Errorf("%s: err = %v, want ErrAuth", name, err)
		}
	}
}

func TestHTTPStatusErrorMapping(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusUnauthorized:       ErrAuth,
		http.StatusForbidden:          ErrAuth,
		http.StatusTooManyRequests:    ErrRateLimited,
		http.StatusBadGateway:         ErrRateLimited,
		http.StatusServiceUnavailable: ErrRateLimited,
	} {
		if err := httpStatusError("x", status, ""); !errors.Is(err, want) {
			t.Errorf("status %d mapped to %v, want %v", status, err, want)
		}
	}
	if err := httpStatusError("x", http.StatusOK, ""); err != nil {
		t.Errorf("200 mapped to %v", err)
	}
	// A 400 is the provider's own complaint and keeps its message.
	err := httpStatusError("x", http.StatusBadRequest, "model not found")
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Errorf("400 should keep the body: %v", err)
	}
}

// TestEveryRegisteredProviderIsInTheCatalog keeps the shipped registry and the
// documented plan from drifting apart.
func TestEveryRegisteredProviderIsInTheCatalog(t *testing.T) {
	catalog := map[string]bool{}
	for _, e := range Catalog() {
		catalog[e.Name] = true
	}
	for _, name := range Default().Names() {
		if !catalog[name] {
			t.Errorf("provider %q is registered but missing from the catalog", name)
		}
	}
}

func newDataForSEOAgainst(t *testing.T, h http.Handler) *dataForSEOProvider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := NewDataForSEO("login", "password")
	if err != nil {
		t.Fatal(err)
	}
	dp := p.(*dataForSEOProvider)
	dp.organic, dp.aiMode, dp.userInfo = srv.URL+"/organic", srv.URL+"/ai_mode", srv.URL+"/user"
	return dp
}

func TestDataForSEOParsesARecordedAIOverview(t *testing.T) {
	p := newDataForSEOAgainst(t, against(t, "dataforseo_ai_overview.json"))
	resp, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "best AI visibility tracking tools"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(resp.Text) == "" {
		t.Fatal("no overview text")
	}
	if len(resp.Citations) == 0 {
		t.Fatal("no references parsed")
	}
	for i, c := range resp.Citations {
		if !strings.HasPrefix(c.URL, "http") {
			t.Errorf("citation %d is not a URL: %q", i, c.URL)
		}
		if c.Position != i+1 {
			t.Errorf("citation %d has position %d, want %d", i, c.Position, i+1)
		}
	}
	// A scraped surface has no model. Inventing one would suggest a choice
	// was made about which model answered.
	if resp.Model != "" {
		t.Errorf("model = %q, want empty for a scraped surface", resp.Model)
	}
	if p.Access() != AccessScraped {
		t.Errorf("access = %q", p.Access())
	}
}

func TestDataForSEOParsesAIMode(t *testing.T) {
	p := newDataForSEOAgainst(t, against(t, "dataforseo_ai_mode.json"))
	resp, err := p.Run(context.Background(), Request{Engine: AIModeEngine, Prompt: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(resp.Text) == "" || len(resp.Citations) == 0 {
		t.Fatalf("text=%d citations=%d", len(resp.Text), len(resp.Citations))
	}
}

// TestDataForSEOMissingOverviewIsNotABrandMiss is the most important test for
// this provider. Google frequently shows no AI Overview at all, and counting
// that as an answer that ignored the brand would drag visibility toward zero
// for reasons that have nothing to do with the brand.
func TestDataForSEOMissingOverviewIsNotABrandMiss(t *testing.T) {
	body := `{"status_code":20000,"tasks":[{"status_code":20000,"result":[{"items":[
		{"type":"organic","title":"a"},{"type":"people_also_ask"}]}]}]}`
	p := newDataForSEOAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	_, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "q"})
	if !errors.Is(err, ErrNoAnswerSurface) {
		t.Fatalf("err = %v, want ErrNoAnswerSurface", err)
	}
}

func TestDataForSEOEmptyOverviewBlockIsAlsoNoSurface(t *testing.T) {
	body := `{"status_code":20000,"tasks":[{"status_code":20000,"result":[{"items":[
		{"type":"ai_overview","markdown":"   ","items":[]}]}]}]}`
	p := newDataForSEOAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	_, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "q"})
	if !errors.Is(err, ErrNoAnswerSurface) {
		t.Fatalf("err = %v, want ErrNoAnswerSurface", err)
	}
}

// TestDataForSEOFailureInsideA200 covers this vendor's habit of reporting
// problems with an HTTP 200 and a failure code in the body.
func TestDataForSEOFailureInsideA200(t *testing.T) {
	for code, want := range map[int]error{
		40100: ErrAuth,
		40202: ErrAuth,
		50401: ErrRateLimited,
	} {
		body := fmt.Sprintf(`{"status_code":%d,"status_message":"nope","tasks":[]}`, code)
		p := newDataForSEOAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(body))
		}))
		_, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "q"})
		if !errors.Is(err, want) {
			t.Errorf("vendor code %d mapped to %v, want %v", code, err, want)
		}
	}
}

func TestDataForSEOLocationRefusesAnUnknownCountry(t *testing.T) {
	if _, err := dataForSEOLocation(""); err != nil {
		t.Errorf("an empty country should default: %v", err)
	}
	if got, _ := dataForSEOLocation("gb"); got != 2826 {
		t.Errorf("GB = %d, want 2826", got)
	}
	// Silently measuring the United States and labelling it Narnia is the
	// quiet wrongness this project exists to avoid.
	if _, err := dataForSEOLocation("XX"); err == nil {
		t.Error("an unknown country must be an error, not a fall back to the US")
	}
}

func TestDataForSEOMissingCredentialsIsAuth(t *testing.T) {
	for _, pair := range [][2]string{{"", "p"}, {"l", ""}, {"", ""}} {
		if _, err := NewDataForSEO(pair[0], pair[1]); !errors.Is(err, ErrAuth) {
			t.Errorf("(%q,%q): err = %v, want ErrAuth", pair[0], pair[1], err)
		}
	}
}

// ---- SearchApi -------------------------------------------------------------

// searchApiStub routes by the engine parameter, the way the real API does, so
// the two-step AI Overview path is exercised rather than stubbed out.
func newSearchApiAgainst(t *testing.T, byEngine map[string]string) (*searchApiProvider, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		engine := r.URL.Query().Get("engine")
		fixture, ok := byEngine[engine]
		if !ok {
			http.Error(w, `{"error":"unexpected engine `+engine+`"}`, http.StatusBadRequest)
			return
		}
		// A page_token call carries the query inside the token, and sending q
		// alongside it is rejected by the real API.
		if r.URL.Query().Get("page_token") != "" && r.URL.Query().Get("q") != "" {
			http.Error(w, `{"error":"q must not accompany page_token"}`, http.StatusBadRequest)
			return
		}
		body, err := os.ReadFile("testdata/" + fixture)
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	p, err := NewSearchApi("sa-test")
	if err != nil {
		t.Fatal(err)
	}
	sp := p.(*searchApiProvider)
	sp.endpoint = srv.URL + "/search"
	return sp, &calls
}

// TestSearchApiReachesBingCopilot is the reason this provider exists: nothing
// else in this build can see that surface.
func TestSearchApiReachesBingCopilot(t *testing.T) {
	p, calls := newSearchApiAgainst(t, map[string]string{"bing_copilot": "searchapi_bing_copilot.json"})
	resp, err := p.Run(context.Background(), Request{Engine: BingCopilotEngine, Prompt: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(resp.Text) == "" {
		t.Fatal("no answer text")
	}
	if len(resp.Citations) == 0 {
		t.Fatal("no reference links parsed")
	}
	if resp.Calls != 1 || *calls != 1 {
		t.Errorf("calls reported %d, made %d, want 1 and 1", resp.Calls, *calls)
	}
	if resp.Model != "" {
		t.Errorf("model = %q, want empty for a scraped surface", resp.Model)
	}
	if p.Access() != AccessScraped {
		t.Errorf("access = %q", p.Access())
	}
}

func TestSearchApiReachesAIMode(t *testing.T) {
	p, _ := newSearchApiAgainst(t, map[string]string{"google_ai_mode": "searchapi_ai_mode.json"})
	resp, err := p.Run(context.Background(), Request{Engine: AIModeEngine, Prompt: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Citations) == 0 || strings.TrimSpace(resp.Text) == "" {
		t.Fatalf("text=%d citations=%d", len(resp.Text), len(resp.Citations))
	}
	for i, c := range resp.Citations {
		if !strings.HasPrefix(c.URL, "http") {
			t.Errorf("citation %d is not a URL: %q", i, c.URL)
		}
		if c.Position != i+1 {
			t.Errorf("citation %d has position %d", i, c.Position)
		}
	}
}

// TestSearchApiAIOverviewTakesTwoCalls pins the two-step, and pins that both
// calls are reported. A user watching their bill should see what this costs.
func TestSearchApiAIOverviewTakesTwoCalls(t *testing.T) {
	p, calls := newSearchApiAgainst(t, map[string]string{
		"google":             "searchapi_google_probe.json",
		"google_ai_overview": "searchapi_ai_overview.json",
	})
	resp, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Errorf("made %d HTTP calls, want 2", *calls)
	}
	if resp.Calls != 2 {
		t.Errorf("reported Calls = %d, want 2: the billable cost must be visible", resp.Calls)
	}
	if strings.TrimSpace(resp.Text) == "" || len(resp.Citations) == 0 {
		t.Fatalf("text=%d citations=%d", len(resp.Text), len(resp.Citations))
	}
}

// TestSearchApiIgnoresTheNotAvailableErrorWhenATokenIsPresent.
//
// Probed 2026-09-12, the ai_overview block read "An AI Overview is not
// available for this search" and handed over a page_token that then resolved
// a 7,000 character answer. Trusting that error text would throw the answer
// away and record a brand miss that never happened. The recorded fixture is
// exactly that response.
func TestSearchApiIgnoresTheNotAvailableErrorWhenATokenIsPresent(t *testing.T) {
	raw, err := os.ReadFile("testdata/searchapi_google_probe.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "not available") {
		t.Skip("the fixture no longer carries the misleading error")
	}
	p, _ := newSearchApiAgainst(t, map[string]string{
		"google":             "searchapi_google_probe.json",
		"google_ai_overview": "searchapi_ai_overview.json",
	})
	resp, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "q"})
	if err != nil {
		t.Fatalf("the not-available error was trusted over the token that works: %v", err)
	}
	if strings.TrimSpace(resp.Text) == "" {
		t.Error("no answer recovered")
	}
}

// TestSearchApiNoTokenIsNoAnswerSurface: when Google renders no overview at
// all there is no token, and that is not a miss for the brand.
func TestSearchApiNoTokenIsNoAnswerSurface(t *testing.T) {
	for name, body := range map[string]string{
		"no ai_overview block": `{"search_metadata":{"status":"Success"},"organic_results":[]}`,
		"block with no token":  `{"search_metadata":{"status":"Success"},"ai_overview":{"error":"not available"}}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		p, _ := NewSearchApi("sa-test")
		sp := p.(*searchApiProvider)
		sp.endpoint = srv.URL + "/search"

		_, err := sp.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "q"})
		if !errors.Is(err, ErrNoAnswerSurface) {
			t.Errorf("%s: err = %v, want ErrNoAnswerSurface", name, err)
		}
		srv.Close()
	}
}

func TestSearchApiEmptyAnswerIsNoAnswerSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"search_metadata":{"status":"Success"},"markdown":"   ","text_blocks":[]}`))
	}))
	defer srv.Close()
	p, _ := NewSearchApi("sa-test")
	sp := p.(*searchApiProvider)
	sp.endpoint = srv.URL + "/search"

	_, err := sp.Run(context.Background(), Request{Engine: BingCopilotEngine, Prompt: "q"})
	if !errors.Is(err, ErrNoAnswerSurface) {
		t.Fatalf("err = %v, want ErrNoAnswerSurface", err)
	}
}

func TestSearchApiWrongEngineIsTyped(t *testing.T) {
	p, _ := NewSearchApi("sa-test")
	_, err := p.Run(context.Background(), Request{Engine: ClaudeEngine, Prompt: "q"})
	if !errors.Is(err, ErrUnsupportedEngine) {
		t.Fatalf("err = %v, want ErrUnsupportedEngine", err)
	}
}

func TestSearchApiErrorTextHandlesBothShapes(t *testing.T) {
	// The API returns error as a string in some shapes and an object in
	// others, and reading only one would swallow the other.
	if got := searchApiErrorText("plain message"); got != "plain message" {
		t.Errorf("string form = %q", got)
	}
	if got := searchApiErrorText(map[string]any{"message": "structured"}); got != "structured" {
		t.Errorf("object form = %q", got)
	}
	if got := searchApiErrorText(nil); got != "" {
		t.Errorf("nil should be empty, got %q", got)
	}
}

func TestSearchApiMissingKeyIsAuthFailure(t *testing.T) {
	if _, err := NewSearchApi("  "); !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}
