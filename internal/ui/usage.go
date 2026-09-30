// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"context"
	"fmt"
	"time"

	"github.com/limelit-co/open/internal/provider"
)

// The top bar says what has been used against the limit that stops runs,
// on every page, because running out should never be the first sign.
//
// With a Limelit Cloud key that is the free allowance: credits used this
// month, read from Cloud (GET /v1/relay/allowance, which spends nothing)
// and kept for a minute so a page view is not a network call. Without one,
// the limit is this instance's own: answers fetched today against the daily
// limit in Settings, which is what a pass is refused above.

// allowanceTTL is how long a read of the free allowance is reused.
const allowanceTTL = time.Minute

func (a *App) usageView(ctx context.Context) *UsageView {
	if a.hasCloudKey(ctx) {
		if al, ok := a.freeAllowance(ctx); ok && al.MonthlyCredits > 0 {
			v := meter("Free credits", al.UsedThisMonth, al.MonthlyCredits)
			v.Detail = fmt.Sprintf("%d of %d free Limelit Cloud credits used this month, %d of %d today; an answer uses about one.",
				al.UsedThisMonth, al.MonthlyCredits, al.UsedToday, al.DailyCredits)
			if !al.MonthResetsAt.IsZero() {
				v.Detail += " The month resets " + al.MonthResetsAt.UTC().Format("Jan 2") + "."
			}
			if al.UsedThisMonth >= al.MonthlyCredits {
				v.Detail += " Used up: add your own provider key in Settings, or upgrade to Limelit Cloud."
			}
			return v
		}
	}
	today, err := a.db.RunsToday(ctx)
	if err != nil {
		return nil
	}
	v := meter("Answers today", today, a.runsPerDay(ctx))
	v.Detail = "Answers fetched today against the daily limit in Settings; a pass that would go over it does not run. You pay your providers for these directly."
	return v
}

func meter(label string, used, limit int) *UsageView {
	v := &UsageView{Label: label, Used: used, Limit: limit, Width: "0%"}
	if limit > 0 {
		pct := used * 100 / limit
		if pct > 100 {
			pct = 100
		}
		v.Width = fmt.Sprintf("%d%%", pct)
		v.Warn = used*10 >= limit*9
	}
	return v
}

// freeAllowance reads the allowance for the saved key, reusing a read for
// allowanceTTL. A failed read is reused too, so an offline instance does not
// wait on Cloud for every page.
func (a *App) freeAllowance(ctx context.Context) (provider.LimelitAllowance, bool) {
	key := a.credentials(ctx)(provider.LimelitKeyEnv)
	a.allowance.Lock()
	defer a.allowance.Unlock()
	if a.allowance.key == key && time.Since(a.allowance.at) < allowanceTTL {
		return a.allowance.val, a.allowance.ok
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	al, err := provider.FetchLimelitAllowance(fetchCtx, key)
	cancel()
	a.allowance.key, a.allowance.at, a.allowance.val, a.allowance.ok = key, time.Now(), al, err == nil
	return al, err == nil
}
