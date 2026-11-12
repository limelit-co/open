// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/limelit-co/open/internal/config"
	"github.com/limelit-co/open/internal/credentials"
	"github.com/limelit-co/open/internal/prnow"
	"github.com/limelit-co/open/internal/prnow/prnowtest"
	"github.com/limelit-co/open/internal/secrets"
	"github.com/limelit-co/open/internal/store"
)

const prnowKey = "prnow_cli_test_key"

// prnowEnv is a fresh data directory, a fake PRNow and the IO to drive the
// commands. The key is in the environment unless a test clears it.
func prnowEnv(t *testing.T) (*prnowtest.Server, prnowIO, *strings.Builder, *[]time.Duration) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	t.Setenv(config.DataDirEnv, dir)
	t.Setenv("LIMELIT_SECRET", "")
	t.Setenv(prnow.KeyEnv, prnowKey)
	s := prnowtest.New(t, prnowKey)
	var out strings.Builder
	var slept []time.Duration
	return s, prnowIO{
		in:  strings.NewReader(""),
		out: &out,
		client: func(key string) *prnow.Client {
			c := prnow.New(key)
			c.BaseURL = s.BaseURL()
			return c
		},
		sleep: func(d time.Duration) { slept = append(slept, d) },
	}, &out, &slept
}

