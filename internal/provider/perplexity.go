// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Perplexity, through the Agent API. Effort levels enable web search, so the
// online flag on a target is accepted and ignored. Search results supply the
// source URLs and page titles directly.

const (
	// PerplexityEndpoint is the Agent API URL.
	PerplexityEndpoint = "https://api.perplexity.ai/v1/agent"

	// PerplexityDefaultModel selects the fast effort level.
	PerplexityDefaultModel = "fast"

	perplexityTestTokens = 16

	// A truncated answer can lose the brands named at its end. Reject
	// incomplete responses before handing an answer to the runner.
	perplexityMaxOutputTokens = 1500
)

type perplexityProvider struct {
	apiKey   string
	endpoint string
	client   *http.Client
}

// NewPerplexity builds the provider.
func NewPerplexity(apiKey string) (Provider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("%w: PERPLEXITY_API_KEY is not set", ErrAuth)
	}
	return &perplexityProvider{
		apiKey:   strings.TrimSpace(apiKey),
		endpoint: PerplexityEndpoint,
		client:   &http.Client{Timeout: 2 * time.Minute},
	}, nil
}

func (p *perplexityProvider) Name() string   { return "perplexity" }
func (p *perplexityProvider) Access() Access { return AccessAPI }

func (p *perplexityProvider) Engines() map[string]string {
	return map[string]string{PerplexityEngine: PerplexityDefaultModel}
}

type perplexityRequest struct {
	Effort          string `json:"effort"`
	Input           string `json:"input"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

type perplexityError struct {
	Message string `json:"message"`
}

type perplexityResponse struct {
	Model             string `json:"model"`
	Status            string `json:"status"`
	IncompleteDetails struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Results []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"results"`
	} `json:"output"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *perplexityError `json:"error"`
}

func perplexityEffort(model string) (string, error) {
	if model == "" || model == "sonar" {
		return PerplexityDefaultModel, nil
	}
	name, version, pinned := strings.Cut(model, "@")
	switch name {
	case "fast", "low", "medium", "high", "xhigh", "ultra":
		if !pinned || (version != "" && !strings.Contains(version, "@")) {
			return model, nil
		}
	}
	return "", fmt.Errorf("perplexity: unsupported effort %q", model)
}

// Run asks Perplexity one prompt.
func (p *perplexityProvider) Run(ctx context.Context, req Request) (Response, error) {
	if req.Engine != PerplexityEngine {
		return Response{}, fmt.Errorf("%w: perplexity reaches perplexity, not %s", ErrUnsupportedEngine, req.Engine)
	}
	effort, err := perplexityEffort(req.Model)
	if err != nil {
		return Response{}, err
	}
	parsed, err := p.request(ctx, perplexityRequest{Effort: effort, Input: req.Prompt, MaxOutputTokens: perplexityMaxOutputTokens})
	if err != nil {
		return Response{}, err
	}
	if parsed.Status != "completed" {
		return Response{}, fmt.Errorf("perplexity: answer status %q%s", parsed.Status, truncateBody(parsed.IncompleteDetails.Reason))
	}

	var text strings.Builder
	var citations []Citation
	seen := map[string]bool{}
	for _, item := range parsed.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					text.WriteString(part.Text)
				}
			}
		case "search_results":
			for _, r := range item.Results {
				if r.URL == "" || seen[r.URL] {
					continue
				}
				seen[r.URL] = true
				citations = append(citations, Citation{URL: r.URL, Title: r.Title, Position: len(citations) + 1})
			}
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return Response{}, fmt.Errorf("perplexity: no answer text in a completed response")
	}

	return Response{
		Text:         text.String(),
		Model:        parsed.Model,
		Citations:    citations,
		InputTokens:  parsed.Usage.InputTokens,
		OutputTokens: parsed.Usage.OutputTokens,
		Calls:        1,
	}, nil
}

// Test proves the key with a small Agent API request. A token-limited response
// is enough to verify the credential.
func (p *perplexityProvider) Test(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, err := p.request(ctx, perplexityRequest{Effort: PerplexityDefaultModel, Input: "ping", MaxOutputTokens: perplexityTestTokens})
	return err
}

func (p *perplexityProvider) request(ctx context.Context, body perplexityRequest) (perplexityResponse, error) {
	raw, err := postJSONWithStatusError(ctx, p.client, p.endpoint, map[string]string{
		"Authorization": "Bearer " + p.apiKey,
	}, body, "perplexity", p.statusError)
	if err != nil {
		return perplexityResponse{}, err
	}
	var parsed perplexityResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return perplexityResponse{}, fmt.Errorf("perplexity: decode response: %w", err)
	}
	if parsed.Error != nil {
		message := strings.ReplaceAll(parsed.Error.Message, p.apiKey, "[redacted]")
		return perplexityResponse{}, fmt.Errorf("perplexity: agent error%s", truncateBody(message))
	}
	return parsed, nil
}

func (p *perplexityProvider) statusError(status int, payload []byte) error {
	var failure struct {
		Error *perplexityError `json:"error"`
	}
	if json.Unmarshal(payload, &failure) == nil && failure.Error != nil && failure.Error.Message != "" {
		message := strings.ReplaceAll(failure.Error.Message, p.apiKey, "[redacted]")
		return fmt.Errorf("%w%s", httpStatusError("perplexity", status, ""), truncateBody(message))
	}
	return httpStatusError("perplexity", status, strings.ReplaceAll(string(payload), p.apiKey, "[redacted]"))
}

// postJSON sends one JSON request, retrying once on a transient failure, and
// maps the status onto the typed errors the runner branches on.
//
// Shared by the providers whose auth is a single header. The runner owns
// backoff, concurrency and the daily ceiling, so this stops at one retry.
func postJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body any, who string) ([]byte, error) {
	return postJSONWithStatusError(ctx, client, url, headers, body, who, func(status int, payload []byte) error {
		return httpStatusError(who, status, string(payload))
	})
}

func postJSONWithStatusError(ctx context.Context, client *http.Client, url string, headers map[string]string, body any, who string, statusError func(int, []byte) error) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(encoded)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("content-type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", who, err)
			continue
		}
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("%s: read response: %w", who, readErr)
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return payload, nil
		}

		err = statusError(resp.StatusCode, payload)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = err
			continue
		}
		return nil, err
	}
	return nil, lastErr
}

// httpStatusError maps an HTTP status onto the typed errors the runner reads.
func httpStatusError(who string, status int, body string) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return fmt.Errorf("%w: %s rejected the key", ErrAuth, who)
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s", ErrRateLimited, who)
	case status >= 500:
		return fmt.Errorf("%w: %s returned %d", ErrRateLimited, who, status)
	default:
		return fmt.Errorf("%s: http %d%s", who, status, truncateBody(body))
	}
}
