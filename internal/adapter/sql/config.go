// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sql

import (
	dirs "github.com/trendvidia/alidade/internal/scope"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
	"gopkg.in/yaml.v3"
)

// SkipDirs are the directories the layer never descends into, by name.
// The build reads them too: a file under one is not in the layer's
// signature, and nothing else is left out of it (#93).
var SkipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".tmp": true}

// A corpus is what discovery found.
type corpus struct {
	// skip are the directories left out besides SkipDirs.
	skip     *dirs.Dirs
	files    []string // every .sql file, absolute, sorted
	configs  []sqlcConfig
	module   string // the Go module at root, or ""
	testdata int
	unread   []unread // entries the walk could not open, passed over
}

type unread struct {
	path string
	err  error
}

// A sqlcConfig is one package of one sqlc.yaml: which files are schema,
// which are queries, and what Go they generate.
type sqlcConfig struct {
	dir     string   // the config file's directory, absolute
	schema  []string // absolute files or directories
	queries []string
	// goPkg is the import path of the generated package, or "" when
	// the config generates no Go or the root has no go.mod.
	goPkg string
	naming
}

// naming is what sqlc's struct naming depends on.
type naming struct {
	exactTableNames bool
	rename          map[string]string
	initialisms     map[string]bool
	exclusions      []string
}

// discover walks root for .sql files and sqlc configuration.
func discover(root string, skipTestdata bool, skip *dirs.Dirs) (*corpus, error) {
	c := &corpus{module: modulePath(root), skip: skip}
	var cfgFiles []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			c.unread = append(c.unread, unread{path, err})
			return nil
		}
		if d.IsDir() {
			return c.enter(root, path, d.Name(), skipTestdata)
		}
		switch d.Name() {
		case "sqlc.yaml", "sqlc.yml", "sqlc.json":
			cfgFiles = append(cfgFiles, path)
		default:
			if strings.HasSuffix(d.Name(), ".sql") {
				c.files = append(c.files, path)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(c.files)
	slices.Sort(cfgFiles)
	for _, f := range cfgFiles {
		c.configs = append(c.configs, parseConfig(f, root, c.module)...)
	}
	return c, nil
}

// enter decides whether the walk descends into a directory.
func (c *corpus) enter(root, path, name string, skipTestdata bool) error {
	switch {
	case path == root:
		return nil
	case SkipDirs[name], c.skip.SkipsDir(root, path):
		return filepath.SkipDir
	case skipTestdata && name == "testdata":
		c.testdata += countSQL(path)
		return filepath.SkipDir
	}
	return nil
}

func countSQL(dir string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".sql") {
			n++
		}
		return nil
	})
	return n
}

// modulePath returns the module path of the go.mod at root, or "".
func modulePath(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	return modfile.ModulePath(b)
}

// pathList accepts a string or a list of strings, as sqlc does.
type pathList []string

func (p *pathList) UnmarshalYAML(n *yaml.Node) error {
	var one string
	if n.Decode(&one) == nil {
		*p = pathList{one}
		return nil
	}
	var many []string
	if err := n.Decode(&many); err != nil {
		return err
	}
	*p = many
	return nil
}

// rawConfig is the shape of sqlc.yaml, version 2 (sql[]) or 1
// (packages[]). sqlc.json is JSON, which yaml decodes.
type rawConfig struct {
	SQL []struct {
		Queries pathList `yaml:"queries"`
		Schema  pathList `yaml:"schema"`
		Gen     struct {
			Go *rawGoGen `yaml:"go"`
		} `yaml:"gen"`
	} `yaml:"sql"`
	// A version 1 package carries its Go options inline, beside its
	// paths, where version 2 nests them under gen.go (#96).
	Packages []struct {
		Path                        string   `yaml:"path"`
		Queries                     pathList `yaml:"queries"`
		Schema                      pathList `yaml:"schema"`
		EmitExactTableNames         bool     `yaml:"emit_exact_table_names"`
		InflectionExcludeTableNames []string `yaml:"inflection_exclude_table_names"`
	} `yaml:"packages"`
	Rename map[string]string `yaml:"rename"`
}

type rawGoGen struct {
	Out                         string            `yaml:"out"`
	EmitExactTableNames         bool              `yaml:"emit_exact_table_names"`
	Rename                      map[string]string `yaml:"rename"`
	Initialisms                 *[]string         `yaml:"initialisms"`
	InflectionExcludeTableNames []string          `yaml:"inflection_exclude_table_names"`
}

