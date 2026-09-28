// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package httpx

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/limelit-co/open/internal/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "limelit.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return New(":0", db, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", nil)
}

func TestHealthzReportsDatabaseState(t *testing.T) {
	// A health check that only proves the listener is alive stays green
	// through an outage, so this one has to name the migrations it read.
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.handleHealthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}

	var body health
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q", body.Status)
	}
	if body.Version != "test" {
		t.Errorf("version = %q", body.Version)
	}
	if len(body.Migrations) == 0 {
		t.Error("healthz did not report any applied migration, so it never touched the database")
	}
	if body.Database == "" {
		t.Error("healthz did not report the database path")
	}
}

func TestHealthzIsRoutedOnGetOnly(t *testing.T) {
	s := newTestServer(t)
	srv := httptest.NewServer(s.http.Handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz = %d", resp.StatusCode)
	}

	resp, err = http.Post(srv.URL+"/healthz", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz = %d, want 405", resp.StatusCode)
	}
}

func TestServeShutsDownOnContextCancel(t *testing.T) {
	// Graceful shutdown is what keeps a scheduled evaluation from being killed
	// mid-write when the container is told to stop.
	s := New("127.0.0.1:0", mustDB(t), slog.New(slog.NewTextHandler(io.Discard, nil)), "test", nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Serve returned %v, want a clean shutdown", err)
	}
}

func mustDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "limelit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
