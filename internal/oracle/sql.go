// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/trendvidia/alidade/internal/graph"
)

// The SQL oracle is sqlc itself (#148). Each sqlc configuration of a
// repository is copied, with its schema and query files, into a scratch
// directory — sqlc writes where its configuration says, and the
// repository is never touched — and run with sqlc's json generator, which
// writes sqlc's own catalog (the schema after every migration, as sqlc
// applies them) and every query with the tables its columns and
// parameters come from.

// SQL is the oracle's view of a repository's SQL.
type SQL struct {
	// Tables maps each table's graph ID to its columns' graph IDs.
	Tables map[graph.ID][]graph.ID
	// Queries are sqlc's queries, each with the tables it touches.
	Queries []Query
	// SchemaFiles are the files the configurations read as schema,
	// relative to the repository: a table declared anywhere else is
	// outside sqlc's view, not missing from its catalog.
	SchemaFiles map[string]bool
	// Configs counts the configurations sqlc generated for, and Failed
	// names the ones it refused, each with its error.
	Configs int
	Failed  map[string]string
}

// A Query is one sqlc query: its name, the name of the file it is in —
// sqlc reports the base name only — the directory of its configuration
// relative to the repository, and the tables sqlc says its result
// columns, parameters or INSERT target come from.
type Query struct {
	Name, File, Dir string
	Tables          []graph.ID
}

// SQLC runs sqlc's json generator over every sqlc configuration under
// root. A repository with none gives an empty result; a sqlc that cannot
// run here (SQLCUsable) is an error.
func SQLC(m *graph.Minter, root string) (*SQL, error) {
	if err := SQLCUsable(); err != nil {
		return nil, fmt.Errorf("oracle: %w", err)
	}
	out := &SQL{Tables: map[graph.ID][]graph.ID{}, SchemaFiles: map[string]bool{}, Failed: map[string]string{}}
	for _, cfg := range sqlcConfigs(root) {
		rel, _ := filepath.Rel(root, cfg)
		if err := out.run(m, root, cfg); err != nil {
			out.Failed[rel] = err.Error()
			continue
		}
		out.Configs++
	}
	return out, nil
}

// SQLCUsable says why sqlc cannot run the oracle here, or nil. The scratch
// directory is under os.TempDir, and a sqlc installed as a snap reads only
// what is under $HOME and not hidden: from /tmp it reports every
// configuration as a missing file (#246).
func SQLCUsable() error {
	path, err := exec.LookPath("sqlc")
	if err != nil {
		return fmt.Errorf("sqlc is not on PATH: %w", err)
	}
	if real, err := filepath.EvalSymlinks(path); err != nil || filepath.Base(real) != "snap" {
		return nil
	}
	tmp, home := os.TempDir(), os.Getenv("HOME")
	rel, err := filepath.Rel(home, tmp)
	if home == "" || err != nil || strings.HasPrefix(rel, ".") || strings.Contains(rel, "/.") {
		return fmt.Errorf("sqlc is a snap (%s), which cannot read %s: set TMPDIR to a directory under your home that is not hidden", path, tmp)
	}
	return nil
}

// sqlcConfigs finds every sqlc configuration under root, passing over the
// directories no repository's own SQL lives in.
func sqlcConfigs(root string) []string {
	var out []string
	skip := skipOf(root)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != root && skip.SkipsDir(root, p) {
			return filepath.SkipDir // the build leaves it out too
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		switch d.Name() {
		case "sqlc.yaml", "sqlc.yml", "sqlc.json":
			out = append(out, p)
		}
		return nil
	})
	return out
}