// parseConfig reads a sqlc configuration and returns one sqlcConfig per
// package it declares.
func parseConfig(file, root, module string) []sqlcConfig {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var raw rawConfig
	if yaml.Unmarshal(b, &raw) != nil {
		return nil
	}
	dir := filepath.Dir(file)
	abs := func(ps pathList) []string {
		out := make([]string, 0, len(ps))
		for _, p := range ps {
			out = append(out, filepath.Join(dir, filepath.FromSlash(p)))
		}
		return out
	}
	var cfgs []sqlcConfig
	for _, s := range raw.SQL {
		cfg := sqlcConfig{dir: dir, schema: abs(s.Schema), queries: abs(s.Queries), naming: defaultNaming()}
		if g := s.Gen.Go; g != nil {
			cfg.goPkg = goPackage(root, module, filepath.Join(dir, filepath.FromSlash(g.Out)))
			cfg.apply(raw.Rename, g)
		}
		cfgs = append(cfgs, cfg)
	}
	for _, p := range raw.Packages {
		cfg := sqlcConfig{dir: dir, schema: abs(p.Schema), queries: abs(p.Queries), naming: defaultNaming()}
		if p.Path != "" {
			cfg.goPkg = goPackage(root, module, filepath.Join(dir, filepath.FromSlash(p.Path)))
		}
		cfg.apply(raw.Rename, &rawGoGen{EmitExactTableNames: p.EmitExactTableNames, InflectionExcludeTableNames: p.InflectionExcludeTableNames})
		cfgs = append(cfgs, cfg)
	}
	return cfgs
}

// apply folds the global renames and a package's Go options into the
// naming, package options last so they win.
func (n *naming) apply(global map[string]string, g *rawGoGen) {
	for k, v := range global {
		n.rename[k] = v
	}
	if g == nil {
		return
	}
	n.exactTableNames = g.EmitExactTableNames
	n.exclusions = g.InflectionExcludeTableNames
	for k, v := range g.Rename {
		n.rename[k] = v
	}
	if g.Initialisms != nil {
		n.initialisms = map[string]bool{}
		for _, i := range *g.Initialisms {
			n.initialisms[i] = true
		}
	}
}

func defaultNaming() naming {
	return naming{rename: map[string]string{}, initialisms: map[string]bool{"id": true}}
}

// goPackage returns the import path of a generated directory: the module
// path plus the directory's path within the module.
func goPackage(root, module, out string) string {
	if module == "" {
		return ""
	}
	rel, err := filepath.Rel(root, out)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	if rel == "." {
		return module
	}
	return module + "/" + filepath.ToSlash(rel)
}

// under reports whether a file is one of, or lies beneath one of, paths.
func under(file string, paths []string) bool {
	for _, p := range paths {
		if file == p || strings.HasPrefix(file, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// listed reports whether sqlc reads a file for a configured path, as its
// sqlpath.Glob does: the path itself, a file directly in the directory it
// names — sqlc lists a directory without descending, so a rollback/
// directory inside migrations/ is not schema (#119) — or a match of the
// glob it is.
func listed(file string, paths []string) bool {
	for _, p := range paths {
		if file == p || filepath.Dir(file) == p {
			return true
		}
		if strings.ContainsAny(p, "*?[]") {
			if ok, _ := filepath.Match(p, file); ok {
				return true
			}
		}
	}
	return false
}

// schemaOrder lists the .sql files in the order sqlc applies schema —
// each configuration's schema paths in the order it lists them, the
// files of each in path order, which is os.ReadDir's — then the files no
// configuration's schema reaches at all, in path order (#119). A file
// inside a configured schema directory that sqlc does not read is in
// neither list, and is counted in unread.
func (c *corpus) schemaOrder() (order []string, unread int) {
	seen := map[string]bool{}
	for _, cfg := range c.configs {
		for _, p := range cfg.schema {
			for _, f := range c.files {
				if !seen[f] && listed(f, []string{p}) {
					seen[f] = true
					order = append(order, f)
				}
			}
		}
	}
	for _, f := range c.files {
		switch {
		case seen[f]:
		case c.inSchemaTree(f):
			unread++
		default:
			order = append(order, f)
		}
	}
	return order, unread
}

// inSchemaTree reports whether a file lies under some configuration's
// schema directory, read by sqlc or not.
func (c *corpus) inSchemaTree(file string) bool {
	for _, cfg := range c.configs {
		if under(file, cfg.schema) {
			return true
		}
	}
	return false
}

// configFor returns every configuration that treats the file as a query
// file, and every one that treats it as schema. sqlc compiles each package
// on its own, so a file two packages list is in both (#118).
func (c *corpus) configFor(file string) (queries, schema []*sqlcConfig) {
	for i := range c.configs {
		cfg := &c.configs[i]
		if listed(file, cfg.queries) {
			queries = append(queries, cfg)
		}
		if listed(file, cfg.schema) {
			schema = append(schema, cfg)
		}
	}
	return queries, schema
}
