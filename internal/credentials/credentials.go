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
	"sort"

	"github.com/limelit-co/open/internal/config"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/secrets"
	"github.com/limelit-co/open/internal/store"
	"github.com/limelit-co/open/internal/target"
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

// Save seals value and stores it as credential name, where Source finds it.
// The dashboard and `limelit login` both write through here, and a running
// server reads the store on every call, so a key saved by either is in use
// at once, with no restart.
func Save(ctx context.Context, db *store.DB, keys *secrets.Keyring, name, value string) error {
	sealed, err := keys.Seal(value)
	if err != nil {
		return err
	}
	return db.SetSetting(ctx, Prefix+name, sealed)
}

// Track adds a target for every engine entry's key reaches, through every
// registration built in reg that takes the same key. Connecting a key means
// wanting to track what it reaches; a saved key with no target leaves the
// Run button disabled for a reason nobody can see. API targets are online,
// the only switch a click can sensibly set. It returns the specs tracked.
func Track(ctx context.Context, db *store.DB, reg *provider.Registry, entry provider.CatalogEntry) ([]string, error) {
	var tracked []string
	for _, e := range provider.SharedKeyEntries(entry) {
		if _, built := reg.Lookup(e.Name); !built {
			continue
		}
		for engine := range e.Engines {
			spec := engine + ":" + e.Name
			if e.Access == provider.AccessAPI {
				spec += ":online"
			}
			parsed, err := target.Parse(spec)
			if err != nil || parsed.Validate(reg) != nil {
				continue
			}
			access, _ := parsed.Access(reg)
			if _, err := db.AddTarget(ctx, store.Target{
				Spec: parsed.String(), Engine: parsed.Engine, Provider: parsed.Provider,
				Model: parsed.Model, Online: parsed.Online, Access: string(access),
			}); err != nil {
				return tracked, err
			}
			tracked = append(tracked, parsed.String())
		}
	}
	sort.Strings(tracked)
	return tracked, nil
}
