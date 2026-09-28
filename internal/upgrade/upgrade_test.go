// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package upgrade

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/limelit-co/open/internal/store"
)

func seeded(t *testing.T) *store.DB {
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
	if _, err := db.AddPrompt(ctx, store.Prompt{Text: "best widget tools", Active: true}); err != nil {
		t.Fatal(err)
	}
	return db
}

// cloudStub captures what the open core actually sent.
func cloudStub(t *testing.T, status int, reply string) (*httptest.Server, *[]byte, *string) {
	t.Helper()
	var (
		body []byte
		auth string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, &body, &auth
}

const okReply = `{"ok":true,"import":{"import_id":"abc","prompts_imported":1,"chats_imported":0},
	"workspace_url":"https://limelit.co/overview","mcp_url":"https://api.limelit.co/mcp"}`

func TestRunSendsTheExportWithTheKey(t *testing.T) {
	db := seeded(t)
	srv, body, auth := cloudStub(t, http.StatusOK, okReply)

	result, err := Run(context.Background(), db, Options{Key: "llk_test", Endpoint: srv.URL, Client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if *auth != "Bearer llk_test" {
		t.Errorf("Authorization = %q", *auth)
	}
	if !result.OK || result.Import.Prompts != 1 {
		t.Errorf("result = %+v", result)
	}
	if result.MCPURL == "" {
		t.Error("the MCP endpoint did not come back, so the user has nothing to point a client at")
	}

	var sent map[string]any
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatalf("the payload is not valid JSON: %v", err)
	}
	if sent["source_instance"] == nil || sent["source_instance"] == "" {
		t.Error("the payload does not identify this instance, so Cloud cannot make a re-run idempotent")
	}
	for _, table := range []string{"property", "prompts", "chats", "mentions", "citations"} {
		if _, ok := sent[table]; !ok {
			t.Errorf("the payload is missing %q", table)
		}
	}
}

// TestRunNeverSendsAProviderKey. A migration is not a reason to hand a third
// party the credentials that produced the data.
func TestRunNeverSendsAProviderKey(t *testing.T) {
	ctx := context.Background()
	db := seeded(t)
	if err := db.SetSetting(ctx, "credential:OPENAI_API_KEY", "sk-super-secret-value"); err != nil {
		t.Fatal(err)
	}
	srv, body, _ := cloudStub(t, http.StatusOK, okReply)

	if _, err := Run(ctx, db, Options{Key: "llk_test", Endpoint: srv.URL, Client: srv.Client()}); err != nil {
		t.Fatal(err)
	}
	payload := string(*body)
	for _, secret := range []string{"sk-super-secret-value", "OPENAI_API_KEY", "credential:"} {
		if strings.Contains(payload, secret) {
			t.Errorf("the payload carries %q", secret)
		}
	}
}

// TestInstanceIDIsStable is what makes a retry safe. A fresh id per run would
// make Cloud treat every re-run as a new import and duplicate everything.
func TestInstanceIDIsStable(t *testing.T) {
	ctx := context.Background()
	db := seeded(t)

	first, err := InstanceID(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	second, err := InstanceID(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("instance id changed between calls: %q then %q", first, second)
	}
	if !strings.HasPrefix(first, "open-") || len(first) < 20 {
		t.Errorf("instance id looks wrong: %q", first)
	}

	// And across a fresh handle on the same database.
	srv, body, _ := cloudStub(t, http.StatusOK, okReply)
	if _, err := Run(ctx, db, Options{Key: "k", Endpoint: srv.URL, Client: srv.Client()}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	json.Unmarshal(*body, &sent)
	if sent["source_instance"] != first {
		t.Errorf("the upload used %v, not the stored id %q", sent["source_instance"], first)
	}
}

func TestRunRequiresAKey(t *testing.T) {
	_, err := Run(context.Background(), seeded(t), Options{})
	if err == nil {
		t.Fatal("an upgrade with no key was attempted")
	}
	// The message has to say where to get one.
	if !strings.Contains(err.Error(), "limelit.co/settings") || !strings.Contains(err.Error(), KeyEnv) {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
}

// TestCloudErrorsSayWhatHappenedToTheData. A status code alone leaves a user
// guessing whether their history is now half in two places.
func TestCloudErrorsSayWhatHappenedToTheData(t *testing.T) {
	for _, tc := range []struct {
		status int
		reply  string
		want   []string
	}{
		{http.StatusUnauthorized, `{"error":{"message":"bad key"}}`, []string{"rejected the API key", "Nothing was imported"}},
		{http.StatusRequestEntityTooLarge, `{"error":{"message":"too big"}}`, []string{"--since", "Nothing was imported"}},
		{http.StatusBadRequest, `{"error":{"message":"version 2 not supported"}}`, []string{"version 2 not supported", "Nothing was imported"}},
		{http.StatusInternalServerError, `{"error":{"message":"boom"}}`, []string{"re-running is safe"}},
	} {
		srv, _, _ := cloudStub(t, tc.status, tc.reply)
		_, err := Run(context.Background(), seeded(t), Options{Key: "k", Endpoint: srv.URL, Client: srv.Client()})
		if err == nil {
			t.Fatalf("status %d was treated as success", tc.status)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("status %d error %q does not contain %q", tc.status, err, want)
			}
		}
	}
}

func TestBuildPayloadProducesValidJSONWithTheIDFirst(t *testing.T) {
	// The id is spliced into the envelope rather than the document being
	// decoded and re-encoded, so a malformed splice is the risk worth testing.
	raw, err := buildPayload(context.Background(), seeded(t), "open-abc", "")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the spliced payload is not valid JSON: %v\n%s", err, raw)
	}
	if doc["source_instance"] != "open-abc" {
		t.Errorf("source_instance = %v", doc["source_instance"])
	}
	if doc["schema_version"] == nil {
		t.Error("the splice lost the export envelope")
	}
}

func TestSinceNarrowsThePayload(t *testing.T) {
	raw, err := buildPayload(context.Background(), seeded(t), "open-abc", "2099-01-01")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(raw, &doc)
	if doc["since"] != "2099-01-01" {
		t.Errorf("since = %v, want it recorded in the payload", doc["since"])
	}
}
