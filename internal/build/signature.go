// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package build

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/trendvidia/alidade/internal/scope"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/trendvidia/alidade/internal/adapter/goasm"
	"github.com/trendvidia/alidade/internal/adapter/markdown"
	"github.com/trendvidia/alidade/internal/adapter/proto"
	"github.com/trendvidia/alidade/internal/adapter/pxf"
	"github.com/trendvidia/alidade/internal/adapter/py"
	"github.com/trendvidia/alidade/internal/adapter/sql"
	"github.com/trendvidia/alidade/internal/adapter/ts"
)

// A layer's signature is a hash over the sorted paths and contents of
// every file it reads, so a rebuild can tell an untouched layer from a
// changed one by content — a modification time lies after a checkout or
// an rsync — and skip it (#56). The rules below say which files belong to
// which layer; they are a superset of what each adapter reads, so a
// change the adapter would see always changes the signature, and a file
// the adapter ignores (testdata, say) may cost a needless rerun and never
// a missed one.
//
// A layer's files are left out of its signature only under the
// directories that layer itself skips, read from the adapter (#93): one
// skip list for every layer kept the TS layer's "build" and "dist" out of
// the Go layer's signature, and an edit under internal/build/ went
// unseen. The walk itself skips only what every layer skips. The Go tool
// would read a package under node_modules; the signature does not, and
// this is the one known gap.
var walkSkip = map[string]bool{".git": true, "node_modules": true}

// layerSkip names, per layer, the directories its own walk never enters.
// The Go layer has no entry, and its signature therefore covers the whole
// tree: go/packages follows an import wherever it leads, and the skip
// list in [golang.Modules] only keeps a go.mod from being discovered as a
// module. So an edit under testdata/ or _scratch/ reruns the most
// expensive layer for nothing — the direction this file errs in.
var layerSkip = map[string]map[string]bool{
	"proto": proto.SkipDirs, "sql": sql.SkipDirs, "pxf": pxf.SkipDirs,
	"goasm": goasm.SkipDirs, "ts": ts.SkipDirs, "py": py.SkipDirs, "md": markdown.SkipDirs,
}

// A read is one layer's dependence on a file: on its content, read only
// outside the layer's skipped directories (own) or anywhere in the tree
// (anywhere), or only on its existence (presence).
type read struct {
	layer string
	how   readKind
}

type readKind int

const (
	own readKind = iota
	anywhere
	presence
)

// byName and byExt say which layers read a file. Some files are read by
// more than one layer (#93):
//   - goasm reads //go:generate directives from Go files, and the
//     frontend layer matches generated files to .proto paths;
//   - the Markdown layer resolves a link to code by whether the path
//     exists, whether its directory holds Go, and the module path of the
//     nearest go.mod — wherever the link points;
//   - the SQL and goasm layers name Go packages by the module path;
//   - esbuild's resolver reads package.json and jsconfig.json.
var (
	byName = map[string][]read{
		"go.mod":      {{"go", own}, {"md", anywhere}, {"sql", anywhere}, {"goasm", anywhere}},
		"go.sum":      {{"go", own}},
		"go.work":     {{"go", own}},
		"go.work.sum": {{"go", own}},
		"buf.yaml":    {{"proto", own}}, "buf.gen.yaml": {{"proto", own}}, "buf.work.yaml": {{"proto", own}},
		"sqlc.yaml": {{"sql", own}}, "sqlc.yml": {{"sql", own}}, "sqlc.json": {{"sql", own}},
		"svelte.config.js": {{"ts", own}}, ".gitignore": {{"ts", own}},
		"package.json": {{"ts", own}}, "jsconfig.json": {{"ts", own}},
	}
	byExt = map[string][]read{
		".go":    {{"go", own}, {"goasm", own}, {"md", presence}},
		".proto": {{"proto", own}, {"ts", own}},
		".sql":   {{"sql", own}}, ".pxf": {{"pxf", own}}, ".goasm": {{"goasm", own}}, ".md": {{"md", own}},
		// A Python file is the Python layer's, and the Markdown layer's
		// links to it resolve by its existence.
		".py": {{"py", own}, {"md", presence}},
	}
	// dirReads: a directory's existence decides how a Markdown link to it
	// resolves.
	dirReads = []read{{"md", presence}}
	// extensionlessReads: so does the existence of a file with no
	// extension (markdown.pathNode's case ""). A link to "Makefile" in a
	// directory that holds Go is the package it would be while nothing is
	// there, and nothing at all once it is.
	extensionlessReads = []read{{"md", presence}}
)

func init() {
	// The frontend layer's extensions are its own list (#115).
	for e := range ts.Extensions {
		byExt[e] = []read{{"ts", own}}
	}
}

// fileReads says which layers read a file, by name.
func fileReads(base string) []read {
	if rs, ok := byName[base]; ok {
		return rs
	}
	if strings.HasPrefix(base, "buf.gen.") {
		return []read{{"proto", own}}
	}
	if strings.HasPrefix(base, "tsconfig") && strings.HasSuffix(base, ".json") {
		return []read{{"ts", own}}
	}
	if ext := filepath.Ext(base); ext != "" {
		return byExt[ext]
	}
	return extensionlessReads
}

// signatures hashes every file under root that some layer reads and
// returns one signature per layer; a layer with no files has "".
func signatures(root string, skip *scope.Dirs) (map[string]string, error) {
	entries := map[string][]string{} // layer -> "path hash" lines
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			// An entry the walk cannot open is passed over; the layers
			// that would read it record it (#92).
			return nil
		}
		if path == root {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		reads := fileReads(d.Name())
		if d.IsDir() {
			if walkSkip[d.Name()] || skip.SkipsDir(root, path) {
				return filepath.SkipDir
			}
			reads = dirReads
		}
		var hash string
		for _, r := range reads {
			if r.how == own && underSkipped(rel, layerSkip[r.layer]) {
				continue
			}
			line := rel
			if r.how != presence {
				if hash == "" {
					hash = contentHash(path)
				}
				line += " " + hash
			}
			entries[r.layer] = append(entries[r.layer], line)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for l, lines := range entries {
		sort.Strings(lines)
		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		out[l] = hex.EncodeToString(sum[:])
	}
	return out, nil
}

// underSkipped reports whether a repo-relative path lies under a
// directory named in skip.
func underSkipped(rel string, skip map[string]bool) bool {
	dirs := strings.Split(rel, "/")
	for _, d := range dirs[:len(dirs)-1] {
		if skip[d] {
			return true
		}
	}
	return false
}

// contentHash hashes a file. One that cannot be read — a dangling
// symbolic link, an editor's lock file — is signed as such, so its
// becoming readable is a change.
func contentHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "unreadable"
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "unreadable"
	}
	return hex.EncodeToString(h.Sum(nil))
}
