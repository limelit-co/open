// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// The star link shows how many people starred the repository, read from
// GitHub's public API (GET /repos/limelit-co/open, no token) at most once an
// hour. The read runs in the background: a page never waits on GitHub, and an
// instance that cannot reach it shows the link without a count.

// starsTTL is how long a read of the star count is reused.
const starsTTL = time.Hour

// githubAPI is GitHub's API host. LIMELIT_GITHUB_API overrides it, for tests.
func githubAPI() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("LIMELIT_GITHUB_API")), "/"); v != "" {
		return v
	}
	return "https://api.github.com"
}

// starCount returns the last star count read, and starts a fresh read in the
// background when that one is older than starsTTL.
func (a *App) starCount() (int, bool) {
	a.stars.Lock()
	defer a.stars.Unlock()
	if !a.stars.busy && time.Since(a.stars.at) >= starsTTL {
		a.stars.busy = true
		go a.refreshStars(context.Background())
	}
	return a.stars.n, a.stars.ok
}

// refreshStars reads the star count. A failed read keeps the last count and
// is not retried until starsTTL has passed.
func (a *App) refreshStars(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	n, err := fetchStars(ctx, a.userAgent())
	a.stars.Lock()
	defer a.stars.Unlock()
	a.stars.busy, a.stars.at = false, time.Now()
	if err != nil {
		a.log.Debug("star count unavailable", "err", err)
		return
	}
	a.stars.n, a.stars.ok = n, true
}

func fetchStars(ctx context.Context, userAgent string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPI()+"/repos/limelit-co/open", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("github answered %s", resp.Status)
	}
	var repo struct {
		Stars *int `json:"stargazers_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
		return 0, err
	}
	if repo.Stars == nil || *repo.Stars < 0 {
		return 0, fmt.Errorf("github answered no star count")
	}
	return *repo.Stars, nil
}

// starsLabel writes a count the way GitHub does: 145, 1.2k, 12.3k.
func starsLabel(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	s := strconv.FormatFloat(float64(n/100)/10, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0") + "k"
}
