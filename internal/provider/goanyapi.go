// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// GoAnyAPI, for the two Google surfaces that have no API at all.
//
// Both endpoints are scraped by the vendor: the caller sends a query and gets
// back what a person would have seen on the results page, AI Overview block
// included. That is why this is AccessScraped and not AccessAPI, and why its
// answers are never averaged with a model API's.
//
// THE BEHAVIOUR THAT MATTERS HERE is the absent surface. Google shows no AI
// Overview for most queries, and the vendor reports that as a normal success:
// `code: "ok"` with `search_parameters.feature_available: false` and empty
// `results`. It costs credits anyway. It is not a miss for the brand, so it
// returns ErrNoAnswerSurface and the runner excludes the chat from every
// denominator. Reading an empty overview as "the overview ignored us" would
// pull visibility toward zero hardest on exactly the queries where an
// overview is rare.
//
// Shapes are the vendor's documented example responses (2026-11-14). The
// fixture under testdata/ is that sample, not a call this repository made.

const (
	// GoAnyAPIOverviewEndpoint answers an ordinary Google results page and
	// carries the AI Overview inside it, which is where a person meets it.
	GoAnyAPIOverviewEndpoint = "https://api.goanyapi.com/api/v1/google-ai-overview"

	// GoAnyAPIAIModeEndpoint is the dedicated AI Mode surface.
	GoAnyAPIAIModeEndpoint = "https://api.goanyapi.com/api/v1/google-ai-mode"

	// GoAnyAPIBalanceEndpoint returns the remaining credit balance and is
	// free, so pressing Test in Settings costs nothing.
	GoAnyAPIBalanceEndpoint = "https://api.goanyapi.com/api/v1/credits/balance"

	// goAnyAPIOK is the vendor's own success marker, a string rather than a
	// number, returned inside a 200 response.
	goAnyAPIOK = "ok"

	// goAnyAPIDefaultDevice is the desktop surface. A phone results page can
	// render a different overview, so the device is pinned rather than left
	// to the vendor's default.
	goAnyAPIDefaultDevice = "desktop"
)

type goAnyAPIProvider struct {
	key      string
	overview string
	aiMode   string
	balance  string
	client   *http.Client
}

// NewGoAnyAPI builds the provider from a single API key.
func NewGoAnyAPI(apiKey string) (Provider, error) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, fmt.Errorf("%w: GOANYAPI_API_KEY is not set", ErrAuth)
	}
	return &goAnyAPIProvider{
		key:      key,
		overview: GoAnyAPIOverviewEndpoint,
		aiMode:   GoAnyAPIAIModeEndpoint,
		balance:  GoAnyAPIBalanceEndpoint,
		// A live scrape of a results page is slow; the vendor's own ceiling
		// is well under this.
		client: &http.Client{Timeout: 3 * time.Minute},
	}, nil
}

func (p *goAnyAPIProvider) Name() string   { return "goanyapi" }
func (p *goAnyAPIProvider) Access() Access { return AccessScraped }

// Engines maps to the empty string because a scraped surface has no model to
// choose. A target that pins one is rejected at parse time.
func (p *goAnyAPIProvider) Engines() map[string]string {
	return map[string]string{AIOverviewEngine: "", AIModeEngine: ""}
}

type goAnyAPIRequest struct {
	Query    string `json:"query"`
	Country  string `json:"country,omitempty"`
	Language string `json:"language,omitempty"`
	Device   string `json:"device,omitempty"`
}

type goAnyAPIEnvelope struct {
	// Code is the vendor's success marker and arrives as a string. A
	// numeric zero would never match it, so it is not decoded as an int.
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Endpoint         string `json:"endpoint"`
		CostCredits      int    `json:"costCredits"`
		RemainingCredits int    `json:"remainingCredits"`
		Result           struct {
			Format  string          `json:"format"`
			Content json.RawMessage `json:"content"`
		} `json:"result"`
	} `json:"data"`
}

