// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import (
	"strings"

	"github.com/belluikit/alidade/internal/scope"
)

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
	// linux, darwin and windows on amd64 and arm64, js/wasm, wasip1/wasm,
	// android/arm64 and ios/arm64, each also with every build tag those
	// constraints name set (#48), the fewest configurations that bring
	// them in; "linux/amd64,windows/arm64+gl" names them, a configuration's
	// build tags after its platform. Each extra configuration is a further
	// load and call graph, and adds only what the ones before it did not,
	// marked build=<config>.
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

	// Only, when not nil, loads only the packages with these import paths
	// and emits only their declarations and the edges leaving them: a
	// worktree's changed packages, over a base graph that holds the rest
	// (#25, ADR 005 amended). An edge into another package of the tree's
	// modules is named by ID, as any is, and resolves against the base's
	// node; a module none of them is in is not loaded, and is still of the
	// graph, so a call into it lands on the declaration. The Report's
	// counts are of the packages loaded. A package the host's
	// configuration builds no file of is left out of the host's load,
	// which naming it would fail, and read under the further
	// configurations; with Configs a list, those of them that bring in a
	// file of the packages asked for (#35).
	Only []string
	// testConfigs, when not "", are the configurations the load with
	// tests takes in place of Configs under ExactTests: those a base's
	// load with tests chose, which Reload sets.
	testConfigs string
}

// Reload is opts for a load of only the packages of a worktree whose
// other packages are base's graph's (#25). With Configs, the loads take
// the configurations base's build chose from the whole module, so a
// package another platform's brings in is read under the one a full
// build reads it under (#35); with ExactTests the load with tests takes
// those base's load with tests chose, which bring in test files the load
// without tests has no configuration for.
func Reload(opts Options, base *Report, only []string) Options {
	opts.Only = only
	if opts.Configs != "" {
		opts.Configs = strings.Join(base.Configs, ",")
		opts.testConfigs = strings.Join(base.testConfigs, ",")
	}
	return opts
}

// DefaultOptions returns the options ADR 004 asks for: tests out, module
// detected from the packages. Generated code is always in: it is the
// bridge between a schema and the code that uses it, and the option that
// left it out cut that bridge for no saving in build time or memory, so
// it was removed (#126).
func DefaultOptions() Options {
	return Options{DepsFromExportData: true}
}
