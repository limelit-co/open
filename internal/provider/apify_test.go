// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The fixtures here are real recorded responses from the Apify Actor with
// personal data removed: one run that delivered an AI Overview, one that
// delivered none. They pin the shapes the parser reads (data.status,
// data.defaultDatasetId, data.storageIds.datasets.errors, and the two dataset
// item shapes).

func newApifyAgainst(t *testing.T, h http.Handler) *apifyProvider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := NewApify("ap-test")
	if err != nil {
		t.Fatal(err)
	}
	ap := p.(*apifyProvider)
	ap.base = srv.URL + "/v2"
	return ap
}

// apifyFixtureHandler routes one recorded run and its two datasets, the way
// the real API does, so the multi-step path is exercised rather than stubbed
// out. An empty dataset id is not routed, because the Actor leaves one empty
// when it has nothing to say.
func apifyFixtureHandler(t *testing.T, runFixture, defaultItems, errorsItems string) http.Handler {
	t.Helper()
	runBody, err := os.ReadFile("testdata/" + runFixture)
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) []byte {
		if name == "" {
			return nil
		}
		body, readErr := os.ReadFile("testdata/" + name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return body
	}
	var run apifyRunEnvelope
	if err := json.Unmarshal(runBody, &run); err != nil {
		t.Fatal(err)
	}
	datasets := map[string][]byte{}
	// The default dataset always exists, even when the Actor delivered
	// nothing into it; an empty array is the recorded shape for that case.
	if id := run.Data.DefaultDatasetID; id != "" {
		datasets[id] = []byte("[]")
		if defaultItems != "" {
			datasets[id] = read(defaultItems)
		}
	}
	if id := run.Data.StorageIDs.Datasets.Errors; id != "" && errorsItems != "" {
		datasets[id] = read(errorsItems)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/acts/"+ApifyActorID+"/runs"):
			w.Write(runBody)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/datasets/"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/datasets/"), "/items")
			body, ok := datasets[id]
			if !ok {
				http.Error(w, `{"error":{"message":"no such dataset"}}`, http.StatusNotFound)
				return
			}
			w.Write([]byte(body))
		default:
			http.Error(w, `{"error":{"message":"unexpected request"}}`, http.StatusNotFound)
		}
	})
}

func TestApifyParsesAnAIOverview(t *testing.T) {
	p := newApifyAgainst(t, apifyFixtureHandler(t,
		"apify_ai_overview_run.json", "apify_ai_overview_items.json", ""))

	resp, err := p.Run(context.Background(), Request{
		Engine: AIOverviewEngine, Prompt: "what is a roth ira",
		LocationCountry: "US", LanguageCode: "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(resp.Text) == "" {
		t.Fatal("no answer text")
	}
	if !strings.Contains(resp.Text, "Roth individual retirement account") {
		t.Errorf("answer text is not the overview body: %q", resp.Text[:60])
	}
	if len(resp.Citations) != 10 {
		t.Fatalf("got %d citations, want the 10 the overview attributed", len(resp.Citations))
	}
	if resp.Citations[0].URL != "https://www.fidelity.com/learning-center/smart-money/what-is-a-roth-ira" {
		t.Errorf("first citation is %q", resp.Citations[0].URL)
	}
	if resp.Citations[0].Position != 1 {
		t.Errorf("first citation has position %d, want 1", resp.Citations[0].Position)
	}
	for i, c := range resp.Citations {
		if c.Position != i+1 {
			t.Errorf("citation %d has position %d, want %d", i, c.Position, i+1)
		}
		if !strings.HasPrefix(c.URL, "https://") {
			t.Errorf("citation %d is not a URL: %q", i, c.URL)
		}
		if c.Title == "" {
			t.Errorf("citation %d kept no title", i)
		}
	}
	// A scraped surface has no model to report and must not invent one.
	if resp.Model != "" {
		t.Errorf("scraped answer reported model %q", resp.Model)
	}
	if resp.Calls != 1 {
		t.Errorf("calls = %d, want 1", resp.Calls)
	}
	if resp.InputTokens != 0 || resp.OutputTokens != 0 {
		t.Errorf("scraped tokens = %d/%d, want 0/0", resp.InputTokens, resp.OutputTokens)
	}
	if len(resp.FanOut) != 0 {
		t.Errorf("fan-out = %v, want none: google does not publish it", resp.FanOut)
	}
}

func TestApifyAbsentOverviewIsNotABrandMiss(t *testing.T) {
	// The common case: Google renders no overview, the Actor reports the run
	// as SUCCEEDED and writes the reason to the errors dataset. It must
	// surface as an absent surface, which every metric denominator excludes,
	// not as an answer that ignored the brand.
	p := newApifyAgainst(t, apifyFixtureHandler(t,
		"apify_ai_overview_no_surface_run.json", "", "apify_ai_overview_no_surface_errors.json"))

	_, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "weather"})
	if !errors.Is(err, ErrNoAnswerSurface) {
		t.Fatalf("err = %v, want ErrNoAnswerSurface", err)
	}
}

