// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build ignore

// gen bundles svelte2tsx and its dependencies into one script for the V8
// converter, copies the type shims its output needs (#221), and writes
// LICENSES.txt: the license of every package the bundle holds code from,
// which ships with it (#282). Run by scripts/svelte2tsx.sh, which installs
// the pinned packages first:
//
//	go run gen/main.go <dir with node_modules> <out dir>
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// shims are the declarations svelte2tsx's output refers to, from its
// package: its helpers (__sveltets_2_*) and the elements of markup.
var shims = []string{"svelte-shims-v4.d.ts", "svelte-jsx-v4.d.ts"}

func main() {
	dir, out := os.Args[1], os.Args[2]
	src, err := filepath.Abs("gen")
	check(err)
	r := api.Build(api.BuildOptions{
		EntryPoints:   []string{filepath.Join(src, "entry.js")},
		AbsWorkingDir: dir,
		NodePaths:     []string{filepath.Join(dir, "node_modules")},
		Bundle:        true,
		Write:         false,
		Outfile:       "svelte2tsx.js",
		Format:        api.FormatIIFE,
		Platform:      api.PlatformBrowser,
		Target:        api.ES2020,
		MinifySyntax:  true,
		LogLevel:      api.LogLevelWarning,
		Metafile:      true,
		Define:        map[string]string{"process.env.NODE_ENV": `"production"`},
		Plugins:       []api.Plugin{builtins(src)},
	})
	if len(r.Errors) > 0 {
		for _, e := range r.Errors {
			fmt.Fprintln(os.Stderr, "gen:", e.Text)
		}
		os.Exit(1)
	}
	var gz bytes.Buffer
	w, err := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	check(err)
	_, err = w.Write(r.OutputFiles[0].Contents)
	check(err)
	check(w.Close())
	check(os.WriteFile(filepath.Join(out, "svelte2tsx.js.gz"), gz.Bytes(), 0o644))
	for _, s := range shims {
		b, err := os.ReadFile(filepath.Join(dir, "node_modules", "svelte2tsx", s))
		check(err)
		check(os.WriteFile(filepath.Join(out, s), b, 0o644))
	}
	check(os.WriteFile(filepath.Join(out, "LICENSES.txt"), licenses(dir, r.Metafile), 0o644))
}

// licenses is the license of each package the bundle took code from, by
// esbuild's metafile: its name, version and license field, and the text
// of its license file.
func licenses(dir, metafile string) []byte {
	var meta struct {
		Outputs map[string]struct {
			Inputs map[string]struct {
				BytesInOutput int `json:"bytesInOutput"`
			} `json:"inputs"`
		} `json:"outputs"`
	}
	check(json.Unmarshal([]byte(metafile), &meta))
	pkgs := map[string]bool{}
	for _, o := range meta.Outputs {
		for in, x := range o.Inputs {
			if p := packageDir(in); p != "" && x.BytesInOutput > 0 {
				pkgs[p] = true
			}
		}
	}
	var b strings.Builder
	b.WriteString("The Svelte converter's bundle (svelte2tsx.js.gz) holds code from these\npackages, under these licenses.\n")
	for _, p := range slices.Sorted(func(yield func(string) bool) {
		for k := range pkgs {
			if !yield(k) {
				return
			}
		}
	}) {
		var info struct {
			Name, Version, License string
			Author                 json.RawMessage
		}
		raw, err := os.ReadFile(filepath.Join(dir, p, "package.json"))
		check(err)
		check(json.Unmarshal(raw, &info))
		fmt.Fprintf(&b, "\n%s\n%s %s (%s)\n\n", strings.Repeat("-", 72), info.Name, info.Version, info.License)
		if text := licenseText(filepath.Join(dir, p)); text != "" {
			b.WriteString(text)
		} else {
			fmt.Fprintf(&b, "The package publishes no license file. Its package.json declares the\nlicense %s, author %s.\n", info.License, info.Author)
		}
	}
	return []byte(b.String())
}

// packageDir is the node_modules directory of the package an input file
// belongs to, a scoped one included; "" for one outside node_modules.
func packageDir(in string) string {
	parts := strings.Split(filepath.ToSlash(in), "/")
	i := -1
	for j, part := range parts {
		if part == "node_modules" {
			i = j
		}
	}
	if i < 0 || i+1 >= len(parts) {
		return ""
	}
	n := 2
	if strings.HasPrefix(parts[i+1], "@") {
		n = 3
	}
	if i+n > len(parts) {
		return ""
	}
	return filepath.FromSlash(strings.Join(parts[:i+n], "/"))
}

// licenseText is a package's license file and any notice file it
// publishes beside it, "" when it has neither.
func licenseText(pkg string) string {
	entries, err := os.ReadDir(pkg)
	check(err)
	var texts []string
	for _, e := range entries {
		name := strings.ToUpper(e.Name())
		for _, prefix := range []string{"LICENSE", "LICENCE", "COPYING", "NOTICE", "THIRDPARTYNOTICE"} {
			if strings.HasPrefix(name, prefix) {
				b, err := os.ReadFile(filepath.Join(pkg, e.Name()))
				check(err)
				texts = append(texts, strings.TrimSpace(string(b))+"\n")
				break
			}
		}
	}
	return strings.Join(texts, "\n")
}

// builtins stands in for Node's own modules: a POSIX path, and nothing
// for the rest, which TypeScript's own code reaches only under Node — and
// the converter runs it with no process or require, so it never does.
func builtins(src string) api.Plugin {
	return api.Plugin{Name: "node-builtins", Setup: func(pb api.PluginBuild) {
		pb.OnResolve(api.OnResolveOptions{Filter: `^(node:)?(path|fs|os|crypto|inspector|perf_hooks|module|url|util|buffer|child_process|readline|stream|events|tty|v8|worker_threads|assert|process|zlib)$`},
			func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				if a.Path == "path" || a.Path == "node:path" {
					return api.OnResolveResult{Path: filepath.Join(src, "path.js")}, nil
				}
				return api.OnResolveResult{Path: "empty", Namespace: "empty"}, nil
			})
		pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "empty"}, func(api.OnLoadArgs) (api.OnLoadResult, error) {
			empty := "module.exports = {};"
			return api.OnLoadResult{Contents: &empty, Loader: api.LoaderJS}, nil
		})
	}}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}
