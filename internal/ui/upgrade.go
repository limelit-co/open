// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

// The Upgrade screen: what Limelit Cloud adds, and one form that moves this
// instance there. It runs the same upgrade.Run as `limelit upgrade`, so the
// button and the command cannot drift.

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/limelit-co/open/internal/upgrade"
)

// buildUpgrade assembles the page: the hosted-only list, what would move, and
// whether the Cloud key is already in the environment.
func (a *App) buildUpgrade(r *http.Request, base Base) (UpgradePage, error) {
	counts, err := a.db.Counts(r.Context())
	if err != nil {
		return UpgradePage{}, err
	}
	return UpgradePage{
		Base:          base,
		CloudFeatures: cloudFeatures,
		KeyFromEnv:    strings.TrimSpace(os.Getenv(upgrade.KeyEnv)) != "",
		Prompts:       counts.Prompts,
		Competitors:   counts.Competitors,
		Chats:         counts.Chats,
	}, nil
}

func (a *App) upgrade(w http.ResponseWriter, r *http.Request) {
	if !a.configured(r.Context()) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	a.upgradeWith(w, r, func(*UpgradePage) {})
}

func (a *App) upgradeWith(w http.ResponseWriter, r *http.Request, adjust func(*UpgradePage)) {
	base, _, err := a.base(r, "Limelit Cloud", "upgrade")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page, err := a.buildUpgrade(r, base)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	adjust(&page)
	a.write(w, r, "upgrade", page)
}

// runUpgrade pushes this instance to Cloud and shows what arrived.
//
// The key is read from the form, or from the environment when the form left
// it blank, and is never stored, logged or rendered: it is Cloud's
// credential, not this instance's. The request is held open for the upload
// because the result is what the user is waiting to read; Cloud imports in
// one transaction, so a tab closed halfway leaves nothing partial and the
// button can be pressed again.
func (a *App) runUpgrade(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.FormValue("key"))
	if key == "" {
		key = strings.TrimSpace(os.Getenv(upgrade.KeyEnv))
	}
	since := strings.TrimSpace(r.FormValue("since"))
	if since != "" {
		if _, err := time.Parse("2006-01-02", since); err != nil {
			a.upgradeWith(w, r, func(p *UpgradePage) { p.Error = "The start date has to look like 2026-01-31." })
			return
		}
	}

	result, err := upgrade.Run(r.Context(), a.db, upgrade.Options{
		Key:      key,
		Endpoint: os.Getenv(upgrade.EndpointEnv),
		Since:    since,
	})
	if err != nil {
		a.upgradeWith(w, r, func(p *UpgradePage) { p.Error = err.Error() })
		return
	}
	a.log.Info("upgraded to limelit cloud", "prompts", result.Import.Prompts,
		"chats", result.Import.Chats, "skipped", result.Import.ChatsSkipped)
	a.upgradeWith(w, r, func(p *UpgradePage) {
		p.Result = &UpgradeResult{
			Prompts:      result.Import.Prompts,
			Competitors:  result.Import.Competitors,
			Chats:        result.Import.Chats,
			Mentions:     result.Import.Mentions,
			Citations:    result.Import.Citations,
			Skipped:      result.Import.ChatsSkipped,
			WorkspaceURL: result.WorkspaceURL,
			MCPURL:       result.MCPURL,
		}
	})
}
