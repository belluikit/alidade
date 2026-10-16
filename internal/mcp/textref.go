// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/belluikit/alidade/internal/worktree"
)

// search_text at a commit (#23): what origin/main says after a merge, or
// what a pull request's head held, without checking it out. git reads
// the commit — ls-tree for its files, cat-file for their contents — and
// search_text matches them as it matches files on disk. It is git's own
// object store, so alidade parses no format to do it (ADR 000).

// refParam resolves ref in the tree searched to the commit it names.
func refParam(tree, ref string) (string, error) {
	if strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("ref: %q is not a revision", ref)
	}
	out, err := gitOut(tree, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("ref: %q names no commit in %s", ref, tree)
	}
	return strings.TrimSpace(string(out)), nil
}

// A blob is one file of a commit.
type blob struct {
	rel, sha string
	size     int
}

// walkRef calls visit for every matching line of every text file of the
// commit, in path order, which is git's tree order.
func (ts *textSearch) walkRef(visit func(rel string, line int, text string, src []byte) bool) error {
	blobs, err := ts.refBlobs()
	if err != nil || len(blobs) == 0 {
		return err
	}
	var in bytes.Buffer
	for _, b := range blobs {
		in.WriteString(b.sha + "\n")
	}
	cmd := exec.Command("git", "-C", ts.root, "cat-file", "--batch")
	cmd.Env = worktree.GitEnv()
	cmd.Stdin = &in
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("git cat-file: %w", err)
	}
	r := bufio.NewReader(out)
	for _, b := range blobs {
		src, err := readBlob(r)
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("git cat-file %s: %w", b.rel, err)
		}
		if binary(src) {
			continue
		}
		if err := ts.searchSrc(b.rel, src, visit); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return err
		}
	}
	return cmd.Wait()
}

// refBlobs are the commit's files to search: regular files the call asks
// for, under no directory search_text leaves out, and not too large.
// The skip list is the one on disk, .alidade.pxf of the tree searched.
// Without --full-tree, ls-tree lists the directory searched, by paths
// relative to it, as the walk on disk does when the graph is built from
// a directory below the checkout's top.
func (ts *textSearch) refBlobs() ([]blob, error) {
	out, err := gitOut(ts.root, "ls-tree", "-r", "-z", "--long", ts.ref)
	if err != nil {
		return nil, fmt.Errorf("git ls-tree %s: %w", ts.ref, err)
	}
	var all []blob
	venvs := map[string]bool{}
	for _, entry := range bytes.Split(out, []byte{0}) {
		if b, ok := parseTreeEntry(string(entry)); ok {
			all = append(all, b)
			if dir, file, ok := cutLast(b.rel); ok && file == "pyvenv.cfg" {
				venvs[dir] = true
			}
		}
	}
	var blobs []blob
	for _, b := range all {
		if b.size > maxTextFile || !ts.wanted(b.rel) || ts.skippedRel(b.rel, venvs) {
			continue
		}
		blobs = append(blobs, b)
	}
	return blobs, nil
}

// cutLast splits a repo-relative path at its last slash: its directory,
// and its name; false for a file at the top.
func cutLast(rel string) (string, string, bool) {
	i := strings.LastIndexByte(rel, '/')
	if i < 0 {
		return "", rel, false
	}
	return rel[:i], rel[i+1:], true
}

// parseTreeEntry reads one line of ls-tree --long, "mode type sha
// size\tpath", when it is a regular file: not a symbolic link or a
// submodule's commit.
func parseTreeEntry(entry string) (blob, bool) {
	meta, rel, ok := strings.Cut(entry, "\t")
	f := strings.Fields(meta)
	if !ok || len(f) != 4 || f[1] != "blob" || f[0] == "120000" {
		return blob{}, false
	}
	size, err := strconv.Atoi(f[3])
	if err != nil {
		return blob{}, false
	}
	return blob{rel: rel, sha: f[2], size: size}, true
}

// skippedRel reports whether a repo-relative file lies under a directory
// search_text leaves out: by name, by the skip list, or as a virtual
// environment, one of venvs, which [scope.Dirs.SkipsDir] finds on disk
// by its pyvenv.cfg.
func (ts *textSearch) skippedRel(rel string, venvs map[string]bool) bool {
	dirs := strings.Split(rel, "/")
	for i, d := range dirs[:len(dirs)-1] {
		if textSkipDirs[d] || venvs[strings.Join(dirs[:i+1], "/")] {
			return true
		}
	}
	return ts.skip.Skips(rel[:max(strings.LastIndexByte(rel, '/'), 0)])
}

// readBlob reads one object of cat-file --batch: a header, "sha type
// size", the contents, and a newline.
func readBlob(r *bufio.Reader) ([]byte, error) {
	header, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	f := strings.Fields(header)
	if len(f) != 3 {
		return nil, fmt.Errorf("unexpected header %q", strings.TrimSpace(header))
	}
	size, err := strconv.Atoi(f[2])
	if err != nil {
		return nil, err
	}
	src := make([]byte, size+1)
	if _, err := io.ReadFull(r, src); err != nil {
		return nil, err
	}
	return src[:size], nil
}

// gitOut runs git in dir and returns its output.
func gitOut(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = worktree.GitEnv() // -C does not override a GIT_DIR exported
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && stderr.Len() > 0 {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, err
}
