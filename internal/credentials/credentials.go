// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package credentials resolves a provider credential the one way every part
// of this program must resolve it: the environment first, then the encrypted
// settings store.
//
// It is its own package because the dashboard's Test button and the runner
// both need it, and a Test that proved a different value than a run would use
// is worse than no Test at all.
package credentials

import (
	"context"
	"log/slog"

	"github.com/limelit-co/open/internal/config"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/secrets"
	"github.com/limelit-co/open/internal/store"
)

// Prefix is the settings-key prefix a stored credential lives under.
const Prefix = "credential:"

// Source returns a reader for the credentials this instance holds.
//
// An exported environment variable always wins. That ordering is what lets a
// deployment override a value someone pasted into the dashboard without
// anyone having to find and clear the stored one.
func Source(ctx context.Context, db *store.DB, keys *secrets.Keyring, log *slog.Logger) provider.CredentialSource {
	return func(name string) string {
		if v := config.Credential(name); v != "" {
			return v
		}
		if db == nil || keys == nil {
			return ""
		}
		sealed, err := db.Setting(ctx, Prefix+name)
		if err != nil || sealed == "" {
			return ""
		}
		value, err := keys.Unseal(sealed)
		if err != nil {
			// Nearly always a changed LIMELIT_SECRET. Treating it as absent
			// rather than as an error makes the failure read as "no key",
			// which is the state the user has to fix anyway, and the log
			// carries the real cause.
			if log != nil {
				log.Error("stored credential could not be decrypted", "credential", name, "error", err)
			}
			return ""
		}
		return value
	}
}
