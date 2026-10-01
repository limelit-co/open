// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/limelit-co/open/internal/provider/providertest"
)

// fakeGitHub answers the repository read with stars, or with status when it
// is set; it counts the reads.
func fakeGitHub(t *testing.T, stars int, status *atomic.Int32, reads *atomic.Int32) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.URL.Path != "/repos/limelit-co/open" || !strings.HasPrefix(r.Header.Get("User-Agent"), "LimelitOpen/") {
			http.NotFound(w, r)
			return
		}
		if s := status.Load(); s != 0 {
			w.WriteHeader(int(s))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"full_name":"limelit-co/open","stargazers_count":` + strconv.Itoa(stars) + `}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LIMELIT_GITHUB_API", srv.URL)
}

// waitForStars waits for the read New starts to finish.
func waitForStars(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.stars.Lock()
		done := !a.stars.busy && !a.stars.at.IsZero()
		a.stars.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the star count was never read")
}

// TestTheStarLinkShowsTheCount the way GitHub's own button does, read once
// and reused rather than on every page.
func TestTheStarLinkShowsTheCount(t *testing.T) {
	var status, reads atomic.Int32
	fakeGitHub(t, 145, &status, &reads)
	a, _, h := newApp(t, providertest.Registry())
	waitForStars(t, a)
	seedProperty(t, h)
	for range 3 {
		body := get(t, h, "/prompts").Body.String()
		if !strings.Contains(body, `<span class="gh-star-count">145</span>`) || !strings.Contains(body, "(145 stars)") {
			t.Fatal("the star link does not show the count")
		}
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("GitHub was read %d times, want 1", n)
	}
}

// TestWithoutGitHubTheStarLinkHasNoCount, and the page does not wait.
func TestWithoutGitHubTheStarLinkHasNoCount(t *testing.T) {
	a, _, h := newApp(t, providertest.Registry())
	waitForStars(t, a)
	seedProperty(t, h)
	body := get(t, h, "/prompts").Body.String()
	if strings.Contains(body, "gh-star-count") || !strings.Contains(body, `class="gh-star-label">Star<`) {
		t.Error("an unread count shows, or the link is gone")
	}
}

// TestAFailedReadKeepsTheLastCount rather than dropping it.
func TestAFailedReadKeepsTheLastCount(t *testing.T) {
	var status, reads atomic.Int32
	fakeGitHub(t, 1234, &status, &reads)
	a, _, _ := newApp(t, providertest.Registry())
	waitForStars(t, a)
	status.Store(http.StatusForbidden) // GitHub's answer once the hourly limit is used
	a.refreshStars(context.Background())
	if n, ok := a.starCount(); !ok || n != 1234 {
		t.Errorf("count after a failed read = %d, %v", n, ok)
	}
}

func TestStarsLabel(t *testing.T) {
	for n, want := range map[int]string{0: "0", 145: "145", 999: "999", 1000: "1k", 1234: "1.2k", 1999: "1.9k", 12345: "12.3k", 120000: "120k"} {
		if got := starsLabel(n); got != want {
			t.Errorf("starsLabel(%d) = %q, want %q", n, got, want)
		}
	}
}
