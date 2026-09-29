// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package main

import (
	"runtime/debug"
	"testing"
)

// TestResolveBuildNamesTheCommitEveryWayABinaryIsBuilt. Each way this binary
// gets built leaves a different trace of what it is; every one of them has to
// come out as a version and, where the build knows it, the commit.
func TestResolveBuildNamesTheCommitEveryWayABinaryIsBuilt(t *testing.T) {
	const sha = "311fa97b3476440a9bd21a60a800bf73f7cd95dc"
	vcs := func(rev string, modified bool) []debug.BuildSetting {
		m := "false"
		if modified {
			m = "true"
		}
		return []debug.BuildSetting{{Key: "vcs.revision", Value: rev}, {Key: "vcs.modified", Value: m}}
	}
	cases := []struct {
		name                string
		ldVersion, ldCommit string
		info                *debug.BuildInfo
		wantV, wantC        string
	}{
		{
			name:      "release: goreleaser stamps both",
			ldVersion: "v0.1.0", ldCommit: sha,
			info:  &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			wantV: "v0.1.0", wantC: "311fa97b3476",
		},
		{
			name:  "go install at a pseudo-version: no VCS stamp, the commit is in the version",
			info:  &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20260928194719-311fa97b3476"}},
			wantV: "v0.0.0-20260928194719-311fa97b3476", wantC: "311fa97b3476",
		},
		{
			name:  "go install at a tag: the version is all there is",
			info:  &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}},
			wantV: "v0.1.0", wantC: "",
		},
		{
			name:  "go build in a clean checkout",
			info:  &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20260928194719-311fa97b3476"}, Settings: vcs(sha, false)},
			wantV: "v0.0.0-20260928194719-311fa97b3476", wantC: "311fa97b3476",
		},
		{
			name:  "go build in a dirty checkout: shown, marked dirty",
			info:  &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: vcs(sha, true)},
			wantV: "311fa97b3476-dirty", wantC: "311fa97b3476-dirty",
		},
		{
			name:      "container built by Cloud Build: the commit arrives as the version",
			ldVersion: "311fa97b3476",
			info:      &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			wantV:     "311fa97b3476", wantC: "311fa97b3476",
		},
		{
			name:      "container built with no arguments",
			ldVersion: "dev",
			info:      &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			wantV:     "dev", wantC: "",
		},
		{
			name:  "nothing at all",
			wantV: "dev", wantC: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, c := resolveBuild(tc.ldVersion, tc.ldCommit, tc.info)
			if v != tc.wantV || c != tc.wantC {
				t.Errorf("got version %q commit %q, want %q %q", v, c, tc.wantV, tc.wantC)
			}
		})
	}
}
