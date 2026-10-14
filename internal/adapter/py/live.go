// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package py

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/trendvidia/alidade/internal/graph"
	"github.com/trendvidia/alidade/internal/python"
)

// A Live is jedi reading a tree in the background while the layer answers
// from the warm-up pass (#269): every file has the warm-up's answers until
// jedi's for it arrive, and [Refresh] builds the layer from what is known
// at the time. Once jedi has read every file, the layer is the one a
// build without the background gives.
type Live struct {
	root     string
	files    []python.File
	testdata int
	skipped  []graph.Skip

	mu           sync.Mutex
	warm, jedi   map[string]*python.Result
	workers      int
	workerErrors []string
	err          error
	// sums are content hashes: of each file read, taken before jedi
	// started, and of each other file jedi parsed for one, taken as its
	// answer came. A later build compares them to tell which answers an
	// edit cannot have changed (#269). An empty sum never matches.
	sums    map[string]string
	started time.Time
	// reused counts the files whose answers came from an earlier build.
	reused int
	// cache is the directory jedi's answers are written to, when not
	// empty, and kept whether they have been since jedi stopped: written
	// before done is closed, read after.
	cache string
	kept  bool

	version atomic.Int64
	cancel  context.CancelFunc
	done    chan struct{}
}

// names and analyzeEach are the python package's, variables for the tests.
var (
	names       = python.Names
	analyzeEach = python.AnalyzeEach
)

// startLive starts jedi on the files whose answers are not in kept,
// jedi's from an earlier build that still hold.
func startLive(x *extractor, files []python.File, warm, kept map[string]*python.Result, sums map[string]string, opts liveOptions) *Live {
	ctx, cancel := context.WithCancel(context.Background())
	l := &Live{root: x.root, files: files, testdata: x.report.Testdata, skipped: slices.Clone(x.report.Skipped),
		warm: warm, jedi: kept, sums: sums, started: opts.started, reused: len(kept), cache: opts.cache, cancel: cancel, done: make(chan struct{})}
	var todo []python.File
	for _, f := range files {
		if _, ok := kept[f.Rel]; !ok {
			todo = append(todo, f)
		}
	}
	go func() {
		defer close(l.done)
		defer func() {
			if ctx.Err() == nil {
				l.keep()
				l.kept = true
			}
		}()
		if len(todo) == 0 {
			return
		}
		r, err := analyzeEach(ctx, l.root, todo, opts.workers, func(rel string, f *python.Result) {
			outside := l.outsideSums(f)
			l.mu.Lock()
			l.jedi[rel] = f
			for p, sum := range outside {
				if _, ok := l.sums[p]; !ok {
					l.sums[p] = sum
				}
			}
			l.mu.Unlock()
			l.version.Add(1)
		})
		l.mu.Lock()
		if err != nil {
			l.err = err
		} else {
			l.workers, l.workerErrors = r.Workers, r.WorkerErrors
		}
		l.mu.Unlock()
		l.version.Add(1)
	}()
	return l
}

// liveOptions are what startLive needs besides the answers.
type liveOptions struct {
	workers int
	started time.Time
	cache   string
}

// keep writes what l knows to its cache, if it has one. A cache that
// cannot be written costs only the next session's time, so it is not an
// error.
func (l *Live) keep() {
	if l.cache != "" {
		_ = l.save(l.cache)
	}
}

// Close stops jedi and keeps what it knows, for a session that ends
// before jedi is done: the files it answered are kept, the rest left to
// the next session. What a Live that finished knows is kept already.
func (l *Live) Close() {
	l.Stop()
	if !l.kept {
		l.keep()
		l.kept = true
	}
}

// Version changes whenever jedi has read more of the tree, or finished.
func (l *Live) Version() int64 { return l.version.Load() }

// Progress is how many files jedi has read, of how many, and whether it
// is done.
func (l *Live) Progress() (resolved, total int, done bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.done:
		done = true
	default:
	}
	return len(l.jedi), len(l.files), done
}

// Wait returns once jedi is done.
func (l *Live) Wait() { <-l.done }

// Stop stops jedi and returns once it has. A tree read in this process
// rather than by workers cannot be interrupted, and is waited for.
func (l *Live) Stop() {
	l.cancel()
	<-l.done
}

// snapshot is every file's answers as known now, and the files whose
// answers are still the warm-up pass's.
func (l *Live) snapshot() (*python.Result, map[string]bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	files, inferred := map[string]*python.Result{}, map[string]bool{}
	for _, f := range l.files {
		if r, ok := l.jedi[f.Rel]; ok {
			files[f.Rel] = r
		} else if r, ok := l.warm[f.Rel]; ok {
			files[f.Rel] = r
			inferred[f.Rel] = true
		}
	}
	return python.Join(files, l.workers, l.workerErrors), inferred, l.err
}

