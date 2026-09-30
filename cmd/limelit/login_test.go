// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/limelit-co/open/internal/config"
	"github.com/limelit-co/open/internal/credentials"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/secrets"
	"github.com/limelit-co/open/internal/store"
)

// loginEnv points login at a fresh data directory with no key anywhere.
func loginEnv(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	t.Setenv(config.DataDirEnv, dir)
	t.Setenv(provider.LimelitKeyEnv, "")
	t.Setenv("LIMELIT_SECRET", "")
	return dir
}

func cloudSays(a provider.LimelitAllowance, err error) func(context.Context, string) (provider.LimelitAllowance, error) {
	return func(context.Context, string) (provider.LimelitAllowance, error) { return a, err }
}

// savedKey reads the key back the way limelit serve does.
func savedKey(t *testing.T, dir string) (string, []store.Target) {
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
	targets, err := db.Targets(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	return credentials.Source(context.Background(), db, keys, nil)(provider.LimelitKeyEnv), targets
}

// TestLoginSavesTheKeyWhereServeReadsIt. The key lands, encrypted, in the
// store limelit serve reads on every call, and every engine it reaches is
// tracked, so Run works without a restart or a trip to Settings.
func TestLoginSavesTheKeyWhereServeReadsIt(t *testing.T) {
	dir := loginEnv(t)
	var out strings.Builder
	err := login(context.Background(), loginIO{
		in:        strings.NewReader("export LIMELIT_CLOUD_KEY=lmlt_abc123\n"),
		out:       &out,
		allowance: cloudSays(provider.LimelitAllowance{Enabled: true, MonthlyCredits: 1000, UsedThisMonth: 12}, nil),
	}, "", false)
	if err != nil {
		t.Fatal(err)
	}

	key, targets := savedKey(t, dir)
	if key != "lmlt_abc123" {
		t.Errorf("serve would read %q", key)
	}
	specs := map[string]bool{}
	for _, tg := range targets {
		specs[tg.Spec] = true
	}
	for _, want := range []string{"chatgpt:limelit", "gemini:limelit", "ai_overview:limelit", "ai_mode:limelit", "perplexity:limelitapi:online"} {
		if !specs[want] {
			t.Errorf("not tracking %s: %v", want, specs)
		}
	}
	for _, want := range []string{provider.LimelitKeyPage, dir, "988 of 1000", "ChatGPT", "Perplexity", "finish setup"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "limelit.db"))
	if strings.Contains(string(raw), "lmlt_abc123") {
		t.Error("the key is stored in plain text")
	}

	// Logging in again replaces the key and adds no second set of targets.
	if err := login(context.Background(), loginIO{out: &out, allowance: cloudSays(provider.LimelitAllowance{}, nil)}, "lmlt_second", false); err != nil {
		t.Fatal(err)
	}
	if key, again := savedKey(t, dir); key != "lmlt_second" || len(again) != len(targets) {
		t.Errorf("second login: key %q, %d targets (was %d)", key, len(again), len(targets))
	}
}

// TestLoginSavesNothingForAKeyCloudRejects.
func TestLoginSavesNothingForAKeyCloudRejects(t *testing.T) {
	dir := loginEnv(t)
	err := login(context.Background(), loginIO{
		out:       &strings.Builder{},
		allowance: cloudSays(provider.LimelitAllowance{}, fmt.Errorf("%w: rejected", provider.ErrAuth)),
	}, "lmlt_wrong", false)
	if err == nil || !strings.Contains(err.Error(), "nothing was saved") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "limelit.db")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a rejected key still opened the store")
	}
}

// TestLoginSavesAnywayWhenCloudIsUnreachable. A laptop offline for a moment
// should not lose the paste; the first run reports a wrong key.
func TestLoginSavesAnywayWhenCloudIsUnreachable(t *testing.T) {
	dir := loginEnv(t)
	var out strings.Builder
	err := login(context.Background(), loginIO{
		out:       &out,
		allowance: cloudSays(provider.LimelitAllowance{}, errors.New("dial tcp: no route to host")),
	}, "lmlt_offline", false)
	if err != nil {
		t.Fatal(err)
	}
	if key, _ := savedKey(t, dir); key != "lmlt_offline" || !strings.Contains(out.String(), "Could not reach Limelit Cloud") {
		t.Errorf("key %q, output:\n%s", key, out.String())
	}
}

// TestLoginOpensTheKeyPageOnlyForAPersonAtATerminal.
func TestLoginOpensTheKeyPageOnlyForAPersonAtATerminal(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		interactive, noBrowser bool
		wantOpen               bool
	}{
		{"terminal", true, false, true},
		{"terminal, --no-browser", true, true, false},
		{"piped", false, false, false},
	} {
		loginEnv(t)
		var opened string
		var out strings.Builder
		err := login(context.Background(), loginIO{
			in: strings.NewReader("lmlt_typed\n"), out: &out, interactive: tc.interactive,
			openURL:   func(u string) error { opened = u; return nil },
			allowance: cloudSays(provider.LimelitAllowance{}, nil),
		}, "", tc.noBrowser)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := opened == provider.LimelitKeyPage; got != tc.wantOpen {
			t.Errorf("%s: opened %q", tc.name, opened)
		}
		if tc.interactive != strings.Contains(out.String(), "Key: ") {
			t.Errorf("%s: prompt shown = %v", tc.name, !tc.interactive)
		}
	}
}

func TestLoginRefusesAnEmptyPaste(t *testing.T) {
	loginEnv(t)
	err := login(context.Background(), loginIO{in: strings.NewReader("\n"), out: &strings.Builder{}, interactive: true,
		allowance: cloudSays(provider.LimelitAllowance{}, nil)}, "", true)
	if err == nil || !strings.Contains(err.Error(), provider.LimelitKeyPage) {
		t.Errorf("err = %v", err)
	}
}

func TestCleanKeyTakesTheKeyHoweverItWasCopied(t *testing.T) {
	for in, want := range map[string]string{
		"lmlt_a":                              "lmlt_a",
		"  lmlt_a \n":                         "lmlt_a",
		"export LIMELIT_CLOUD_KEY=lmlt_a":     "lmlt_a",
		`LIMELIT_CLOUD_KEY="lmlt_a"`:          "lmlt_a",
		"export LIMELIT_CLOUD_KEY='lmlt_a'\n": "lmlt_a",
	} {
		if got := cleanKey(in); got != want {
			t.Errorf("cleanKey(%q) = %q", in, got)
		}
	}
}
