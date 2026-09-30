// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// The free allowance: Limelit Cloud answers one prompt on one engine with
// Limelit's provider keys, so a new user can measure before buying any key.
// The user signs up for Limelit Cloud, creates an API key in Settings and
// pastes it here. Once the month's allowance is used up, the relay refuses
// and the failed answers say to add the user's own key.
//
// Two registrations share this implementation and one key, because access is
// fixed per provider: "limelit" reaches the consumer surfaces Cloud collects
// through scraping services (ChatGPT, Gemini, Google AI Overviews and AI
// Mode), and "limelitapi" reaches Perplexity through its API.
//
// The prompt passes through Limelit Cloud to reach the engine; the answer is
// stored here, and Cloud keeps only which engine was asked and what it cost.

const (
	// LimelitCloudAPI is Cloud's API host. LIMELIT_CLOUD_API overrides it,
	// for a staging deployment.
	LimelitCloudAPI = "https://api.limelit.co"
	// LimelitKeyEnv is the Cloud API key, the same one `limelit upgrade` uses.
	LimelitKeyEnv = "LIMELIT_CLOUD_KEY"
	// LimelitKeyPage is where a free key is made: sign in (Google works, and
	// a first sign-in creates the account), press one button, copy the key.
	// `limelit login`, the startup message and the setup wizard all send
	// people here.
	LimelitKeyPage = "https://limelit.co/settings/open-key"

	limelitAnswerPath    = "/v1/relay/answer"
	limelitAllowancePath = "/v1/relay/allowance"
	limelitSetupPath     = "/v1/relay/setup"
)

// LimelitScrapedEngines and LimelitAPIEngines are what the two registrations
// reach. Claude is not in the allowance.
var (
	LimelitScrapedEngines = map[string]string{ChatGPTEngine: "", GeminiEngine: "", AIOverviewEngine: "", AIModeEngine: ""}
	LimelitAPIEngines     = map[string]string{PerplexityEngine: "sonar"}
)

type limelitProvider struct {
	name    string
	access  Access
	engines map[string]string
	key     string
	base    string
	client  *http.Client
}

// NewLimelit is the scraped half of the free allowance.
func NewLimelit(key string) (Provider, error) {
	return newLimelit("limelit", AccessScraped, LimelitScrapedEngines, key)
}

// NewLimelitAPI is the API half of the free allowance.
func NewLimelitAPI(key string) (Provider, error) {
	return newLimelit("limelitapi", AccessAPI, LimelitAPIEngines, key)
}

func newLimelit(name string, access Access, engines map[string]string, key string) (Provider, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%w: %s is not set; sign up at limelit.co and create an API key in Settings", ErrAuth, LimelitKeyEnv)
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("LIMELIT_CLOUD_API")), "/")
	if base == "" {
		base = LimelitCloudAPI
	}
	return &limelitProvider{
		name: name, access: access, engines: engines, key: key, base: base,
		// Cloud bounds one relayed call at 100 seconds; the margin covers the hop.
		client: &http.Client{Timeout: 2 * time.Minute},
	}, nil
}

func (p *limelitProvider) Name() string               { return p.name }
func (p *limelitProvider) Access() Access             { return p.access }
func (p *limelitProvider) Engines() map[string]string { return p.engines }

type limelitAnswer struct {
	Text      string `json:"text"`
	Model     string `json:"model"`
	Citations []struct {
		URL      string `json:"url"`
		Title    string `json:"title"`
		Position int    `json:"position"`
	} `json:"citations"`
	Searches        []string `json:"searches"`
	InputTokens     int      `json:"input_tokens"`
	OutputTokens    int      `json:"output_tokens"`
	NoAnswerSurface bool     `json:"no_answer_surface"`
}

// Run asks Cloud for one answer.
func (p *limelitProvider) Run(ctx context.Context, req Request) (Response, error) {
	if _, ok := p.engines[req.Engine]; !ok {
		return Response{}, fmt.Errorf("%w: %s does not reach %q", ErrUnsupportedEngine, p.name, req.Engine)
	}
	body, err := json.Marshal(map[string]string{
		"engine": req.Engine, "prompt": req.Prompt,
		"location_country": req.LocationCountry, "language": req.LanguageCode,
	})
	if err != nil {
		return Response{}, err
	}
	var out limelitAnswer
	if err := p.do(ctx, http.MethodPost, limelitAnswerPath, body, &out); err != nil {
		return Response{}, err
	}
	if out.NoAnswerSurface {
		return Response{}, ErrNoAnswerSurface
	}
	resp := Response{
		Text: out.Text, Model: out.Model, FanOut: out.Searches,
		InputTokens: out.InputTokens, OutputTokens: out.OutputTokens, Calls: 1,
	}
	for i, c := range out.Citations {
		pos := c.Position
		if pos <= 0 {
			pos = i + 1
		}
		resp.Citations = append(resp.Citations, Citation{URL: c.URL, Title: c.Title, Position: pos})
	}
	return resp, nil
}

