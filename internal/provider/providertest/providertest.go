// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package providertest builds a registry that mirrors the shipped provider
// catalog, backed by stubs.
//
// It exists so the parser, the runner and the metrics can be tested against
// the real target strings a user will write, before the real providers are
// implemented and without keys or network afterwards. It is a test helper:
// nothing in cmd/ imports it, so these doubles can never be mistaken for a
// working provider in a deployment.
package providertest

import "github.com/limelit-co/open/internal/provider"

// Registry returns a registry holding every documented provider, each backed
// by a stub that answers with canned text.
func Registry() *provider.Registry {
	reg := provider.NewRegistry()
	for _, e := range provider.Catalog() {
		entry := e
		reg.Register(provider.Registration{
			Name:        entry.Name,
			Access:      entry.Access,
			Engines:     entry.Engines,
			Credentials: entry.Credentials,
			New: func(src provider.CredentialSource) (provider.Provider, error) {
				return provider.NewStub(provider.StubConfig{
					Access:  entry.Access,
					Engines: entry.Engines,
					Answer:  "Canned answer from the " + entry.Name + " double.",
				}), nil
			},
		})
	}
	return reg
}
