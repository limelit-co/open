// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

// Demo mode: a public, read-only instance that keeps running on a schedule.
//
// The point of a hosted demo is to show real history accumulating on real
// engines, which a screenshot cannot. The risk is that it is public: anyone
// can reach the Run button and spend the operator's provider keys, and
// Settings would show which providers are configured. So in demo mode every
// mutating route is refused with an explanation, the credential surface is
// hidden entirely rather than masked, and the scheduler keeps working because
// the scheduler is the whole reason the demo has data.

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/limelit-co/open/internal/config"
)

// DemoEnv turns demo mode on. Any non-empty value.
const DemoEnv = "LIMELIT_DEMO"

// DemoMode reports whether this instance is a public read-only demo.
func DemoMode() bool {
	return strings.TrimSpace(os.Getenv(DemoEnv)) != ""
}

// readOnly wraps a mutating handler so it refuses in demo mode.
//
// A redirect with a flash rather than a bare 403, because the person who
// pressed the button is a visitor who did nothing wrong and should land back
// on a page that says so.
func (a *App) readOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.demo {
			back := r.Header.Get("Referer")
			if back == "" || !strings.HasPrefix(back, "/") && !strings.Contains(back, r.Host) {
				back = "/overview"
			}
			sep := "?"
			if strings.Contains(back, "?") {
				sep = "&"
			}
			http.Redirect(w, r, back+sep+"flash=demo-readonly", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// noSetup handles the wizard's GET pages in demo mode.
//
// A configured demo sends them to the overview: a visitor landing on step one
// of a wizard whose submit buttons refuse would reasonably conclude the
// software is broken. An UNconfigured demo is the operator's mistake, and it
// gets a plain 503 that says so, because the alternative is a redirect loop
// between /overview (not configured, go to /setup) and here (demo, go to
// /overview) that a visitor would see as a hung page.
func (a *App) noSetup(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.demo {
			next(w, r)
			return
		}
		if !a.configured(r.Context()) {
			http.Error(w, "This demo instance has not been configured yet. Set "+DemoEnv+
				" only after running the setup wizard once without it.", http.StatusServiceUnavailable)
			return
		}
		http.Redirect(w, r, "/overview", http.StatusSeeOther)
	}
}

// scheduled reports whether this instance runs passes on its own. The demo
// banner claims a schedule only when there is one.
func (a *App) scheduled(ctx context.Context) bool {
	_, ok := config.ScheduleInterval(a.ScheduleMode(ctx))
	return ok
}
