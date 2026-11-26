// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Apify, for the Google AI Overview through a scraping Actor the user runs on
// their own Apify account.
//
// The Actor (krammer/google-ai-overview-scraper) submits the query to Google,
// records the overview block when Google renders one, and writes one row per
// query into a dataset. Nothing about the request is model selection, so this
// is AccessScraped and its answers are never averaged with a model API's.
//
// THE BEHAVIOUR THAT MATTERS HERE is the absent surface. When Google shows no
// AI Overview the Actor writes no default row and puts the reason in a second
// dataset, then still reports the run as SUCCEEDED. That is not a miss for
// the brand, so it returns ErrNoAnswerSurface and the runner excludes the chat
// from every denominator.
//
// TWO SHAPES ARE VENDOR-QUIRKS rather than choices made here:
//
//   - A run started with waitForFinish can still come back RUNNING, so the run
//     is re-read until it reaches a terminal status or the caller's context
//     ends. The re-read also uses waitForFinish so the vendor holds the
//     connection instead of being polled in a tight loop.
//   - A problem after the run has finished (a busy Google, an exhausted
//     monthly allowance) arrives only in statusMessage, so the text is mapped
//     onto the typed errors rather than passed through as an untyped failure.
//
// The fixture under testdata/ is a real recorded response with personal data
// removed.

const (
	// ApifyBaseEndpoint is the API root. The fields below are built from it at
	// construction and replaced by an httptest server in the unit tests, so
	// no test ever reaches the network.
	ApifyBaseEndpoint = "https://api.apify.com/v2"

	// ApifyActorID is the Actor run for every query. Apify spells the
	// owner/actor separator as a tilde in URLs.
	ApifyActorID = "krammer~google-ai-overview-scraper"

	// apifyWaitForFinish is how long the vendor holds a run or a re-read
	// before answering. It is a server-side wait, not a client timeout.
	apifyWaitForFinish = "60"

	// apifyNoOverviewErrorText is the Actor's own marker for "Google did not
	// render an overview for this query". It is compared exactly because the
	// errors dataset may also carry unrelated failure text.
	apifyNoOverviewErrorText = "No AI Overview returned for the query."

	// apifyStatusSucceeded is the only terminal status that carries rows.
	apifyStatusSucceeded = "SUCCEEDED"
)

// apifyTerminalStatuses are the statuses after which a run will not change.
var apifyTerminalStatuses = map[string]bool{
	"SUCCEEDED": true,
	"FAILED":    true,
	"ABORTED":   true,
	"TIMED-OUT": true,
}

type apifyProvider struct {
	token  string
	base   string
	client *http.Client
}

// NewApify builds the provider from the user's own Apify API token.
func NewApify(token string) (Provider, error) {
	t := strings.TrimSpace(token)
	if t == "" {
		return nil, fmt.Errorf("%w: APIFY_TOKEN is not set", ErrAuth)
	}
	return &apifyProvider{
		token: t,
		base:  ApifyBaseEndpoint,
		// A run waits for the Actor, which scrapes Google, and a re-read can
		// wait another minute; the vendor's own ceiling is well under this.
		client: &http.Client{Timeout: 3 * time.Minute},
	}, nil
}

func (p *apifyProvider) Name() string   { return "apify" }
func (p *apifyProvider) Access() Access { return AccessScraped }

// Engines maps to the empty string because a scraped surface has no model to
// choose. The Actor returns the US English surface only, which is checked in
// Run before any request is made.
func (p *apifyProvider) Engines() map[string]string {
	return map[string]string{AIOverviewEngine: ""}
}

type apifyStartRequest struct {
	// Queries is one query per row of the Actor's input. The prompt is
	// flattened to a single line before it lands here.
	Queries string `json:"queries"`
}

type apifyRunEnvelope struct {
	Data apifyRun `json:"data"`
}

type apifyRun struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	StatusMessage    string `json:"statusMessage"`
	DefaultDatasetID string `json:"defaultDatasetId"`
	StorageIDs       struct {
		Datasets struct {
			Default string `json:"default"`
			Errors  string `json:"errors"`
		} `json:"datasets"`
	} `json:"storageIds"`
}

type apifyItem struct {
	Query string `json:"query"`
	Type  string `json:"type"`
	Text  string `json:"text"`
	// Sources are the pages the overview attributed, with the vendor's own
	// title for each.
	Sources []struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	} `json:"sources"`
	// Error is set only in the errors dataset.
	Error string `json:"error"`
}