func article(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release.txt")
	body := "# Example Co opens its first office in Berlin today\n\n" +
		strings.Repeat("Example Co said the new office will serve customers across Europe. ", 30) + "\n\n" +
		strings.Repeat("The team expects to hire engineers and support staff this year. ", 10)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func releaseArgs(t *testing.T, extra ...string) []string {
	return append([]string{"submit", "-file", article(t), "-category", "Business", "-country", "Germany", "-city", "Berlin", "-plan", "standard"}, extra...)
}

// TestSubmitWithoutYesSendsNothing: the default answer to "submit" is the
// check and the account's credits, like dry_run on the run tools.
func TestSubmitWithoutYesSendsNothing(t *testing.T) {
	s, io, out, _ := prnowEnv(t)
	if err := prnowMain(context.Background(), io, releaseArgs(t)); err != nil {
		t.Fatal(err)
	}
	if n := s.Count("POST", "/submit"); n != 0 {
		t.Fatalf("%d submits without --yes", n)
	}
	for _, want := range []string{"Example Co opens its first office in Berlin today", "Credits: 120", "Nothing was sent", "--yes"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestSubmitWithYesSubmitsOnce(t *testing.T) {
	s, io, out, _ := prnowEnv(t)
	if err := prnowMain(context.Background(), io, releaseArgs(t, "-yes")); err != nil {
		t.Fatal(err)
	}
	if n := s.Count("POST", "/submit"); n != 1 {
		t.Fatalf("%d submits, want 1", n)
	}
	for _, want := range []string{"Submitted: release G9E1HJK is pending", "limelit prnow status G9E1HJK", "limelit prnow retract G9E1HJK"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	var sent prnow.Draft
	if err := json.Unmarshal([]byte(s.Requests()[len(s.Requests())-1].Body), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Title != "Example Co opens its first office in Berlin today" || !strings.HasPrefix(sent.Content, "<p>Example Co said") {
		t.Errorf("the title line was not taken as the title, or the text not turned into HTML: %+v", sent)
	}
}

// TestARetriedSubmitKeepsItsIdempotencyKey: asking again after an in-flight
// answer must reuse the key, or PRNow could charge twice.
func TestARetriedSubmitKeepsItsIdempotencyKey(t *testing.T) {
	s, io, _, slept := prnowEnv(t)
	s.On("POST /submit",
		prnowtest.Reply{Status: 409, Fixture: "in_progress.json", Header: map[string]string{"Retry-After": "2"}},
		prnowtest.Reply{Status: 200, Fixture: "submit.json"})
	if err := prnowMain(context.Background(), io, releaseArgs(t, "-yes")); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range s.Requests() {
		if r.Path == "/submit" {
			keys = append(keys, r.IdempotencyKey)
		}
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("idempotency keys across the retry = %q, want the same one twice", keys)
	}
	if len(*slept) != 1 || (*slept)[0] != 2*time.Second {
		t.Fatalf("waited %v, want Retry-After's 2s", *slept)
	}
}

func TestADuplicateNamesTheExistingRelease(t *testing.T) {
	s, io, _, _ := prnowEnv(t)
	s.On("POST /submit", prnowtest.Reply{Status: 409, Fixture: "duplicate.json"})
	err := prnowMain(context.Background(), io, releaseArgs(t, "-yes"))
	if err == nil || !strings.Contains(err.Error(), "G9E1HJK") || !strings.Contains(err.Error(), "nothing new was submitted") {
		t.Fatalf("err = %v", err)
	}
	if n := s.Count("POST", "/submit"); n != 1 {
		t.Fatalf("a duplicate was retried: %d submits", n)
	}
}

func TestAReleaseOutsideTheLimitsNeverReachesPRNow(t *testing.T) {
	s, io, _, _ := prnowEnv(t)
	short := filepath.Join(t.TempDir(), "short.txt")
	if err := os.WriteFile(short, []byte("Too short to publish."), 0o600); err != nil {
		t.Fatal(err)
	}
	err := prnowMain(context.Background(), io, []string{"submit", "-file", short, "-title", "Short", "-yes"})
	if err == nil {
		t.Fatal("submitted a release that breaks every limit")
	}
	for _, want := range []string{"content is", "category", "country", "city", "--plan is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if n := len(s.Requests()); n != 0 {
		t.Fatalf("%d requests for a release that could not pass", n)
	}
}

func TestStatusPrintsWhereItWentLive(t *testing.T) {
	_, io, out, _ := prnowEnv(t)
	if err := prnowMain(context.Background(), io, []string{"status", "G9E1HJK"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"in_progress", "news-outlet-a: live https://news.example.org/example-co-berlin", "news-outlet-b: held (editor review)", "4 credits refunded"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestPRNowLoginSealsTheKey: the key is checked, then stored encrypted
// where every prnow command reads it, and a refused key is not stored.
func TestPRNowLoginSealsTheKey(t *testing.T) {
	_, io, out, _ := prnowEnv(t)
	t.Setenv(prnow.KeyEnv, "")
	dir := config.DataDir()

	io.in = strings.NewReader("prnow_not_the_key\n")
	if err := prnowMain(context.Background(), io, []string{"login"}); err == nil {
		t.Fatal("a refused key was accepted")
	}
	if got := storedPRNowKey(t, dir); got != "" {
		t.Fatalf("a refused key was saved: %q", got)
	}

	io.in = strings.NewReader("export PRNOW_API_KEY=\"" + prnowKey + "\"\n")
	if err := prnowMain(context.Background(), io, []string{"login"}); err != nil {
		t.Fatal(err)
	}
	if got := storedPRNowKey(t, dir); got != prnowKey {
		t.Fatalf("stored key = %q", got)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "limelit.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), prnowKey) {
		t.Fatal("the key is in the database in plaintext")
	}
	if strings.Contains(out.String(), prnowKey) {
		t.Fatal("login printed the key")
	}
	if !strings.Contains(out.String(), "Example Co") {
		t.Errorf("login did not show the account:\n%s", out)
	}

	// The saved key now drives the other commands with nothing in the env.
	out.Reset()
	if err := prnowMain(context.Background(), io, []string{"account"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Credits: 120 in total, 100 payable from one wallet") {
		t.Errorf("account output:\n%s", out)
	}
}

func storedPRNowKey(t *testing.T, dir string) string {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(dir, "limelit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	keys, err := secrets.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return credentials.Source(context.Background(), db, keys, nil)(prnow.KeyEnv)
}

func TestCleanPRNowKey(t *testing.T) {
	for in, want := range map[string]string{
		"prnow_abc":                        "prnow_abc",
		"  prnow_abc \n":                   "prnow_abc",
		"export PRNOW_API_KEY=prnow_abc":   "prnow_abc",
		`PRNOW_API_KEY="prnow_abc"`:        "prnow_abc",
		"Bearer prnow_abc":                 "prnow_abc",
		"export PRNOW_API_KEY='prnow_abc'": "prnow_abc",
	} {
		if got := cleanPRNowKey(in); got != want {
			t.Errorf("cleanPRNowKey(%q) = %q, want %q", in, got, want)
		}
	}
}
