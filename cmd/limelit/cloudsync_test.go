// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/runner"
	"github.com/limelit-co/open/internal/secrets"
	"github.com/limelit-co/open/internal/store"
	"github.com/limelit-co/open/internal/upgrade"
)

// syncRig is an instance ready to run a pass, a Cloud that counts the
// imports it finished, and a gate that holds every import until opened.
type syncRig struct {
	db       *store.DB
	keys     *secrets.Keyring
	registry *provider.Registry
	imported atomic.Int32
	gate     chan struct{}
}

func newSyncRig(t *testing.T, syncOn bool) *syncRig {
	t.Helper()
	ctx := context.Background()
	rig := &syncRig{gate: make(chan struct{})}
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-rig.gate
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"import":{"import_id":"imp","chats_imported":1}}`))
		rig.imported.Add(1)
	}))
	t.Cleanup(cloud.Close)
	t.Setenv(upgrade.EndpointEnv, cloud.URL)
	t.Setenv(provider.LimelitKeyEnv, "llk_test")
	t.Setenv("LIMELIT_SECRET", "test-secret")

	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "limelit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if rig.keys, err = secrets.Open(dir); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProperty(ctx, store.Property{Name: "Acme", Domain: "acme.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddPrompt(ctx, store.Prompt{Text: "best widget tools", Active: true}); err != nil {
		t.Fatal(err)
	}
	rig.registry = provider.NewRegistry()
	provider.RegisterStub(rig.registry, provider.StubConfig{Answer: "Acme leads."})
	if _, err := db.AddTarget(ctx, store.Target{Spec: "chatgpt:stub", Engine: "chatgpt", Provider: provider.StubName, Access: "api"}); err != nil {
		t.Fatal(err)
	}
	if err := upgrade.SetSyncOn(ctx, db, syncOn); err != nil {
		t.Fatal(err)
	}
	rig.db = db
	return rig
}

func (rig *syncRig) runner(mode syncAfter) *runner.Runner {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return newRunner(context.Background(), rig.db, rig.registry, rig.keys, log, mode)
}

// waitFor polls cond until it holds or a few seconds pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRunCopiesToCloudBeforeItReturns. `limelit run` exits right after its
// pass, which would kill a copy left in the background, so the copy is done
// by the time Run returns.
func TestRunCopiesToCloudBeforeItReturns(t *testing.T) {
	rig := newSyncRig(t, true)
	close(rig.gate)

	if _, err := rig.runner(syncBeforeExit).Run(context.Background(), runner.Options{}); err != nil {
		t.Fatal(err)
	}
	if n := rig.imported.Load(); n != 1 {
		t.Fatalf("Cloud had finished %d imports when Run returned, want 1", n)
	}
	st, _ := upgrade.State(context.Background(), rig.db)
	if st.Last == nil || !st.Last.OK || st.Since == "" {
		t.Errorf("the copy was not recorded: %+v", st)
	}
}

// TestServeCopiesToCloudWithoutHoldingThePass. In serve and mcp the pass
// returns while Cloud is still importing, and the copy lands afterwards.
func TestServeCopiesToCloudWithoutHoldingThePass(t *testing.T) {
	rig := newSyncRig(t, true)

	done := make(chan error, 1)
	go func() {
		_, err := rig.runner(syncInBackground).Run(context.Background(), runner.Options{})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the pass waited for Limelit Cloud")
	}
	if n := rig.imported.Load(); n != 0 {
		t.Fatalf("Cloud finished %d imports before it was let through", n)
	}
	close(rig.gate)
	waitFor(t, "the background copy", func() bool {
		st, _ := upgrade.State(context.Background(), rig.db)
		return st.Last != nil && st.Last.OK
	})
}

// TestNoCopyWhileSyncIsOff. Sync is opt-in: with it off, a pass sends
// nothing to Limelit Cloud, whatever key is saved.
func TestNoCopyWhileSyncIsOff(t *testing.T) {
	rig := newSyncRig(t, false)
	close(rig.gate)
	if _, err := rig.runner(syncBeforeExit).Run(context.Background(), runner.Options{}); err != nil {
		t.Fatal(err)
	}
	if n := rig.imported.Load(); n != 0 {
		t.Fatalf("a pass with sync off sent %d imports", n)
	}
}
