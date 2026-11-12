// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package prnowtest is a stand-in PRNow API for tests: it answers each route
// with a fixture from internal/prnow/testdata and records every request, so
// a test can assert what was sent without the network or a real key.
package prnowtest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// Request is one call the fake received.
type Request struct {
	Method         string
	Path           string
	Authorization  string
	IdempotencyKey string
	Body           string
}

// Reply is what the fake answers on one route.
type Reply struct {
	Status  int
	Fixture string // a file in internal/prnow/testdata
	Header  map[string]string
}

// Server is the fake. Routes are "METHOD /path" after /api/v1, with a
// trailing "*" matching any id.
type Server struct {
	*httptest.Server
	Key string

	mu       sync.Mutex
	routes   map[string][]Reply
	requests []Request
}

// New starts a fake that accepts key and answers the documented routes with
// their success fixtures. Override a route with On.
func New(t *testing.T, key string) *Server {
	t.Helper()
	s := &Server{Key: key, routes: map[string][]Reply{
		"GET /test":       {{Status: 200, Fixture: "test.json"}},
		"GET /categories": {{Status: 200, Fixture: "categories.json"}},
		"POST /draft":     {{Status: 200, Fixture: "draft.json"}},
		"POST /submit":    {{Status: 200, Fixture: "submit.json"}},
		"GET /status/*":   {{Status: 200, Fixture: "status.json"}},
		"POST /retract/*": {{Status: 200, Fixture: "retract.json"}},
	}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.serve(t, w, r) }))
	t.Cleanup(s.Close)
	return s
}

// BaseURL is the API root to give a prnow.Client.
func (s *Server) BaseURL() string { return s.URL + "/api/v1" }

// On sets the replies for route, answered in order; the last one repeats.
func (s *Server) On(route string, replies ...Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[route] = replies
}

// Requests returns every request received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Count returns how many requests hit "METHOD /path" exactly as received.
func (s *Server) Count(method, prefix string) int {
	n := 0
	for _, r := range s.Requests() {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			n++
		}
	}
	return n
}

func (s *Server) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Method: r.Method, Path: path, Authorization: r.Header.Get("Authorization"),
		IdempotencyKey: r.Header.Get("Idempotency-Key"), Body: string(body),
	})
	route := r.Method + " " + path
	replies, ok := s.routes[route]
	if !ok {
		if i := strings.LastIndex(path, "/"); i > 0 {
			route = r.Method + " " + path[:i] + "/*"
			replies, ok = s.routes[route]
		}
	}
	var reply Reply
	if ok && len(replies) > 0 {
		reply = replies[0]
		if len(replies) > 1 {
			s.routes[route] = replies[1:]
		}
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer "+s.Key {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"success":false,"error":"Invalid API key"}`)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"success":false,"error":"Not found"}`)
		return
	}
	for k, v := range reply.Header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(reply.Status)
	_, _ = w.Write(Fixture(t, reply.Fixture))
}

// Fixture reads a file from internal/prnow/testdata.
func Fixture(t *testing.T, name string) []byte {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "testdata", name))
	if err != nil {
		t.Errorf("prnowtest: %v", err)
	}
	return raw
}
