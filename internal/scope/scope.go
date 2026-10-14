// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package scope says which directories of a repository alidade leaves
// out: the ones that hold other people's code rather than the
// repository's own — vendored dependencies and third-party sources — by
// default, or the list the repository writes in .alidade.pxf at its root
// (ADR 003, amended for #215):
//
//	# Directories no layer reads. Replaces the default, vendor and third_party.
//	skip = ["vendor", "third_party", "web/legacy"]
//
// An entry without a "/" is a directory name, skipped wherever it is; one
// with a "/" is a repo-relative path, skipped with everything under it.
// Every layer, search_text, the build's fingerprint and the grep hook
// read the same list, so what one leaves out none of the others reads.
// It adds to the directories each layer already skips as build output or
// dependencies (node_modules, dist, .git); it cannot bring one of those
// back.
//
// Installed packages are left out besides the list, wherever they are,
// and the list cannot bring them back (#266): a virtual environment — a
// directory holding a pyvenv.cfg, whatever its name — and a directory
// named .venv, venv or site-packages. They are the project's
// dependencies, as node_modules is, and every layer leaves them out, not
// only the Python layer that installs them.
package scope

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// File is the configuration's name, at the repository's root.
const File = ".alidade.pxf"

// Default is the list without a configuration.
var Default = []string{"vendor", "third_party"}

// installed are the names of directories that hold installed packages.
var installed = map[string]bool{".venv": true, "venv": true, "site-packages": true}

// Venv reports whether dir is a virtual environment: it holds a
// pyvenv.cfg, as venv, virtualenv and uv write one.
func Venv(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "pyvenv.cfg"))
	return err == nil
}

// Dirs is the list of directories left out.
type Dirs struct {
	names map[string]bool
	paths []string
}

// New makes the list from its entries.
func New(entries []string) *Dirs {
	d := &Dirs{names: map[string]bool{}}
	for _, e := range entries {
		e = strings.Trim(filepath.ToSlash(strings.TrimSpace(e)), "/")
		switch {
		case e == "":
		case strings.Contains(e, "/"):
			d.paths = append(d.paths, e)
		default:
			d.names[e] = true
		}
	}
	return d
}

// Load reads root's configuration, or gives the default when it has none.
func Load(root string) (*Dirs, error) {
	b, err := os.ReadFile(filepath.Join(root, File))
	if errors.Is(err, fs.ErrNotExist) {
		return New(Default), nil
	}
	if err != nil {
		return nil, fmt.Errorf("scope: %w", err)
	}
	entries, err := parse(b)
	if err != nil {
		return nil, fmt.Errorf("scope: %s: %w", File, err)
	}
	if entries == nil {
		entries = Default
	}
	return New(entries), nil
}

// parse reads the skip list; nil when the file does not set one.
func parse(b []byte) ([]string, error) {
	doc, err := pxf.Parse(b)
	if err != nil {
		return nil, err
	}
	var out []string
	set := false
	for _, e := range doc.Entries {
		a, ok := e.(*pxf.Assignment)
		if !ok {
			return nil, fmt.Errorf("unexpected entry %T: only skip is read", e)
		}
		if a.Key != "skip" {
			return nil, fmt.Errorf("unknown key %q: only skip is read", a.Key)
		}
		l, ok := a.Value.(*pxf.ListVal)
		if !ok {
			return nil, errors.New("skip is a list of directory names or paths")
		}
		set = true
		out = []string{}
		for _, v := range l.Elements {
			s, ok := v.(*pxf.StringVal)
			if !ok {
				return nil, errors.New("skip's entries are strings")
			}
			out = append(out, s.Value)
		}
	}
	if !set {
		return nil, nil
	}
	return out, nil
}

// OrDefault is d, or the default list when d is nil: an adapter run
// without a build, as its tests run, skips what a build would.
func (d *Dirs) OrDefault() *Dirs {
	if d == nil {
		return New(Default)
	}
	return d
}

// Skips reports whether rel, repo-relative, is in or under a directory
// left out: one the list names, or one named for installed packages. A
// virtual environment under another name takes a look at the directory
// itself, which [Dirs.SkipsDir] and [Dirs.SkipsUnder] make.
func (d *Dirs) Skips(rel string) bool {
	d = d.OrDefault()
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	if rel == "" || rel == "." {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if d.names[seg] || installed[seg] {
			return true
		}
	}
	for _, p := range d.paths {
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

// SkipsDir is Skips for an absolute directory under root, and also
// reports a virtual environment, whatever its name. A walk asks it of
// every directory it would enter, so it never enters one under a
// directory it reports.
func (d *Dirs) SkipsDir(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." {
		return false
	}
	return d.Skips(rel) || Venv(dir)
}

// SkipsUnder reports whether p, a file or directory under root that no
// walk reached, is in or under a directory left out: SkipsDir of p and
// of each directory between it and root.
func (d *Dirs) SkipsUnder(root, p string) bool {
	if d.Skips(relOf(root, p)) {
		return true
	}
	for dir := p; ; dir = filepath.Dir(dir) {
		rel := relOf(root, dir)
		if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
			return false
		}
		if Venv(dir) {
			return true
		}
	}
}

func relOf(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}