// outsideSums hashes the files jedi parsed for a file's answers that the
// layer does not read itself. One changed since jedi started may have
// been read as it was, and gets an empty sum.
func (l *Live) outsideSums(r *python.Result) map[string]string {
	out := map[string]string{}
	for _, d := range r.Deps {
		for _, p := range d.Files {
			if _, ok := l.sums[p]; ok { // a file read: hashed at the start, never written after
				continue
			}
			path := filepath.Join(l.root, filepath.FromSlash(p))
			if st, err := os.Stat(path); err != nil || st.ModTime().After(l.started) {
				out[p] = ""
			} else {
				out[p] = contentHash(path)
			}
		}
	}
	return out
}

// background answers from the warm-up pass and starts jedi behind it.
// With opts.Reuse, the files whose answers an edit since cannot have
// changed keep jedi's from it, and jedi reads only the rest.
func (x *extractor) background(files []python.File, opts Options) error {
	started := time.Now()
	sums := map[string]string{}
	for _, f := range files {
		sums[f.Rel] = contentHash(filepath.Join(x.root, filepath.FromSlash(f.Rel)))
	}
	kept := map[string]*python.Result{}
	reuse := opts.Reuse
	if reuse == nil && opts.Cache != "" {
		reuse = loadLive(opts.Cache, x.root)
	}
	if reuse != nil {
		kept = reuse.reusable(files, sums)
	}
	// Only the files jedi reads need the warm-up's answers.
	var todo, rest []python.File
	for _, f := range files {
		if _, ok := kept[f.Rel]; ok {
			rest = append(rest, f)
		} else {
			todo = append(todo, f)
		}
	}
	warm, err := names(x.root, todo, rest)
	if err != nil {
		return fmt.Errorf("py: %w", err)
	}
	workers := opts.Workers
	if workers == 0 {
		workers = DefaultWorkers()
	}
	x.report.Live = startLive(x, files, warm, kept, sums, liveOptions{workers: workers, started: started, cache: opts.Cache})
	return x.fromLive(x.report.Live)
}

// reusable is jedi's answers from l for the files given that an edit
// since cannot have changed, and adds to sums, which holds the files'
// hashes now, those of the other files the answers kept depend on. l
// must be stopped.
func (l *Live) reusable(files []python.File, sums map[string]string) map[string]*python.Result {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.edits(files, sums)
	kept := map[string]*python.Result{}
	for _, f := range files {
		r, ok := l.jedi[f.Rel]
		if !ok || e.changed[f.Rel] || !e.keeps(f.Rel, r.Deps[f.Rel]) {
			continue
		}
		kept[f.Rel] = r
		if d := r.Deps[f.Rel]; d != nil {
			for _, p := range d.Files {
				if _, ok := sums[p]; !ok {
					sums[p] = e.now[p]
				}
			}
		}
	}
	return kept
}

// edits is what changed in the tree since a Live started: what a file's
// answers are checked against.
type edits struct {
	// changed are the files whose contents changed, appeared or went away,
	// of those the Live hashed or the files given; moved those that
	// appeared or went away.
	changed, moved map[string]bool
	// modules are the names whose lookup a moved file can change: its own,
	// its package's when it is a package marker, and each of its
	// directories' that came or went with it. packages are the directories
	// whose package marker (__init__.py) moved, slash-terminated.
	modules, packages map[string]bool
	// texts are the changed files' contents, as they are now, and words
	// the words in them.
	texts []string
	words map[string]bool
	// now is every file the Live hashed, as it is now.
	now map[string]string
}

// edits compares l's hashes with the tree now, where sums are the
// hashes of files. l.mu must be held.
func (l *Live) edits(files []python.File, sums map[string]string) *edits {
	e := &edits{changed: map[string]bool{}, moved: map[string]bool{}, modules: map[string]bool{},
		packages: map[string]bool{}, now: map[string]string{}, words: map[string]bool{}}
	for p, was := range l.sums {
		now, ok := sums[p]
		if !ok {
			now = contentHash(filepath.Join(l.root, filepath.FromSlash(p)))
		}
		e.now[p] = now
		if was == "" || now != was {
			e.changed[p] = true
		}
	}
	e.move(l.files, files)
	for p := range e.changed {
		if b, err := os.ReadFile(filepath.Join(l.root, filepath.FromSlash(p))); err == nil {
			e.texts = append(e.texts, string(b))
			for _, w := range strings.FieldsFunc(string(b), notWord) {
				e.words[w] = true
			}
		}
	}
	return e
}

