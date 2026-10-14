// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package ts

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// A SvelteKit app's tsconfig.json extends ./.svelte-kit/tsconfig.json,
// which `svelte-kit sync` (or a dev server, a build, an install's prepare
// script) generates and git ignores. In a fresh clone it is missing, and
// the checker loads the app without the aliases SvelteKit generates —
// $lib and the app's kit.alias — so every import through them resolves
// to nothing: on steward, 35% of the TypeScript calls edges were gone,
// and nothing said so (#232, the owner's decision; ADR 007 amended).
//
// So when that file is missing beside a svelte.config.js, the layer
// serves the config SvelteKit would generate, through the overlay the
// checker already reads components through: paths for each alias the
// layer reads from svelte.config.js (the same table esbuild resolves
// with) and $lib, SvelteKit's compiler options, and its include and
// exclude patterns. It leaves out what SvelteKit generates beside it —
// the $app and $env ambient declarations and each route's ./$types — and
// on steward that cost no calls edge: the synthesized config recovered
// 658 of 658. The synthesis is recorded as a skipped input naming the
// file, so a graph built without a build says where it stood in for one.

// errSynthesized is the reason recorded for a synthesized config.
var errSynthesized = errors.New("SvelteKit's generated config is missing (run svelte-kit sync for the real one): " +
	"alidade synthesized it from svelte.config.js — its aliases and $lib, SvelteKit's compiler options and include patterns, " +
	"without the $app, $env and route $types declarations")

// svelteKitConfigs returns, for each SvelteKit app whose tsconfig extends
// a missing .svelte-kit/tsconfig.json, the config to serve in its place.
func (x *extractor) svelteKitConfigs(d *discovery) map[string]string {
	out := map[string]string{}
	for _, a := range d.apps {
		generated := filepath.Join(a.dir, ".svelte-kit", "tsconfig.json")
		if _, err := os.Stat(generated); err == nil || !extendsGenerated(filepath.Join(a.dir, "tsconfig.json")) {
			continue
		}
		body, err := json.MarshalIndent(x.synthesize(a), "", "  ")
		if err != nil {
			continue
		}
		out[filepath.ToSlash(generated)] = string(body)
		x.report.Synthesized++
		x.skip(generated, errSynthesized)
	}
	return out
}

// extendsGenerated reports whether a tsconfig extends SvelteKit's
// generated one.
func extendsGenerated(tsconfig string) bool {
	b, err := os.ReadFile(tsconfig)
	return err == nil && strings.Contains(string(b), ".svelte-kit/tsconfig.json")
}

// synthesize is the config SvelteKit generates for an app, as far as the
// layer can know it: what `svelte-kit sync` writes, the aliases read
// from svelte.config.js, paths relative to .svelte-kit.
func (x *extractor) synthesize(a app) map[string]any {
	kit := filepath.Join(a.dir, ".svelte-kit")
	paths := map[string][]string{}
	names := make([]string, 0, len(a.aliases))
	for name := range a.aliases {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		rel, err := filepath.Rel(kit, filepath.Join(x.root, a.aliases[name]))
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		paths[name] = []string{rel}
		paths[name+"/*"] = []string{rel + "/*"}
	}
	var include []string
	for _, dir := range []string{"src", "test", "tests"} {
		for _, ext := range []string{"js", "ts", "svelte"} {
			include = append(include, "../"+dir+"/**/*."+ext)
		}
	}
	return map[string]any{
		"compilerOptions": map[string]any{
			"paths":                paths,
			"rootDirs":             []string{".."},
			"verbatimModuleSyntax": true,
			"isolatedModules":      true,
			"lib":                  []string{"esnext", "DOM", "DOM.Iterable"},
			"moduleResolution":     "bundler",
			"module":               "esnext",
			"noEmit":               true,
			"target":               "esnext",
		},
		"include": append([]string{"../vite.config.js", "../vite.config.ts"}, include...),
		"exclude": []string{"../node_modules/**", "../src/service-worker.js", "../src/service-worker/**/*.js",
			"../src/service-worker.ts", "../src/service-worker/**/*.ts", "../src/service-worker.d.ts",
			"../src/service-worker/**/*.d.ts"},
	}
}
