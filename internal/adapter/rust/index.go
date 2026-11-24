// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// A doc is one file of an index: its document and its text.
type doc struct {
	rel  string
	abs  string
	d    *scip.Document
	text []byte
}

// index has rust-analyzer index each Cargo workspace the manifests belong
// to, once each, and records the documents of the files the layer reads.
// A workspace that cannot be indexed is skipped, and says why; its files
// keep their nodes, marked as not read.
func (x *extractor) index(manifests []string) {
	for _, ws := range x.workspaces(manifests) {
		idx, err := runSCIP(ws)
		if err != nil {
			x.skip(ws, 0, err)
			continue
		}
		rel, _ := filepath.Rel(x.root, ws)
		x.report.Workspaces = append(x.report.Workspaces, filepath.ToSlash(rel))
		if t := idx.GetMetadata().GetToolInfo(); t != nil {
			x.report.Engine = strings.TrimSpace(t.GetName() + " " + t.GetVersion())
		}
		x.add(ws, idx)
	}
}

// workspaces finds the root of the Cargo workspace of each manifest, as
// cargo itself does: a package's manifest names its workspace or is one.
func (x *extractor) workspaces(manifests []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range manifests {
		path, err := output(filepath.Dir(m), "cargo", "locate-project", "--workspace", "--message-format", "plain", "--manifest-path", m)
		if err != nil {
			x.skip(m, 0, fmt.Errorf("cargo cannot place it in a workspace: %w", err))
			continue
		}
		ws := filepath.Dir(path)
		if real, err := filepath.EvalSymlinks(ws); err == nil {
			ws = real
		}
		if !seen[ws] {
			seen[ws] = true
			out = append(out, ws)
		}
	}
	slices.Sort(out)
	return out
}

// runSCIP has rust-analyzer index the workspace at ws, which runs Cargo's
// build scripts and compiles its proc macros first (ADR 009), and reads
// the index it writes.
func runSCIP(ws string) (*scip.Index, error) {
	f, err := os.CreateTemp("", "alidade-*.scip")
	if err != nil {
		return nil, err
	}
	out := f.Name()
	f.Close()
	defer os.Remove(out)
	cmd := exec.Command("rust-analyzer", "scip", ws, "--output", out)
	cmd.Dir = ws
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-analyzer scip: %w: %s", err, lastLines(stderr.String(), 5))
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	var idx scip.Index
	if err := proto.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("rust-analyzer scip: reading its index: %w", err)
	}
	return &idx, nil
}

// add records an index's documents of the files the layer reads, and the
// symbols it lists without a definition. A file two workspaces index is
// read from the first.
func (x *extractor) add(ws string, idx *scip.Index) {
	project := ws
	if u, err := url.Parse(idx.GetMetadata().GetProjectRoot()); err == nil && u.Scheme == "file" {
		project = u.Path
	}
	for _, d := range idx.GetDocuments() {
		abs := filepath.Join(project, filepath.FromSlash(d.GetRelativePath()))
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		rel, err := x.m.Rel(abs)
		if err != nil || !x.reading[rel] || x.docs[rel] != nil {
			continue // outside the root, or in a directory the layer skips
		}
		if d.GetPositionEncoding() == scip.PositionEncoding_UTF16CodeUnitOffsetFromLineStart ||
			d.GetPositionEncoding() == scip.PositionEncoding_UTF32CodeUnitOffsetFromLineStart {
			x.skip(abs, 0, errors.New("its index counts columns in UTF-16 or UTF-32, which the layer does not read; its declarations and calls are not read"))
			continue
		}
		text, err := os.ReadFile(abs)
		if err != nil {
			x.skip(abs, 0, err)
			continue
		}
		x.docs[rel] = &doc{rel: rel, abs: abs, d: d, text: text}
	}
	for _, s := range idx.GetExternalSymbols() {
		x.external[s.GetSymbol()] = true
	}
}
