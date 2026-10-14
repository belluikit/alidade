// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package proto

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// buf's managed mode sets go_package at generation time, for every file it
// generates: the Go import path is <go_package_prefix>/<directory of the
// file within its module>, whatever go_package the file itself writes,
// unless a disable rule exempts it. The prefix comes from buf.gen.yaml —
// possibly one prefix per path, possibly a separate buf.gen.*.yaml per set
// of inputs. This file reads that configuration; it does not run buf.
//
// A written go_package does not survive managed mode. protovalidate
// writes go_package = "buf.build/gen/go/…" in validate.proto, for the
// registry's generated SDK, and generates its own copy with managed mode
// under tools/internal/gen; the oracle found every one of its 34 types
// linked to the registry's package, which the repository does not hold
// (#148).

// A genConfig is one buf.gen*.yaml: which files it selects and the
// go_package_prefix overrides that apply to them.
type genConfig struct {
	// dirs, paths and excludes are repo-relative, forward-slashed
	// prefixes from the config's inputs; a file is selected when it is
	// under one of dirs (or dirs is empty), under one of paths (or paths
	// is empty), and under none of excludes.
	dirs, paths, excludes []string
	overrides             []prefixOverride
	// disabled are the module-relative path prefixes a disable rule
	// keeps out of managed go_package, "" for every file.
	disabled []string
}

// A prefixOverride sets the prefix for files whose import path (relative
// to their module) starts with path, or for every file when path is "".
type prefixOverride struct {
	path, prefix string
}

// loadGenConfigs reads every buf.gen*.yaml at root, in name order.
func loadGenConfigs(root string) []genConfig {
	matches, _ := filepath.Glob(filepath.Join(root, "buf.gen*.yaml"))
	slices.Sort(matches)
	var out []genConfig
	for _, m := range matches {
		if cfg, ok := parseGenConfig(m); ok {
			out = append(out, cfg)
		}
	}
	return out
}

// rawGenConfig is the part of a buf.gen*.yaml the layer reads.
type rawGenConfig struct {
	Managed struct {
		Enabled  bool `yaml:"enabled"`
		Override []struct {
			FileOption string `yaml:"file_option"`
			Path       string `yaml:"path"`
			Value      string `yaml:"value"`
		} `yaml:"override"`
		Disable []struct {
			FileOption string `yaml:"file_option"`
			Module     string `yaml:"module"`
			Path       string `yaml:"path"`
		} `yaml:"disable"`
	} `yaml:"managed"`
	Inputs []struct {
		Directory    string   `yaml:"directory"`
		Paths        []string `yaml:"paths"`
		ExcludePaths []string `yaml:"exclude_paths"`
	} `yaml:"inputs"`
}

func parseGenConfig(file string) (genConfig, bool) {
	b, err := os.ReadFile(file)
	if err != nil {
		return genConfig{}, false
	}
	var raw rawGenConfig
	if yaml.Unmarshal(b, &raw) != nil || !raw.Managed.Enabled {
		return genConfig{}, false
	}
	var cfg genConfig
	for _, o := range raw.Managed.Override {
		if o.FileOption == "go_package_prefix" && o.Value != "" {
			cfg.overrides = append(cfg.overrides, prefixOverride{path: path.Clean("/" + o.Path)[1:], prefix: o.Value})
		}
	}
	if len(cfg.overrides) == 0 {
		return genConfig{}, false
	}
	cfg.disabled = disabledPaths(&raw)
	for _, in := range raw.Inputs {
		if in.Directory != "" && in.Directory != "." {
			cfg.dirs = append(cfg.dirs, path.Clean(in.Directory))
		}
		cfg.paths = append(cfg.paths, in.Paths...)
		cfg.excludes = append(cfg.excludes, in.ExcludePaths...)
	}
	return cfg, true
}

// disabledPaths are the paths a disable rule keeps out of managed
// go_package. A rule naming a module names a dependency's, by its
// registry name, whose files are not in the tree.
func disabledPaths(raw *rawGenConfig) []string {
	var out []string
	for _, d := range raw.Managed.Disable {
		if d.Module == "" && (d.FileOption == "" || d.FileOption == "go_package" || d.FileOption == "go_package_prefix") {
			out = append(out, path.Clean("/" + d.Path)[1:])
		}
	}
	return out
}

// under reports whether a repo-relative path is p or lies beneath it.
func under(p, prefix string) bool {
	return prefix == "" || p == prefix || strings.HasPrefix(p, prefix+"/")
}

func anyUnder(p string, prefixes []string) bool {
	for _, pre := range prefixes {
		if under(p, pre) {
			return true
		}
	}
	return false
}

// selects reports whether a configuration generates a file with managed
// go_package: under its inputs, outside its excludes and disable rules.
func (c genConfig) selects(rel, imp string) bool {
	return (len(c.dirs) == 0 || anyUnder(rel, c.dirs)) && (len(c.paths) == 0 || anyUnder(rel, c.paths)) &&
		!anyUnder(rel, c.excludes) && !anyUnder(imp, c.disabled)
}

// goImportPathFor returns the Go import path managed mode gives a file,
// or "" if no configuration covers it or a disable rule exempts it. rel is
// the file's repo-relative path and imp its import path within its
// module. Later configurations and later overrides win, which is the
// order buf applies them in.
func goImportPathFor(cfgs []genConfig, rel, imp string) string {
	if strings.HasPrefix(imp, "google/protobuf/") {
		// buf leaves the well-known types' own go_package alone; a
		// repository holding copies of them (protocompile's
		// wellknownimports) would otherwise put every one in one Go
		// package, which protogen refuses (#148).
		return ""
	}
	prefix := ""
	for _, c := range cfgs {
		if !c.selects(rel, imp) {
			continue
		}
		for _, o := range c.overrides {
			if under(imp, o.path) {
				prefix = o.prefix
			}
		}
	}
	if prefix == "" {
		return ""
	}
	if dir := path.Dir(imp); dir != "." {
		return prefix + "/" + dir
	}
	return prefix
}
