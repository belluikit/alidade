// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import "github.com/trendvidia/alidade/internal/scope"

// Options controls what the Go adapter loads and emits. Both inclusion
// flags are explicit here because caliper's own defaults would decide them
// silently — and one of those defaults, SkipGenerated, does not even reach
// the call graph (ADR 004).
type Options struct {
	// Skip are the directories left out besides the layer's own
	// (ADR 003, amended for #215): .alidade.pxf's list, vendor and
	// third_party by default. Nil is the default.
	Skip *scope.Dirs
	// Module restricts the graph to one module: every go.mod under the
	// directory is loaded otherwise (#67), which is the usual case. A
	// path naming none of them loads nothing.
	Module string

	// Tests includes _test.go files and the test variants of packages.
	// Tests are roughly half the Go source in some repositories and are
	// where intent is documented; they also roughly double the corpus,
	// so this is a choice, not a default (ADR 004 rule 4).
	Tests bool

	// ExactTests, with Tests, makes the production view's Go nodes and
	// Go-to-Go edges the build without tests exactly (#133, the owner's
	// decision): the production graph comes from a load without tests, and
	// the load with them contributes only test declarations, the external
	// packages they reach, and the edges touching a test declaration. One
	// cross-layer gap remains under both modes (#139, ADR 004): a Markdown
	// mention that test declarations make ambiguous stops resolving.
	//
	// Loading tests widens the program — more packages, so more
	// implementations for an interface call — and without this a production
	// interface call can gain package-level edges into packages only tests
	// bring in (go://testing on govm). It costs a second load: 47–84% on
	// the repositories measured.
	ExactTests bool

	// Configs asks for build configurations beyond the host's, so that
	// files behind //go:build constraints the host does not satisfy are
	// in the graph too (#43). "" loads the host's only; "auto" reads the
	// constraints of the files the host's load excluded and picks, from
	// linux, darwin and windows on amd64 and arm64, the fewest
	// configurations that bring them in; "linux/amd64,windows/arm64"
	// names them. Each extra configuration is a further load and call
	// graph, and adds only what the host's did not, marked build=<config>.
	Configs string
	// DepsFromExportData takes dependencies' types from the compiler's
	// export data instead of type-checking their source (caliper v1.5.0).
	// The module's own packages are unchanged; a dependency's functions
	// have no bodies, which this layer never reads — a call that leaves
	// the module is kept at package granularity — so the module's graph
	// is the same, the boundary loses class-hierarchy fan-out into
	// dependency implementations, and the load is 3–4× cheaper. On by
	// default since 2026-09-12 (#58); DepsFromSource is the way back.
	DepsFromExportData bool
}

// DefaultOptions returns the options ADR 004 asks for: tests out, module
// detected from the packages. Generated code is always in: it is the
// bridge between a schema and the code that uses it, and the option that
// left it out cut that bridge for no saving in build time or memory, so
// it was removed (#126).
func DefaultOptions() Options {
	return Options{DepsFromExportData: true}
}