// Test reads the allowance, which proves the key without spending any of it.
func (p *limelitProvider) Test(ctx context.Context) error {
	var a LimelitAllowance
	return p.do(ctx, http.MethodGet, limelitAllowancePath, nil, &a)
}

// FetchLimelitAllowance proves key against Limelit Cloud and reports how much
// of the free allowance is left. It spends nothing. A rejected key is ErrAuth.
func FetchLimelitAllowance(ctx context.Context, key string) (LimelitAllowance, error) {
	var a LimelitAllowance
	p, err := NewLimelit(key)
	if err != nil {
		return a, err
	}
	err = p.(*limelitProvider).do(ctx, http.MethodGet, limelitAllowancePath, nil, &a)
	return a, err
}

// LimelitSetup is what a Limelit Cloud account already tracks: its brand,
// its active prompts and its competitors (GET /v1/relay/setup), so an owner
// can measure the same thing here in one click.
type LimelitSetup struct {
	Brand struct {
		Name   string `json:"name"`
		Domain string `json:"domain"`
	} `json:"brand"`
	Prompts []struct {
		Text     string `json:"text"`
		Category string `json:"category"`
		Branded  *bool  `json:"branded"`
	} `json:"prompts"`
	Competitors []struct {
		Name    string   `json:"name"`
		Domain  string   `json:"domain"`
		Aliases []string `json:"aliases"`
	} `json:"competitors"`
}

// FetchLimelitSetup reads the key's Limelit Cloud account setup. It spends
// nothing. A rejected key is ErrAuth.
func FetchLimelitSetup(ctx context.Context, key string) (LimelitSetup, error) {
	var out LimelitSetup
	p, err := NewLimelit(key)
	if err != nil {
		return out, err
	}
	err = p.(*limelitProvider).do(ctx, http.MethodGet, limelitSetupPath, nil, &out)
	return out, err
}

// LimelitAllowance is Cloud's account of the free allowance.
type LimelitAllowance struct {
	Enabled        bool     `json:"enabled"`
	Engines        []string `json:"engines"`
	MonthlyCredits int      `json:"monthly_credits"`
	UsedThisMonth  int      `json:"used_this_month"`
	DailyCredits   int      `json:"daily_credits"`
	UsedToday      int      `json:"used_today"`
}

func (p *limelitProvider) do(ctx context.Context, method, path string, body []byte, into any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, p.base+path, rd)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.key)
	httpReq.Header.Set("Accept", "application/json")
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	res, err := p.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("limelit cloud: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("limelit cloud: read: %w", err)
	}
	if err := limelitStatusError(res.StatusCode, raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("limelit cloud: decode: %w", err)
	}
	return nil
}

// limelitStatusError maps Cloud's relay codes onto the typed errors the
// runner reads. A used-up allowance is ErrQuota, carrying Cloud's own
// sentence, so the failed answer tells the user to add their own key.
func limelitStatusError(status int, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}
	var e struct {
		Code    string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &e)
	msg := strings.TrimSpace(e.Message)
	switch {
	case status == http.StatusUnauthorized || e.Code == "auth_required":
		return fmt.Errorf("%w: Limelit Cloud rejected the key; create a new one in Settings at limelit.co", ErrAuth)
	case e.Code == "allowance_exhausted", e.Code == "daily_allowance_exhausted",
		e.Code == "daily_pool_exhausted", e.Code == "relay_disabled":
		if msg == "" {
			msg = "the free Limelit allowance is used up; add your own provider key in Settings"
		}
		return fmt.Errorf("%w: %s", ErrQuota, msg)
	case e.Code == "engine_not_available":
		return fmt.Errorf("%w: %s", ErrUnsupportedEngine, msg)
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: limelit cloud", ErrRateLimited)
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
	}
	return errors.New("limelit cloud: http " + fmt.Sprint(status) + ": " + msg)
}
