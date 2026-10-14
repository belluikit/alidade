// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package ts

import (
	"bufio"
	"github.com/trendvidia/alidade/internal/scope"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// SkipDirs are the directories the layer never descends into, by name:
// dependencies, build output, and the directories every layer skips.
// The build reads them too: a file under one is not in the layer's
// signature, and nothing else is left out of it (#93).
var SkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".tmp": true,
	".svelte-kit": true, "dist": true, "build": true, ".astro": true,
	"out": true, // electron-vite's and others' build output (#115)
}

// Extensions are the files the layer reads (ADR 007 decision 1, amended
// by #115 to the ECMAScript module forms .mjs/.cjs and their TypeScript
// counterparts .mts/.cts). The Markdown layer's link targets and the
// build's signature read this list, so the three cannot disagree.
var Extensions = map[string]bool{
	".ts": true, ".tsx": true, ".mts": true, ".cts": true,
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true, ".svelte": true,
}

// Declaration reports whether a file is a type declaration file (.d.ts,
// .d.mts, .d.cts), which declares another module's types and is not read.
func Declaration(name string) bool {
	for _, s := range []string{".d.ts", ".d.mts", ".d.cts"} {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// Source reports whether the layer reads a file, by name.
func Source(name string) bool {
	return Extensions[filepath.Ext(name)] && !Declaration(name)
}

type unread struct {
	path string
	err  error
}

// A discovery is what one walk of the tree found.
type discovery struct {
	// files are the frontend files, absolute and sorted; protos every
	// .proto file, for matching a generated file's header to its source.
	files, protos []string
	// skip are the directories left out besides SkipDirs.
	skip *scope.Dirs
	// configs are the tsconfig.json and jsconfig.json files, each a
	// project the type checker reads (#215).
	configs []string
	// apps are the SvelteKit applications, one per svelte.config.js, each
	// with its own aliases: "$lib" in one app is not the other's (#99).
	apps []app
	// testdata counts frontend files skipped under testdata directories.
	testdata int
	// unread are the entries the walk could not open, passed over.
	unread []unread
}

// discover walks root for frontend files, .proto files and SvelteKit
// configurations, skipping build output and what the root .gitignore
// names.
func discover(root string, skipTestdata bool, skip *scope.Dirs) (*discovery, error) {
	d := &discovery{skip: skip}
	ignored := gitignoredDirs(root)
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			d.unread = append(d.unread, unread{path, err})
			return nil
		}
		if e.IsDir() {
			return d.enter(root, path, e.Name(), ignored, skipTestdata)
		}
		d.add(root, path, e.Name())
		return nil
	})
	slices.Sort(d.files)
	slices.Sort(d.protos)
	slices.Sort(d.configs)
	return d, err
}

// enter decides whether the walk descends into a directory.
func (d *discovery) enter(root, path, name string, ignored ignoreSet, skipTestdata bool) error {
	if path == root {
		return nil
	}
	rel, _ := filepath.Rel(root, path)
	switch {
	case SkipDirs[name], ignored.matches(name, filepath.ToSlash(rel)), d.skip.SkipsDir(root, path):
		return filepath.SkipDir
	case skipTestdata && name == "testdata":
		d.testdata += countFrontend(path)
		return filepath.SkipDir
	}
	return nil
}

// add records one file the walk found.
func (d *discovery) add(root, path, name string) {
	switch {
	case name == "svelte.config.js":
		d.svelteConfig(root, path)
	case name == "tsconfig.json" || name == "jsconfig.json":
		d.configs = append(d.configs, path)
	case strings.HasSuffix(name, ".proto"):
		d.protos = append(d.protos, path)
	case Declaration(name):
	case Extensions[filepath.Ext(name)]:
		d.files = append(d.files, path)
	}
}

func countFrontend(dir string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && Source(e.Name()) {
			n++
		}
		return nil
	})
	return n
}

// ignoreSet is the directory-shaped part of a .gitignore: plain names,
// matched anywhere, and root-anchored names. Globs are not honoured —
// this is for "site/" and "build/", not for a full ignore engine.
type ignoreSet struct {
	anywhere map[string]bool
	rooted   map[string]bool
}

func (s ignoreSet) matches(name, rel string) bool {
	return s.anywhere[name] || s.rooted[rel]
}

// gitignoredDirs reads the root .gitignore for directory patterns.
func gitignoredDirs(root string) ignoreSet {
	s := ignoreSet{anywhere: map[string]bool{}, rooted: map[string]bool{}}
	f, err := os.Open(filepath.Join(root, ".gitignore"))
	if err != nil {
		return s
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") || strings.ContainsAny(line, "*?[") {
			continue
		}
		rooted := strings.HasPrefix(line, "/")
		line = strings.Trim(line, "/")
		if line == "" {
			continue
		}
		if rooted || strings.Contains(line, "/") {
			s.rooted[line] = true
		} else {
			s.anywhere[line] = true
		}
	}
	return s
}

var (
	aliasBlockRE = regexp.MustCompile(`(?s)\balias\s*:\s*\{(.*?)\}`)
	aliasEntryRE = regexp.MustCompile(`(?:^|[\s,{])(?:"([^"]+)"|'([^']+)'|([\$\w.-]+))\s*:\s*(?:"([^"]+)"|'([^']+)')`)
)

// svelteConfig reads kit.alias from a svelte.config.js — a JavaScript
// file, read for the one object literal SvelteKit documents there —
// and adds SvelteKit's own $lib default. Values are made relative to
// root, which is esbuild's working directory. A "name/*" key is the
// same alias as "name" to esbuild and is dropped.
func (d *discovery) svelteConfig(root, path string) {
	src, err := os.ReadFile(path)
	if err != nil {
		return
	}
	a := app{dir: filepath.Dir(path), aliases: map[string]string{}}
	add := func(name, target string) {
		if strings.HasSuffix(name, "/*") || a.aliases[name] != "" {
			return
		}
		rel, err := filepath.Rel(root, filepath.Join(a.dir, target))
		if err != nil {
			return
		}
		a.aliases[name] = "./" + filepath.ToSlash(rel)
	}
	if m := aliasBlockRE.FindSubmatch(src); m != nil {
		for _, e := range aliasEntryRE.FindAllSubmatch(m[1], -1) {
			name := string(e[1]) + string(e[2]) + string(e[3])
			target := string(e[4]) + string(e[5])
			add(name, strings.TrimSuffix(target, "/*"))
		}
	}
	add("$lib", "src/lib")
	d.apps = append(d.apps, a)
}

// An app is one SvelteKit application: its directory and its aliases.
type app struct {
	dir     string
	aliases map[string]string
}

// appOf returns the app a file belongs to — the one whose directory is
// the deepest above it — or nil for a file of no app.
func (d *discovery) appOf(file string) *app {
	var best *app
	for i := range d.apps {
		a := &d.apps[i]
		if strings.HasPrefix(file, a.dir+string(filepath.Separator)) && (best == nil || len(a.dir) > len(best.dir)) {
			best = a
		}
	}
	return best
}