// run copies one configuration into a scratch directory, points each of
// its sql entries' generator at json, runs sqlc and reads the result.
func (s *SQL) run(m *graph.Minter, root, cfg string) error {
	raw, err := os.ReadFile(cfg)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	entries, _ := doc["sql"].([]any)
	if len(entries) == 0 {
		return fmt.Errorf("no sql entries (only version 2 configurations are read)")
	}
	scratch, err := os.MkdirTemp("", "alidade-oracle-sqlc-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	dir := filepath.Dir(cfg)
	for i, e := range entries {
		entry, ok := e.(map[string]any)
		if !ok {
			return fmt.Errorf("sql entry %d is not a mapping", i)
		}
		if err := s.copyEntry(root, dir, scratch, entry); err != nil {
			return err
		}
		entry["gen"] = map[string]any{"json": map[string]any{"out": fmt.Sprintf("oracle-json-%d", i), "filename": "codegen.json"}}
		delete(entry, "codegen")
	}
	if err := generate(scratch, doc); err != nil {
		return err
	}
	for i := range entries {
		rel, _ := filepath.Rel(root, dir)
		if err := s.read(m, filepath.Join(scratch, fmt.Sprintf("oracle-json-%d", i), "codegen.json"), rel); err != nil {
			return err
		}
	}
	return nil
}

// copyEntry copies one sql entry's schema and query files into scratch,
// at the same paths relative to the configuration, and records the
// schema files.
func (s *SQL) copyEntry(root, dir, scratch string, entry map[string]any) error {
	for _, key := range []string{"schema", "queries"} {
		for _, p := range paths(entry[key]) {
			files, err := copyPath(filepath.Join(dir, p), filepath.Join(scratch, p))
			if err != nil {
				return err
			}
			for _, f := range files {
				if rel, err := filepath.Rel(root, f); err == nil && key == "schema" {
					s.SchemaFiles[filepath.ToSlash(rel)] = true
				}
			}
		}
	}
	return nil
}

// generate writes the rewritten configuration into scratch and runs sqlc.
func generate(scratch string, doc map[string]any) error {
	body, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(scratch, "sqlc.yaml"), body, 0o644); err != nil {
		return err
	}
	cmd := exec.Command("sqlc", "generate")
	cmd.Dir = scratch
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sqlc generate: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// paths reads a schema or queries value: one path or a list of them.
func paths(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, p := range v {
			if s, ok := p.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// copyPath copies a file, or a directory's files without descending, as
// sqlc reads a directory, and returns the source files it copied.
func copyPath(src, dst string) ([]string, error) {
	st, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []string{src}, copyFile(src, dst)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return nil, err
			}
			out = append(out, filepath.Join(src, e.Name()))
		}
	}
	return out, nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// codegen is the part of sqlc's json output the oracle reads.
type codegen struct {
	Catalog struct {
		Schemas []struct {
			Name   string `json:"name"`
			Tables []struct {
				Rel     ident `json:"rel"`
				Columns []struct {
					Name string `json:"name"`
				} `json:"columns"`
			} `json:"tables"`
		} `json:"schemas"`
	} `json:"catalog"`
	Queries []struct {
		Name     string `json:"name"`
		Filename string `json:"filename"`
		Columns  []struct {
			Table *ident `json:"table"`
		} `json:"columns"`
		Params []struct {
			Column struct {
				Table *ident `json:"table"`
			} `json:"column"`
		} `json:"params"`
		InsertIntoTable *ident `json:"insert_into_table"`
	} `json:"queries"`
}

type ident struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

// systemSchemas are the PostgreSQL schemas sqlc's catalog carries that no
// migration declares.
var systemSchemas = map[string]bool{"pg_catalog": true, "information_schema": true, "pg_temp": true}

// read adds one generated catalog and its queries. dir is the
// configuration's directory relative to the repository, where its query
// files' names are relative to.
func (s *SQL) read(m *graph.Minter, file, dir string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var cg codegen
	if err := json.Unmarshal(b, &cg); err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	s.readCatalog(m, &cg)
	s.readQueries(m, &cg, dir)
	return nil
}

// readCatalog adds the catalog's tables, each with its columns, leaving
// out PostgreSQL's own schemas.
func (s *SQL) readCatalog(m *graph.Minter, cg *codegen) {
	for _, schema := range cg.Catalog.Schemas {
		if systemSchemas[schema.Name] {
			continue
		}
		for _, t := range schema.Tables {
			name := tableName(schema.Name, t.Rel.Name)
			id := sqlID(m, graph.SQLTable, name)
			if _, seen := s.Tables[id]; seen || id == "" {
				continue
			}
			cols := []graph.ID{}
			for _, c := range t.Columns {
				if cid := sqlID(m, graph.SQLColumn, name+"."+c.Name); cid != "" {
					cols = append(cols, cid)
				}
			}
			s.Tables[id] = cols
		}
	}
}

// readQueries adds the queries, each with the tables of its result
// columns, its parameters and its INSERT target.
func (s *SQL) readQueries(m *graph.Minter, cg *codegen, dir string) {
	for _, q := range cg.Queries {
		tables := map[graph.ID]bool{}
		add := func(t *ident) {
			if t != nil && t.Name != "" {
				tables[sqlID(m, graph.SQLTable, tableName(t.Schema, t.Name))] = true
			}
		}
		for _, c := range q.Columns {
			add(c.Table)
		}
		for _, p := range q.Params {
			add(p.Column.Table)
		}
		add(q.InsertIntoTable)
		delete(tables, "")
		query := Query{Name: q.Name, File: q.Filename, Dir: filepath.ToSlash(dir)}
		for t := range tables {
			query.Tables = append(query.Tables, t)
		}
		s.Queries = append(s.Queries, query)
	}
}

// tableName writes a table as the graph keys it: public's tables bare,
// as an unqualified CREATE TABLE names them, the rest qualified.
func tableName(schema, name string) string {
	if schema == "" || schema == "public" {
		return name
	}
	return schema + "." + name
}

func sqlID(m *graph.Minter, kind graph.SQLKind, name string) graph.ID {
	id, err := m.SQLID(kind, name)
	if err != nil {
		return ""
	}
	return id
}
