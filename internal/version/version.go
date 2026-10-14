// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package version says which build of alidade is running, from the build
// information the Go toolchain stamps into every binary: the module
// version — the tag for a release or a go install, a pseudo-version for a
// build from a checkout between tags — the commit, and the Go it was
// built with (#70). Nothing is set at build time by hand, so a locally
// built binary reports itself as faithfully as a released one.
package version

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// Info is one build's identity.
type Info struct {
	// Version is the main module's version: "v0.4.0", a pseudo-version
	// such as "v0.4.1-0.20260913052412-5518c313a159", or "devel" when
	// the toolchain stamped none (a go test binary, say).
	Version string
	// Revision is the commit the binary was built from, "" when the
	// build had no version control information (a go install from the
	// module proxy has the version and not the commit).
	Revision string
	// Modified says the working tree had uncommitted changes.
	Modified bool
	// Go is the toolchain and platform: "go1.27.1 darwin/arm64".
	Go string
	// Embeds names the runtimes linked into this build by its tags —
	// "v8", "cpython 3.14.8" — which the caller fills in: a binary built
	// without them reads the same otherwise and has less in its graph.
	Embeds []string
}

// Read returns the running binary's Info.
func Read() Info {
	info := Info{Version: "devel", Go: runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		info.Version = v
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Revision = s.Value
		case "vcs.modified":
			info.Modified = s.Value == "true"
		}
	}
	return info
}

// String renders an Info on one line: "alidade v0.4.0 (f6cb367, go1.27.1
// darwin/arm64)". A modified tree already shows in the version, which
// the toolchain suffixes "+dirty".
func (i Info) String() string {
	var parts []string
	if i.Revision != "" {
		rev := i.Revision
		if len(rev) > 7 {
			rev = rev[:7]
		}
		parts = append(parts, rev)
	}
	parts = append(parts, i.Go)
	s := strings.Join(parts, ", ")
	if len(i.Embeds) > 0 {
		s += "; " + strings.Join(i.Embeds, ", ")
	}
	return "alidade " + i.Version + " (" + s + ")"
}
