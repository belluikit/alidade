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
	"regexp"
	"slices"
	"strings"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// A doc is one file of an index: its document and its text.
type doc struct {
	rel  string
	abs  string
	ws   string // the workspace whose index it is
	d    *scip.Document
	text []byte
	// moduleSym is the symbol of the file's own module, which every
	// item's symbol in the file starts with.
	moduleSym string
	split     [][]byte // text's lines, once read
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
			x.unread[ws] = err.Error()
			continue
		}
		rel, _ := filepath.Rel(x.root, ws)
		x.report.Workspaces = append(x.report.Workspaces, filepath.ToSlash(rel))
		x.indexed = append(x.indexed, ws)
		if t := idx.GetMetadata().GetToolInfo(); t != nil {
			x.report.Engine = strings.TrimSpace(t.GetName() + " " + t.GetVersion())
		}
		x.add(ws, idx)
	}
}

// workspaces finds the root of the Cargo workspace of each manifest, as
// cargo itself does: a package's manifest names its workspace or is one.
// It records each workspace's manifests.
func (x *extractor) workspaces(manifests []string) []string {
	seen := map[string]bool{}
	var out []string
	x.manifests = map[string][]string{}
	for _, m := range manifests {
		path, err := output(filepath.Dir(m), "cargo", "locate-project", "--workspace", "--message-format", "plain", "--manifest-path", m)
		if err != nil {
			err = fmt.Errorf("cargo cannot place it in a workspace: %w", err)
			x.skip(m, 0, err)
			x.unread[filepath.Dir(m)] = err.Error()
			continue
		}
		ws := filepath.Dir(path)
		if real, err := filepath.EvalSymlinks(ws); err == nil {
			ws = real
		}
		x.manifests[ws] = append(x.manifests[ws], m)
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
	done, err := lockOutside(cmd, ws)
	if err != nil {
		return nil, err
	}
	defer done()
	noInstall(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-analyzer scip: %w: %s", err, lastLines(stderr.String(), 5))
	}
	if lost := stdLost(stderr.String()); lost != "" {
		return nil, errors.New(lost)
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

var stdLostRE = regexp.MustCompile("(?m)ERROR can't load standard library, try installing `rust-src`(?: sysroot_path=(\\S+))?")

// stdLost says why a workspace is not read when rust-analyzer indexed it
// without the standard library's source, or "" when it had it. The check
// before indexing looks at the toolchain of the repository's root, but
// rustup gives each workspace the toolchain its rust-toolchain.toml pins
// (#130). rust-analyzer adds rust-src itself to a rustup toolchain that
// lacks it (`rustup component add rust-src`, in the workspace), so this is
// the case where that failed: offline, or a toolchain rustup does not
// manage. The index is then whole in shape and wrong in substance, a call
// through a std type left out, and `rust-analyzer scip` still exits 0; its
// standard error is the only place that says so (measured on
// rust-analyzer 1.100). That is log text, not an API: the day it changes,
// TestWorkspaceWithoutStd fails.
func stdLost(stderr string) string {
	m := stdLostRE.FindStringSubmatch(stderr)
	if m == nil {
		return ""
	}
	where := "its toolchain"
	if m[1] != "" {
		where = m[1]
	}
	return fmt.Sprintf("rust-analyzer could not load the standard library's source from %s, and without it loses the type of a quarter of all expressions, so the workspace is not read: run `rustup component add rust-src` in it, where rustup picks the toolchain its rust-toolchain.toml pins", where)
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
		x.docs[rel] = &doc{rel: rel, abs: abs, ws: ws, d: d, text: text}
	}
	for _, s := range idx.GetExternalSymbols() {
		x.external[s.GetSymbol()] = true
	}
}