// Run fetches one Google AI Overview for one query.
func (p *apifyProvider) Run(ctx context.Context, req Request) (Response, error) {
	if req.Engine != AIOverviewEngine {
		return Response{}, fmt.Errorf("%w: apify reaches ai_overview, not %s", ErrUnsupportedEngine, req.Engine)
	}

	prompt := apifySingleLine(req.Prompt)
	if prompt == "" {
		return Response{}, fmt.Errorf("apify: the prompt is empty")
	}
	// The Actor asks Google's US English surface. Measuring another market
	// and labelling it this one would be the quiet wrongness this project
	// exists to avoid, so the refusal is explicit and costs no request.
	if err := apifyCheckLocale(req); err != nil {
		return Response{}, err
	}

	run, err := p.startRun(ctx, prompt)
	if err != nil {
		return Response{}, err
	}
	for !apifyTerminalStatuses[run.Status] {
		if run.ID == "" {
			return Response{}, fmt.Errorf("apify: run in status %s carried no id to re-read", run.Status)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Response{}, fmt.Errorf("apify: waiting for the run: %w", ctxErr)
		}
		run, err = p.readRun(ctx, run.ID)
		if err != nil {
			return Response{}, err
		}
	}
	if run.Status != apifyStatusSucceeded {
		return Response{}, apifyRunFailure(run)
	}

	if run.DefaultDatasetID != "" {
		items, err := p.fetchItems(ctx, run.DefaultDatasetID)
		if err != nil {
			return Response{}, err
		}
		if answer := apifyAnswer(items); answer != nil {
			return Response{
				Text: strings.TrimSpace(answer.Text),
				// A scraped surface has no model to report. Inventing "google"
				// would suggest a model was chosen.
				Model:     "",
				Citations: apifyCitations(*answer),
				// Google does not publish the searches behind an AI Overview,
				// so there is no fan-out to report.
				Calls: 1,
			}, nil
		}
	}

	// No usable row. The Actor puts the reason, when it has one, in a second
	// dataset; a run that rendered nothing at all leaves it empty.
	if errorsID := run.StorageIDs.Datasets.Errors; errorsID != "" {
		items, err := p.fetchItems(ctx, errorsID)
		if err != nil {
			return Response{}, err
		}
		if err, ok := apifyErrorDatasetReason(items); ok {
			return Response{}, err
		}
	}

	if err, ok := apifyMessageError(run.StatusMessage); ok {
		return Response{}, err
	}
	return Response{}, fmt.Errorf("apify: run %s finished with no answer%s", run.Status, apifyDetail(run.StatusMessage))
}

// apifySingleLine flattens a prompt to one line.
//
// The Actor accepts one query per line in its queries field, so a prompt
// whose newlines survived would be scraped as several queries and billed as
// several.
func apifySingleLine(prompt string) string {
	r := strings.NewReplacer("\r", " ", "\n", " ")
	return strings.TrimSpace(r.Replace(prompt))
}

// apifyCheckLocale refuses a market or language the Actor cannot serve.
//
// The refusal is a plain error rather than a typed one: the credential is
// fine and the engine exists, so ErrAuth or ErrUnsupportedEngine would send
// the user to fix the wrong thing.
func apifyCheckLocale(req Request) error {
	if c := strings.TrimSpace(req.LocationCountry); c != "" && !strings.EqualFold(c, "US") {
		return fmt.Errorf("apify: this actor returns AI Overviews for the United States only, not %q", c)
	}
	if l := strings.TrimSpace(req.LanguageCode); l != "" {
		base := strings.ToLower(strings.SplitN(l, "-", 2)[0])
		if base != "en" {
			return fmt.Errorf("apify: this actor returns English results only, not %q", l)
		}
	}
	return nil
}

// startRun submits the query and waits, server-side, for the run to finish.
func (p *apifyProvider) startRun(ctx context.Context, prompt string) (apifyRun, error) {
	endpoint := p.base + "/acts/" + ApifyActorID + "/runs?waitForFinish=" + apifyWaitForFinish
	raw, err := p.do(ctx, http.MethodPost, endpoint, apifyStartRequest{Queries: prompt})
	if err != nil {
		return apifyRun{}, err
	}
	var env apifyRunEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return apifyRun{}, fmt.Errorf("apify: decode run: %w", err)
	}
	return env.Data, nil
}

// readRun re-reads a run that was still going when it was started.
func (p *apifyProvider) readRun(ctx context.Context, id string) (apifyRun, error) {
	endpoint := p.base + "/actor-runs/" + url.PathEscape(id) + "?waitForFinish=" + apifyWaitForFinish
	raw, err := p.do(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return apifyRun{}, err
	}
	var env apifyRunEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return apifyRun{}, fmt.Errorf("apify: decode run: %w", err)
	}
	return env.Data, nil
}

// fetchItems reads one dataset. The endpoint returns a bare JSON array.
func (p *apifyProvider) fetchItems(ctx context.Context, datasetID string) ([]apifyItem, error) {
	raw, err := p.do(ctx, http.MethodGet, p.base+"/datasets/"+url.PathEscape(datasetID)+"/items", nil)
	if err != nil {
		return nil, err
	}
	var items []apifyItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("apify: decode dataset: %w", err)
	}
	return items, nil
}

// apifyAnswer picks the first row that carries readable text, in the order
// the Actor wrote them. A row with no text is skipped rather than returned,
// because an empty overview is not an answer.
func apifyAnswer(items []apifyItem) *apifyItem {
	for i := range items {
		if strings.TrimSpace(items[i].Text) != "" {
			return &items[i]
		}
	}
	return nil
}