// goAnyAPIContent is the JSON body of data.result.content when format is
// "json". The same answer text and links appear twice, under results and
// under serp_features; only results is read here, and the duplicate is left
// alone rather than merged, so a citation is counted once.
type goAnyAPIContent struct {
	Results          []goAnyAPIResult `json:"results"`
	SerpFeatures     []goAnyAPIResult `json:"serp_features"`
	SearchParameters struct {
		// FeatureAvailable is how the vendor says "Google rendered no
		// overview for this query". It is a normal success, not an error.
		FeatureAvailable *bool `json:"feature_available"`
	} `json:"search_parameters"`
}

type goAnyAPIResult struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	Text  string `json:"text"`
	// Links are the sources the overview attributed, with the vendor's own
	// label. They are citations, not the page's share URL.
	Links []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"links"`
}

// Run fetches one Google AI surface for one query.
func (p *goAnyAPIProvider) Run(ctx context.Context, req Request) (Response, error) {
	var endpoint string
	switch req.Engine {
	case AIOverviewEngine:
		endpoint = p.overview
	case AIModeEngine:
		endpoint = p.aiMode
	default:
		return Response{}, fmt.Errorf("%w: goanyapi reaches ai_overview and ai_mode, not %s", ErrUnsupportedEngine, req.Engine)
	}

	body := goAnyAPIRequest{
		Query:   req.Prompt,
		Country: strings.ToLower(strings.TrimSpace(req.LocationCountry)),
		// The vendor accepts a language tag with a region, so the base
		// language is passed through rather than widened.
		Language: strings.TrimSpace(req.LanguageCode),
		Device:   goAnyAPIDefaultDevice,
	}

	raw, err := p.post(ctx, endpoint, body)
	if err != nil {
		return Response{}, err
	}

	var parsed goAnyAPIEnvelope
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Response{}, fmt.Errorf("goanyapi: decode response: %w", err)
	}
	if parsed.Code != goAnyAPIOK {
		return Response{}, goAnyAPIEnvelopeError(parsed.Code, parsed.Message)
	}

	// A non-json format is not something this provider asked for, and
	// guessing at markdown here would invent structure the engine did not
	// give. Refusing is the honest answer.
	if parsed.Data.Result.Format != "" && parsed.Data.Result.Format != "json" {
		return Response{}, fmt.Errorf("goanyapi: unexpected result format %q", parsed.Data.Result.Format)
	}

	var content goAnyAPIContent
	if len(parsed.Data.Result.Content) > 0 {
		if err := json.Unmarshal(parsed.Data.Result.Content, &content); err != nil {
			return Response{}, fmt.Errorf("goanyapi: decode content: %w", err)
		}
	}

	if content.SearchParameters.FeatureAvailable != nil && !*content.SearchParameters.FeatureAvailable {
		// Google rendered nothing for this query. Not a brand miss.
		return Response{}, fmt.Errorf("%w: google showed no %s for this query", ErrNoAnswerSurface, req.Engine)
	}

	answer := goAnyAPIAnswer(content)
	if answer == nil || strings.TrimSpace(answer.Text) == "" {
		// No text means no answer surface, whether or not the flag was set:
		// an overview block with nothing readable in it is not an answer.
		return Response{}, fmt.Errorf("%w: the %s block carried no text", ErrNoAnswerSurface, req.Engine)
	}

	return Response{
		Text: strings.TrimSpace(answer.Text),
		// A scraped surface has no model to report. Inventing "google" would
		// suggest a model was chosen.
		Model:     "",
		Citations: goAnyAPICitations(answer),
		// Google does not publish the searches behind an AI Overview or an
		// AI Mode answer, so there is no fan-out to report.
		Calls: 1,
	}, nil
}

// goAnyAPIAnswer picks the entry that carries the answer text. The vendor's
// results array can hold other blocks, and type is "ai_summary" for the one
// that matters; a block with text but an unknown type is still accepted,
// because a renamed type should not silently produce a zero.
func goAnyAPIAnswer(content goAnyAPIContent) *goAnyAPIResult {
	var fallback *goAnyAPIResult
	for i := range content.Results {
		r := &content.Results[i]
		if strings.TrimSpace(r.Text) == "" {
			continue
		}
		if r.Type == "ai_summary" {
			return r
		}
		if fallback == nil {
			fallback = r
		}
	}
	return fallback
}

// goAnyAPICitations keeps the links in the order the engine gave them,
// deduplicated by URL. An entry with no URL is skipped: a citation without a
// source is not a citation.
func goAnyAPICitations(item *goAnyAPIResult) []Citation {
	var (
		out  []Citation
		seen = map[string]bool{}
	)
	for _, l := range item.Links {
		url := strings.TrimSpace(l.URL)
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		out = append(out, Citation{URL: url, Title: l.Title, Position: len(out) + 1})
	}
	return out
}

// post sends one request and maps the status onto the typed errors.
//
// It does not retry: the runner owns backoff and the daily ceiling, and a
// provider that retried on its own would spend the user's credits twice for
// one prompt. The one thing worth handling locally is 402, which this vendor
// uses for an empty credit balance and which the generic status helper would
// otherwise report as an untyped failure; a balance problem is not a burst,
// so calling it ErrRateLimited would have the runner back off all day.
func (p *goAnyAPIProvider) post(ctx context.Context, endpoint string, body goAnyAPIRequest) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.key)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("goanyapi: %w", err)
	}
	payload, readErr := readLimited(resp)
	if readErr != nil {
		return nil, fmt.Errorf("goanyapi: read response: %w", readErr)
	}
	if resp.StatusCode == http.StatusPaymentRequired {
		return nil, fmt.Errorf("%w: goanyapi has no credits left%s", ErrQuota, truncateBody(string(payload)))
	}
	if err := httpStatusError("goanyapi", resp.StatusCode, string(payload)); err != nil {
		return nil, err
	}
	return payload, nil
}

// Test proves the key against the balance endpoint, which is free, so
// pressing Test in Settings spends nothing.
func (p *goAnyAPIProvider) Test(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.balance, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("goanyapi: %w", err)
	}
	body, err := readLimited(resp)
	if err != nil {
		return fmt.Errorf("goanyapi: read body: %w", err)
	}
	if err := httpStatusError("goanyapi", resp.StatusCode, string(body)); err != nil {
		return err
	}

	var parsed goAnyAPIEnvelope
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("goanyapi: decode response: %w", err)
	}
	if parsed.Code != goAnyAPIOK {
		return goAnyAPIEnvelopeError(parsed.Code, parsed.Message)
	}
	return nil
}

// goAnyAPIEnvelopeError maps the vendor's error marker onto the typed errors
// the runner reads. The vendor sends the code inside the body as well as the
// HTTP status, so both spellings of each condition are handled.
func goAnyAPIEnvelopeError(code, message string) error {
	c := strings.ToLower(strings.TrimSpace(code))
	msg := strings.TrimSpace(c + " " + message)
	switch {
	case strings.Contains(c, "invalid_params"), strings.Contains(c, "invalid_request"):
		return fmt.Errorf("goanyapi: %s", msg)
	case strings.Contains(c, "unauthorized"), strings.Contains(c, "forbidden"),
		strings.Contains(c, "invalid_api_key"):
		return fmt.Errorf("%w: goanyapi %s", ErrAuth, msg)
	case strings.Contains(c, "insufficient_credits"), strings.Contains(c, "quota"),
		strings.Contains(c, "balance"):
		return fmt.Errorf("%w: goanyapi %s", ErrQuota, msg)
	case strings.Contains(c, "rate"), strings.Contains(c, "timeout"),
		strings.Contains(c, "request_failed"):
		return fmt.Errorf("%w: goanyapi %s", ErrRateLimited, msg)
	default:
		return fmt.Errorf("goanyapi: %s", msg)
	}
}
