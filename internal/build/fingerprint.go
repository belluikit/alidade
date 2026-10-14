// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package build

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/belluikit/alidade/internal/scope"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Fingerprint says cheaply whether the tree the session builds from may
// have changed since a previous call: a hash over every path the walk
// sees under the root and each schema root, with each file's size and
// modification time and each directory's presence (#204). It decides
// only whether to ask; what changed is still decided by the layers'
// content signatures, which a modification time cannot fool. Measured
// on the corpus: 3–20 ms for 492–1,866 files, against 90–210 ms for the
// content signatures and seconds for a rebuild.
//
// It misses one edit: a file rewritten to the same size with its old
// modification time restored, which an editor, git or an agent does not
// do. A file added, removed or renamed changes the set, and so the hash.
func (s *Session) Fingerprint() (string, error) {
	h := sha256.New()
	roots := []string{s.root}
	for _, r := range s.opts.SchemaRoots {
		abs, _, err := schemaRoot(r)
		if err != nil {
			return "", err
		}
		roots = append(roots, abs)
	}
	for _, root := range roots {
		lines, err := stats(root)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\n%s\n", root, strings.Join(lines, "\n"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// stats lists every entry under root the signatures' walk would see, as
// "path size mtime" for a file and "path/" for a directory, sorted.
func stats(root string) ([]string, error) {
	// A configuration that does not read leaves the default list here;
	// the build it triggers says what is wrong with it.
	skip, err := scope.Load(root)
	if err != nil {
		skip = scope.New(scope.Default)
	}
	var lines []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil // the layers record what they cannot read (#92)
		}
		if path == root {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if walkSkip[d.Name()] || skip.SkipsDir(root, path) {
				return filepath.SkipDir
			}
			lines = append(lines, filepath.ToSlash(rel)+"/")
			return nil
		}
		info, err := d.Info()
		if err != nil {
			lines = append(lines, filepath.ToSlash(rel)+" unreadable")
			return nil
		}
		lines = append(lines, fmt.Sprintf("%s %d %d", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	sort.Strings(lines)
	return lines, err
}