// apifyCitations keeps the sources in the order the Actor listed them.
//
// A source with no URL is skipped: a citation without a source is not a
// citation. Positions are renumbered after the skip so the list has no hole.
// Nothing is deduplicated, because the Actor already lists each source once
// and collapsing repeats here would hide an attribution the surface made.
func apifyCitations(item apifyItem) []Citation {
	var out []Citation
	for _, s := range item.Sources {
		u := strings.TrimSpace(s.URL)
		if u == "" {
			continue
		}
		out = append(out, Citation{URL: u, Title: s.Title, Position: len(out) + 1})
	}
	return out
}

// apifyErrorDatasetReason reads the second dataset, which the Actor fills when
// it could not deliver an overview.
func apifyErrorDatasetReason(items []apifyItem) (error, bool) {
	var other string
	for _, it := range items {
		msg := strings.TrimSpace(it.Error)
		if msg == "" {
			continue
		}
		if msg == apifyNoOverviewErrorText {
			// Google rendered nothing for this query. Not a brand miss.
			return fmt.Errorf("%w: google showed no ai_overview for this query", ErrNoAnswerSurface), true
		}
		if other == "" {
			other = msg
		}
	}
	if other == "" {
		return nil, false
	}
	if err, ok := apifyMessageError(other); ok {
		return err, true
	}
	return fmt.Errorf("apify: %s", other), true
}

// apifyRunFailure reports a terminal run that did not succeed, naming both the
// status and the vendor's own explanation.
func apifyRunFailure(run apifyRun) error {
	message := strings.TrimSpace(run.Status + ": " + run.StatusMessage)
	if err, ok := apifyMessageError(message); ok {
		return err
	}
	if strings.TrimSpace(run.StatusMessage) == "" {
		return fmt.Errorf("apify: run ended with status %s", run.Status)
	}
	return fmt.Errorf("apify: run ended with status %s: %s", run.Status, strings.TrimSpace(run.StatusMessage))
}

// apifyMessageError maps the vendor's own wording onto the typed errors the
// runner reads. The match is a case-insensitive substring because the wording
// arrives inside a sentence, not as a field.
func apifyMessageError(message string) (error, bool) {
	m := strings.ToLower(message)
	switch {
	case strings.Contains(m, "temporarily limiting requests"), strings.Contains(m, "is busy"):
		return fmt.Errorf("%w: apify %s", ErrRateLimited, strings.TrimSpace(message)), true
	case strings.Contains(m, "monthly allowance"):
		return fmt.Errorf("%w: apify %s", ErrQuota, strings.TrimSpace(message)), true
	}
	return nil, false
}

// apifyDetail formats a vendor message for a plain error.
func apifyDetail(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	return ": " + message
}

// apifyStatusError maps an HTTP status onto the typed errors.
//
// 402 is this vendor's "the account has no credit left", which is a billing
// state and not a burst, so it is reported as ErrQuota rather than
// ErrRateLimited. It is handled here rather than through httpStatusError
// because that helper treats every 5xx as a rate limit, and the contract for
// this provider is that anything else keeps its body for the user to read.
func apifyStatusError(status int, body string) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return fmt.Errorf("%w: apify rejected the token", ErrAuth)
	case status == http.StatusPaymentRequired:
		return fmt.Errorf("%w: apify has no credit left%s", ErrQuota, truncateBody(body))
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: apify", ErrRateLimited)
	default:
		return fmt.Errorf("apify: http %d%s", status, truncateBody(body))
	}
}

// do sends one request and maps the status onto the typed errors.
//
// The token travels only in the Authorization header. It is never placed in a
// query string, where it would end up in the vendor's access logs and in any
// URL this project prints in an error. No retry happens here: the runner owns
// backoff, and retrying a paid scrape would bill the user twice for one
// prompt.
func (p *apifyProvider) do(ctx context.Context, method, endpoint string, body any) ([]byte, error) {
	var payloadReader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payloadReader = bytes.NewReader(encoded)
	}
	var req *http.Request
	var err error
	if payloadReader != nil {
		req, err = http.NewRequestWithContext(ctx, method, endpoint, payloadReader)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, endpoint, nil)
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payloadReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+p.token)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apify: %w", err)
	}
	payload, readErr := readLimited(resp)
	if readErr != nil {
		return nil, fmt.Errorf("apify: read response: %w", readErr)
	}
	if err := apifyStatusError(resp.StatusCode, string(payload)); err != nil {
		return nil, err
	}
	return payload, nil
}

// Test proves the token against the account endpoint, which is free, so
// pressing Test in Settings does not start an Actor run.
func (p *apifyProvider) Test(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/users/me", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("apify: %w", err)
	}
	body, err := readLimited(resp)
	if err != nil {
		return fmt.Errorf("apify: read body: %w", err)
	}
	return apifyStatusError(resp.StatusCode, string(body))
}
