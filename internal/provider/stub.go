// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package provider

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/limelit-co/open/internal/engines"
)

// StubName is the provider name a stub registers under.
const StubName = "stub"

// StubConfig shapes a canned provider.
//
// The stub exists so the runner, the metrics and the MCP tools can be tested
// end to end with no network, no keys and no fixtures to re-record. It is
// registered only by callers that ask for it, never by DefaultRegistry, so it
// cannot appear in a real deployment's target list.
type StubConfig struct {
	// Access defaults to AccessAPI.
	Access Access
	// Engines defaults to every chat engine, with no model pin.
	Engines map[string]string
	// Answer is returned for every prompt unless AnswerFor has an entry.
	Answer string
	// AnswerFor maps a prompt to its answer, for tests that need different
	// text per prompt.
	AnswerFor map[string]string
	// Citations are returned with every answer.
	Citations []Citation
	// Err, when set, is returned by Run instead of an answer. Use the typed
	// errors to exercise the runner's failure paths.
	Err error
	// TestErr, when set, is returned by Test.
	TestErr error
}

// Stub is a canned provider. It counts its calls so a test can assert that
// the runner asked exactly once per prompt and target.
type Stub struct {
	cfg StubConfig

	mu    sync.Mutex
	calls int
	seen  []Request
}

// NewStub builds a canned provider.
func NewStub(cfg StubConfig) *Stub {
	if cfg.Access == "" {
		cfg.Access = AccessAPI
	}
	if len(cfg.Engines) == 0 {
		cfg.Engines = map[string]string{
			engines.ChatGPT:    "",
			engines.Claude:     "",
			engines.Perplexity: "",
			engines.Gemini:     "",
		}
	}
	if cfg.Answer == "" {
		cfg.Answer = "This is a canned answer."
	}
	return &Stub{cfg: cfg}
}

func (s *Stub) Name() string { return StubName }

func (s *Stub) Access() Access { return s.cfg.Access }

func (s *Stub) Engines() map[string]string {
	out := make(map[string]string, len(s.cfg.Engines))
	for k, v := range s.cfg.Engines {
		out[k] = v
	}
	return out
}

// Run returns the canned answer, or the canned error.
func (s *Stub) Run(ctx context.Context, req Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	s.mu.Lock()
	s.calls++
	s.seen = append(s.seen, req)
	s.mu.Unlock()

	if _, ok := s.cfg.Engines[req.Engine]; !ok {
		return Response{}, fmt.Errorf("%w: stub cannot reach %s", ErrUnsupportedEngine, req.Engine)
	}
	if s.cfg.Err != nil {
		return Response{}, s.cfg.Err
	}

	text := s.cfg.Answer
	if custom, ok := s.cfg.AnswerFor[req.Prompt]; ok {
		text = custom
	}
	model := s.cfg.Engines[req.Engine]
	if req.Model != "" {
		model = req.Model
	}
	if model == "" {
		model = "stub"
	}
	resp := Response{
		Text:      text,
		Model:     model,
		Citations: append([]Citation(nil), s.cfg.Citations...),
		Calls:     1,
	}
	if s.cfg.Access == AccessAPI {
		// Rough but deterministic, so a usage assertion is stable across runs.
		resp.InputTokens = len(strings.Fields(req.Prompt))
		resp.OutputTokens = len(strings.Fields(text))
	}
	return resp, nil
}

func (s *Stub) Test(ctx context.Context) error { return s.cfg.TestErr }

// Calls is how many times Run was invoked.
func (s *Stub) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// Requests is every request Run received, in order.
func (s *Stub) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.seen...)
}

// RegisterStub adds a stub to a registry and returns it, so a test can both
// resolve targets through the registry and assert on what the provider saw.
func RegisterStub(r *Registry, cfg StubConfig) *Stub {
	stub := NewStub(cfg)
	r.Register(Registration{
		Name:    StubName,
		Access:  stub.Access(),
		Engines: stub.Engines(),
		New:     func(CredentialSource) (Provider, error) { return stub, nil },
	})
	return stub
}