// move records the files that appeared or went away between before and
// after, and what a lookup by name can find them by.
func (e *edits) move(before, after []python.File) {
	was := map[string]bool{}
	for _, f := range before {
		was[f.Rel] = true
	}
	for _, f := range after {
		if !was[f.Rel] {
			e.changed[f.Rel], e.moved[f.Rel] = true, true
		}
		delete(was, f.Rel)
	}
	for p := range was {
		e.changed[p], e.moved[p] = true, true
	}
	// A lookup of a directory's name finds the same thing whatever files
	// the directory holds, so a moved file changes it only when the
	// directory itself came or went with it (#281): on openpilot, 400 files
	// looked up "openpilot", and each reran for any file added under it.
	wasDir, isDir := dirsOf(before), dirsOf(after)
	for p := range e.moved {
		dir, base := path.Split(p)
		for d := strings.TrimSuffix(dir, "/"); d != ""; d = parentDir(d) {
			if !wasDir[d] || !isDir[d] {
				e.modules[path.Base(d)] = true
			}
		}
		if base == "__init__.py" || base == "__init__.pyi" {
			// A package marker changes what its directory's name finds.
			e.packages[dir] = true
			if dir != "" {
				e.modules[path.Base(strings.TrimSuffix(dir, "/"))] = true
			}
			continue
		}
		e.modules[strings.TrimSuffix(strings.TrimSuffix(base, ".pyi"), ".py")] = true
	}
}

// dirsOf is every directory holding one of files, at any depth,
// slash-separated and without a trailing slash.
func dirsOf(files []python.File) map[string]bool {
	dirs := map[string]bool{}
	for _, f := range files {
		for d := parentDir(f.Rel); d != "" && !dirs[d]; d = parentDir(d) {
			dirs[d] = true
		}
	}
	return dirs
}

// parentDir is the directory holding p, "" at the root.
func parentDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

// keeps reports whether the answers of an unchanged file, which depend
// on d, survive e: resolve.py's Record says what d holds.
//   - No file jedi parsed for it changed, appeared or went away.
//   - No file appeared or went away that a module name it looked up
//     could find: the file under its own name, a package marker under its
//     package's, and a directory that came or went with the file under
//     the directory's.
//   - No package marker above it appeared or went away. Which of its
//     parent directories are packages decides its sys.path in jedi; no
//     case is known where that alone changes an answer, so this rule is
//     the cautious side of one.
//   - No file changed or added contains a name it searched the project
//     for, and when a search reached jedi's cap on files opened, no file
//     appeared or went away at all.
func (e *edits) keeps(file string, d *python.Deps) bool {
	for dir := range e.packages {
		if strings.HasPrefix(file, dir) {
			return false
		}
	}
	if d == nil {
		return true
	}
	if d.All && len(e.moved) > 0 {
		return false
	}
	for _, p := range d.Files {
		if e.changed[p] {
			return false
		}
	}
	for _, m := range d.Modules {
		if e.modules[m] {
			return false
		}
	}
	for _, n := range d.Names {
		if e.contain(n) {
			return false
		}
	}
	return true
}

// contain reports whether a changed file holds name as jedi's search
// matches it, a whole word (\bname\b). Python's \w holds every letter,
// decimal digit and underscore notWord does, so a word split here is one
// at least as fine as Python's; a name with any other character is
// looked for as text.
func (e *edits) contain(name string) bool {
	if !strings.ContainsFunc(name, notWord) {
		return e.words[name]
	}
	for _, t := range e.texts {
		if strings.Contains(t, name) {
			return true
		}
	}
	return false
}

func notWord(r rune) bool { return r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) }

// contentHash hashes a file; one that cannot be read has an empty hash,
// which matches nothing.
func contentHash(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fromLive adds the declarations and edges l knows now.
func (x *extractor) fromLive(l *Live) error {
	r, inferred, err := l.snapshot()
	x.report.Pending, x.report.Reused = len(inferred), l.reused
	if err != nil && len(inferred) > 0 && err != context.Canceled {
		x.skip(x.root, fmt.Errorf("jedi stopped before reading every file, and the calls of %d stay INFERRED by name: %w", len(inferred), err))
	}
	return x.emit(l.files, r, inferred)
}

// Refresh builds the layer into b again from what l knows now: jedi's
// answers for the files it has read, the warm-up pass's for the rest.
func Refresh(b *graph.Builder, l *Live) (*Report, error) {
	start := time.Now()
	x := &extractor{b: b, m: b.Minter(), root: l.root, emitted: map[graph.ID]bool{},
		report: &Report{Testdata: l.testdata, Skipped: slices.Clone(l.skipped), Live: l}}
	for _, f := range l.files {
		if err := x.file(f.Rel); err != nil {
			return nil, err
		}
	}
	if err := x.fromLive(l); err != nil {
		return nil, err
	}
	x.report.Elapsed = time.Since(start)
	return x.report, nil
}
