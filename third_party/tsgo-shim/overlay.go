// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package tsgo

import (
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/microsoft/typescript-go/internal/vfs"
)

// Overlay is base with files of its own, absolute and forward-slashed,
// laid over it: the frontend layer's translations of Svelte components,
// which the checker reads beside the component as Card.svelte.tsx, so an
// import of ./Card.svelte resolves to it (#221). Nothing is written.
func Overlay(base FS, files map[string]string) FS {
	o := &overlay{FS: base, files: files, dirs: map[string]map[string]bool{}}
	for f := range files {
		o.dirs[path.Dir(f)] = setOf(o.dirs[path.Dir(f)], path.Base(f))
	}
	return o
}

type overlay struct {
	vfs.FS
	files map[string]string
	dirs  map[string]map[string]bool
}

func setOf(m map[string]bool, k string) map[string]bool {
	if m == nil {
		m = map[string]bool{}
	}
	m[k] = true
	return m
}

func (o *overlay) FileExists(p string) bool {
	if _, ok := o.files[p]; ok {
		return true
	}
	return o.FS.FileExists(p)
}

func (o *overlay) ReadFile(p string) (string, bool) {
	if s, ok := o.files[p]; ok {
		return s, true
	}
	return o.FS.ReadFile(p)
}

func (o *overlay) DirectoryExists(p string) bool {
	if _, ok := o.dirs[strings.TrimSuffix(p, "/")]; ok {
		return true
	}
	return o.FS.DirectoryExists(p)
}

func (o *overlay) GetAccessibleEntries(p string) vfs.Entries {
	e := o.FS.GetAccessibleEntries(p)
	for name := range o.dirs[strings.TrimSuffix(p, "/")] {
		e.Files = append(e.Files, name)
	}
	return e
}

func (o *overlay) Stat(p string) vfs.FileInfo {
	if s, ok := o.files[p]; ok {
		return fileInfo{name: path.Base(p), size: int64(len(s))}
	}
	return o.FS.Stat(p)
}

func (o *overlay) Realpath(p string) string {
	if _, ok := o.files[p]; ok {
		return p
	}
	return o.FS.Realpath(p)
}

type fileInfo struct {
	name string
	size int64
}

func (f fileInfo) Name() string       { return f.name }
func (f fileInfo) Size() int64        { return f.size }
func (f fileInfo) Mode() fs.FileMode  { return 0o444 }
func (f fileInfo) ModTime() time.Time { return time.Time{} }
func (f fileInfo) IsDir() bool        { return false }
func (f fileInfo) Sys() any           { return nil }