func TestApifyRequestShape(t *testing.T) {
	var (
		gotMethod, gotPath, gotQuery, gotAuth string
		gotBody                               apifyStartRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v2/datasets/") {
			w.Write([]byte(`[]`))
			return
		}
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"id":"run-1","status":"SUCCEEDED","defaultDatasetId":"ds-1"}}`))
	}))
	t.Cleanup(srv.Close)
	p, err := NewApify("ap-test")
	if err != nil {
		t.Fatal(err)
	}
	ap := p.(*apifyProvider)
	ap.base = srv.URL + "/v2"

	if _, err := ap.Run(context.Background(), Request{
		Engine: AIOverviewEngine,
		// Two lines, as a pasted prompt often has: the Actor reads one query
		// per line, so a surviving newline would be scraped as two queries.
		Prompt: "what is a roth ira\ncompare with a traditional ira",
	}); err != nil && !strings.Contains(err.Error(), "no answer") {
		// The stub returns an empty dataset, so the run ends without an
		// answer; only the request shape is under test here.
		t.Fatalf("Run: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if !strings.Contains(gotPath, ApifyActorID+"/runs") {
		t.Errorf("path = %q, want it to name the Actor's runs endpoint", gotPath)
	}
	if gotQuery != "waitForFinish=60" {
		t.Errorf("query = %q, want waitForFinish=60 and nothing else", gotQuery)
	}
	if gotAuth != "Bearer ap-test" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if strings.Contains(gotQuery, "ap-test") || strings.Contains(gotPath, "ap-test") {
		t.Error("the token leaked into the URL")
	}
	if strings.ContainsAny(gotBody.Queries, "\r\n") {
		t.Errorf("queries still carries a newline: %q", gotBody.Queries)
	}
	if gotBody.Queries != "what is a roth ira compare with a traditional ira" {
		t.Errorf("queries = %q", gotBody.Queries)
	}
}

func TestApifyRereadsARunThatIsStillRunning(t *testing.T) {
	var reReads int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			w.Write([]byte(`{"data":{"id":"run-1","status":"RUNNING"}}`))
		case strings.HasPrefix(r.URL.Path, "/v2/actor-runs/run-1"):
			reReads++
			if r.URL.Query().Get("waitForFinish") != "60" {
				t.Errorf("re-read without waitForFinish=60: %q", r.URL.RawQuery)
			}
			w.Write([]byte(`{"data":{"id":"run-1","status":"SUCCEEDED","defaultDatasetId":"ds-1"}}`))
		case strings.HasPrefix(r.URL.Path, "/v2/datasets/ds-1/items"):
			w.Write([]byte(`[{"query":"q","text":"An answer.","sources":[{"url":"https://x.com","title":"X.com"}]}]`))
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	p, err := NewApify("ap-test")
	if err != nil {
		t.Fatal(err)
	}
	ap := p.(*apifyProvider)
	ap.base = srv.URL + "/v2"

	resp, err := ap.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if reReads != 1 {
		t.Errorf("the run was re-read %d times, want 1", reReads)
	}
	if resp.Text != "An answer." || len(resp.Citations) != 1 {
		t.Errorf("text=%q citations=%+v", resp.Text, resp.Citations)
	}
}

func TestApifyRejectsAnEngineItCannotReach(t *testing.T) {
	hits := 0
	p := newApifyAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	_, err := p.Run(context.Background(), Request{Engine: AIModeEngine, Prompt: "q"})
	if !errors.Is(err, ErrUnsupportedEngine) {
		t.Fatalf("err = %v, want ErrUnsupportedEngine", err)
	}
	if hits != 0 {
		t.Errorf("an unsupported engine made %d HTTP requests, want 0", hits)
	}
}

func TestApifyRejectsAMarketItCannotServe(t *testing.T) {
	// The Actor asks Google's US English surface. Measuring another market
	// and labelling it this one would be quietly wrong, so the refusal is
	// explicit and before any request that would be billed.
	cases := map[string]Request{
		"other country":  {Engine: AIOverviewEngine, Prompt: "q", LocationCountry: "GB"},
		"other language": {Engine: AIOverviewEngine, Prompt: "q", LanguageCode: "de"},
		"region tag":     {Engine: AIOverviewEngine, Prompt: "q", LanguageCode: "zh-TW"},
	}
	for name, req := range cases {
		hits := 0
		p := newApifyAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
		}))
		_, err := p.Run(context.Background(), req)
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if errors.Is(err, ErrAuth) || errors.Is(err, ErrQuota) ||
			errors.Is(err, ErrRateLimited) || errors.Is(err, ErrUnsupportedEngine) ||
			errors.Is(err, ErrNoAnswerSurface) {
			t.Errorf("%s: err = %v, want a plain error, not a typed one", name, err)
		}
		if hits != 0 {
			t.Errorf("%s: made %d HTTP requests, want 0", name, hits)
		}
	}

	// The accepted spellings, so the check is not simply rejecting anything
	// that is set.
	for _, req := range []Request{
		{Engine: AIOverviewEngine, Prompt: "q", LocationCountry: "us", LanguageCode: "en-US"},
		{Engine: AIOverviewEngine, Prompt: "q", LocationCountry: "US", LanguageCode: "EN"},
		{Engine: AIOverviewEngine, Prompt: "q"},
	} {
		if err := apifyCheckLocale(req); err != nil {
			t.Errorf("locale %q/%q refused: %v", req.LocationCountry, req.LanguageCode, err)
		}
	}
}

func TestApifyEmptyPromptMakesNoRequest(t *testing.T) {
	hits := 0
	p := newApifyAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	_, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: " \r\n\t "})
	if err == nil {
		t.Fatal("an empty prompt produced a run")
	}
	if hits != 0 {
		t.Errorf("an empty prompt made %d HTTP requests, want 0", hits)
	}
}

func TestApifyMapsHTTPFailures(t *testing.T) {
	cases := map[int]error{
		http.StatusUnauthorized:    ErrAuth,
		http.StatusForbidden:       ErrAuth,
		http.StatusPaymentRequired: ErrQuota,
		http.StatusTooManyRequests: ErrRateLimited,
	}
	for status, want := range cases {
		p := newApifyAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":{"message":"nope"}}`))
		}))
		_, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "q"})
		if !errors.Is(err, want) {
			t.Errorf("status %d gave %v, want %v", status, err, want)
		}
	}

	// Anything else keeps its body, so a user can read what the vendor said.
	p := newApifyAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"actor input is invalid"}}`))
	}))
	_, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "q"})
	if err == nil || !strings.Contains(err.Error(), "actor input is invalid") {
		t.Errorf("err = %v, want the vendor's message kept", err)
	}
	for _, typed := range []error{ErrAuth, ErrQuota, ErrRateLimited, ErrUnsupportedEngine, ErrNoAnswerSurface} {
		if errors.Is(err, typed) {
			t.Errorf("a 400 was reported as %v", typed)
		}
	}
}

func TestApifyMapsVendorMessages(t *testing.T) {
	cases := map[string]error{
		"Google is temporarily limiting requests for this query.": ErrRateLimited,
		"The proxy is busy, retry shortly.":                       ErrRateLimited,
		"Your monthly allowance is used up.":                      ErrQuota,
	}
	for message, want := range cases {
		body, _ := json.Marshal(map[string]any{"data": map[string]any{
			"id": "run-1", "status": "FAILED", "statusMessage": message,
		}})
		p := newApifyAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		_, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "q"})
		if !errors.Is(err, want) {
			t.Errorf("%q gave %v, want %v", message, err, want)
		}
	}
}

func TestApifyOtherRunFailureIsAPlainError(t *testing.T) {
	body := `{"data":{"id":"run-1","status":"FAILED","statusMessage":"input schema mismatch"}}`
	p := newApifyAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	_, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "q"})
	if err == nil {
		t.Fatal("a FAILED run produced an answer")
	}
	for _, typed := range []error{ErrAuth, ErrQuota, ErrRateLimited, ErrUnsupportedEngine, ErrNoAnswerSurface} {
		if errors.Is(err, typed) {
			t.Errorf("err = %v, want no typed error", err)
		}
	}
	if !strings.Contains(err.Error(), "FAILED") || !strings.Contains(err.Error(), "input schema mismatch") {
		t.Errorf("err = %v, want the status and the vendor's message", err)
	}
}

func TestApifyOtherErrorRowIsAPlainError(t *testing.T) {
	run := `{"data":{"id":"run-1","status":"SUCCEEDED","defaultDatasetId":"ds-1","storageIds":{"datasets":{"errors":"ds-err"}}}}`
	p := newApifyAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			w.Write([]byte(run))
		case strings.HasSuffix(r.URL.Path, "/datasets/ds-1/items"):
			w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/datasets/ds-err/items"):
			w.Write([]byte(`[{"query":"q","error":"the page could not be parsed"}]`))
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	_, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "q"})
	if err == nil || !strings.Contains(err.Error(), "the page could not be parsed") {
		t.Fatalf("err = %v, want a plain error carrying the vendor's text", err)
	}
	if errors.Is(err, ErrNoAnswerSurface) {
		t.Error("an unrelated Actor error was reported as an absent surface")
	}
}

func TestApifySucceededWithNoRowsFallsBackToTheStatusMessage(t *testing.T) {
	// The Actor can finish SUCCEEDED with an empty dataset and no errors
	// dataset, leaving statusMessage as the only explanation. It still has to
	// reach the typed errors, or the runner would retry a billing problem.
	for _, c := range []struct {
		message string
		want    error
	}{
		{"Google is busy, try again later.", ErrRateLimited},
		{"The monthly allowance for this account is used up.", ErrQuota},
	} {
		run, _ := json.Marshal(map[string]any{"data": map[string]any{
			"id": "run-1", "status": "SUCCEEDED", "statusMessage": c.message,
			"defaultDatasetId": "ds-1",
		}})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.HasPrefix(r.URL.Path, "/v2/datasets/") {
				w.Write([]byte(`[]`))
				return
			}
			w.Write(run)
		}))
		p, err := NewApify("ap-test")
		if err != nil {
			t.Fatal(err)
		}
		ap := p.(*apifyProvider)
		ap.base = srv.URL + "/v2"

		_, err = ap.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "q"})
		if !errors.Is(err, c.want) {
			t.Errorf("%q gave %v, want %v", c.message, err, c.want)
		}
		srv.Close()
	}
}

func TestApifyTestUsesTheAccountCallOnce(t *testing.T) {
	var hits int
	p := newApifyAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/users/me" {
			t.Errorf("%s %s, want GET /v2/users/me", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer ap-test" {
			t.Errorf("auth header %q", r.Header.Get("Authorization"))
		}
		hits++
		w.Write([]byte(`{"data":{"username":"someone"}}`))
	}))
	if err := p.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if hits != 1 {
		t.Errorf("the account endpoint was hit %d times, want 1", hits)
	}
}

func TestApifyTestReportsABadKey(t *testing.T) {
	p := newApifyAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	if err := p.Test(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestApifyRequiresAToken(t *testing.T) {
	if _, err := NewApify("  "); !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}
