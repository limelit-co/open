// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package main

import (
	"regexp"
	"runtime/debug"
	"strings"
)

// version and commit are stamped at build time with -ldflags. A release sets
// both. A build without them falls back to what Go recorded: the VCS stamp
// when built inside a checkout, or the module version when built by
// `go install ...@version`, which has no checkout and so no VCS stamp.
var (
	version = ""
	commit  = ""
)

// buildInfo reports the version to show and the commit this binary was built
// from. The commit is a 12-hex revision, with -dirty when the tree had
// uncommitted changes, or "" when nothing about the build names one.
func buildInfo() (string, string) {
	info, _ := debug.ReadBuildInfo()
	return resolveBuild(version, commit, info)
}

var (
	// pseudoRev is the revision at the end of a Go pseudo-version, such as
	// v0.0.0-20260928194719-311fa97b3476.
	pseudoRev = regexp.MustCompile(`\d{14}-([0-9a-f]{12})(\+incompatible)?$`)
	// hexStamp is a bare revision used as a version, which is how the
	// container build is stamped when it is given a commit and nothing else.
	hexStamp = regexp.MustCompile(`^[0-9a-f]{7,40}(-dirty)?$`)
)

func resolveBuild(ldVersion, ldCommit string, info *debug.BuildInfo) (string, string) {
	v, c := strings.TrimSpace(ldVersion), shortRev(strings.TrimSpace(ldCommit))
	if info != nil {
		revision, modified := "", false
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
		if c == "" && revision != "" {
			c = shortRev(revision)
			if modified {
				c += "-dirty"
			}
		}
		if mv := info.Main.Version; v == "" && mv != "" && mv != "(devel)" {
			v = mv
		}
		if m := pseudoRev.FindStringSubmatch(info.Main.Version); c == "" && m != nil {
			c = m[1]
		}
	}
	if c == "" && hexStamp.MatchString(v) {
		c = shortRev(v)
	}
	switch {
	case v != "":
		return v, c
	case c != "":
		return c, c
	default:
		return "dev", ""
	}
}

// shortRev cuts a full revision to the 12 characters shown everywhere else,
// keeping a -dirty suffix.
func shortRev(rev string) string {
	head, dirty := strings.CutSuffix(rev, "-dirty")
	if len(head) > 12 {
		head = head[:12]
	}
	if dirty {
		return head + "-dirty"
	}
	return head
}

// versionLine is what `limelit version` prints.
func versionLine() string {
	v, c := buildInfo()
	if c == "" || c == v {
		return v
	}
	return v + " (commit " + c + ")"
}
