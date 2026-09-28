// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package upgrade moves a self-hosted instance to Limelit Cloud.
//
// One command, nothing retyped. It exports everything this instance holds and
// pushes it to a Cloud workspace the user already owns, identified by an API
// key they paste. What they measured on their own machine becomes the same
// numbers in Cloud, and the grid is the same grid.
//
// Two things it deliberately does not do.
//
// It never sends a provider key. The payload is data: prompts, answers,
// mentions, citations, usage. The OpenAI or Anthropic key that produced them
// stays on this machine, because a migration is not a reason to hand a third
// party your credentials.
//
// It never deletes anything locally. An upgrade that wiped the source would
// make a failed import unrecoverable, and the point of an open core is that
// leaving is reversible.
package upgrade

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/limelit-co/open/internal/export"
	"github.com/limelit-co/open/internal/store"
)

const (
	// DefaultEndpoint is the Cloud import endpoint.
	DefaultEndpoint = "https://api.limelit.co/v1/import/open"
	// EndpointEnv overrides it, for a self-hosted Cloud or a test.
	EndpointEnv = "LIMELIT_CLOUD_ENDPOINT"
	// KeyEnv supplies the Cloud API key without putting it in a shell
	// history.
	KeyEnv = "LIMELIT_CLOUD_KEY"

	// instanceSetting holds this instance's own id.
	instanceSetting = "instance_id"
)

// Result is what Cloud reported back.
type Result struct {
	OK     bool `json:"ok"`
	Import struct {
		ImportID     string `json:"import_id"`
		Prompts      int    `json:"prompts_imported"`
		Competitors  int    `json:"competitors_imported"`
		Chats        int    `json:"chats_imported"`
		Mentions     int    `json:"mentions_imported"`
		Citations    int    `json:"citations_imported"`
		ChatsSkipped int    `json:"chats_skipped"`
	} `json:"import"`
	WorkspaceURL string `json:"workspace_url"`
	MCPURL       string `json:"mcp_url"`
	Next         string `json:"next"`
}

// Options configure one upgrade.
type Options struct {
	// Key is the Cloud API key. Required.
	Key string
	// Endpoint overrides the Cloud import URL.
	Endpoint string
	// Since narrows the export to answers on or after this date.
	Since string
	// Client is injected by tests.
	Client *http.Client
}

// Run exports this instance and pushes it to Cloud.
func Run(ctx context.Context, db *store.DB, opts Options) (Result, error) {
	key := strings.TrimSpace(opts.Key)
	if key == "" {
		return Result{}, fmt.Errorf(
			"a Limelit Cloud API key is required. Create one at https://limelit.co/settings and pass it with --key, or set %s", KeyEnv)
	}
	endpoint := strings.TrimSpace(opts.Endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	client := opts.Client
	if client == nil {
		// A large export over a slow link is normal, and a short timeout
		// would abandon a transfer that was going to succeed.
		client = &http.Client{Timeout: 30 * time.Minute}
	}

	instance, err := InstanceID(ctx, db)
	if err != nil {
		return Result{}, err
	}
	payload, err := buildPayload(ctx, db, instance, opts.Since)
	if err != nil {
		return Result{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(payload))

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("could not reach Limelit Cloud: %w. Nothing was changed here; re-running is safe", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusOK {
		return Result{}, cloudError(resp.StatusCode, body)
	}

	var result Result
	if err := json.Unmarshal(body, &result); err != nil {
		return Result{}, fmt.Errorf("limelit cloud accepted the upload but its reply could not be read: %w", err)
	}
	return result, nil
}

// buildPayload exports the instance and stamps it with its own id, which is
// what lets Cloud make a re-run idempotent.
func buildPayload(ctx context.Context, db *store.DB, instance, since string) ([]byte, error) {
	var buf bytes.Buffer
	if err := export.WriteJSON(ctx, db, &buf, export.Options{
		Since: since,
		Now:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return nil, err
	}

	// The export is a stream, so the id is spliced into the envelope rather
	// than the whole document being decoded and re-encoded to add one field.
	doc := buf.Bytes()
	if !bytes.HasPrefix(doc, []byte("{")) {
		return nil, fmt.Errorf("the export is not a JSON object")
	}
	stamped := append([]byte(`{"source_instance":`), mustJSON(instance)...)
	stamped = append(stamped, ',')
	stamped = append(stamped, doc[1:]...)
	return stamped, nil
}

// InstanceID returns this instance's own id, generating one on first use.
//
// It has to be stable across runs: it is half the key Cloud uses to tell an
// answer it has already imported from one it has not, so an upgrade
// interrupted halfway and re-run does not duplicate anything. A fresh id per
// run would make every retry a full re-import.
func InstanceID(ctx context.Context, db *store.DB) (string, error) {
	existing, err := db.Setting(ctx, instanceSetting)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(existing) != "" {
		return existing, nil
	}

	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := "open-" + hex.EncodeToString(raw)
	if err := db.SetSetting(ctx, instanceSetting, id); err != nil {
		return "", err
	}
	return id, nil
}

// cloudError turns a Cloud rejection into something a user can act on.
//
// Every case says what happened to the data and what to do next, because the
// alternative is a status code and a person guessing whether their history is
// now half in two places.
func cloudError(status int, body []byte) error {
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &payload)
	detail := payload.Error.Message
	if detail == "" {
		detail = payload.Message
	}
	if detail == "" {
		detail = strings.TrimSpace(string(body))
	}

	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("limelit cloud rejected the API key. Create a new one at https://limelit.co/settings. Nothing was imported")
	case http.StatusRequestEntityTooLarge:
		return fmt.Errorf("the export is too large for one upload. Narrow it with --since, for example --since %s. Nothing was imported: %s",
			time.Now().AddDate(0, -3, 0).Format("2006-01-02"), detail)
	case http.StatusBadRequest:
		return fmt.Errorf("limelit cloud could not read this export: %s. Nothing was imported", detail)
	default:
		return fmt.Errorf("limelit cloud returned %d: %s. The import is one transaction, so nothing was partially written and re-running is safe",
			status, detail)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
