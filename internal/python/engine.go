// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build python

package python

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/trendvidia/pylib"
)

// jedi is jedi and parso, pinned in VERSIONS; scripts/jedi.sh writes it.
//
//go:embed jedi.tar.gz
var jedi []byte

//go:embed resolve.py
var resolveScript string

// Available reports whether this build can read Python.
func Available() bool { return true }

// CPythonVersion is the CPython linked.
const CPythonVersion = pylib.Version

// Engine identifies what decides a file's answers besides the files under
// the root: resolve.py, jedi and parso, and the CPython linked with its
// standard library, which is all jedi can read outside the root, since it
// infers in this interpreter. Answers kept from an engine of another
// identity are not this one's (#279).
var Engine = sync.OnceValue(func() string {
	h := sha256.New()
	pylibBuild := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, m := range info.Deps {
			if m.Path == "github.com/trendvidia/pylib" {
				pylibBuild = m.Version + " " + m.Sum
			}
		}
	}
	for _, part := range []string{resolveScript, string(jedi), pylib.Version, pylibBuild} {
		fmt.Fprintf(h, "%d\n%s", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
})

// workerEnv marks a process started as a worker (#237): it reads a root
// and its jobs on stdin, writes the resolver's lines on stdout, and exits
// before the program it is part of runs. Every binary that links this
// package can be its own worker, a test binary included, so a worker is
// always this same executable.
const workerEnv = "ALIDADE_PY_WORKER"

func init() {
	if os.Getenv(workerEnv) == "1" {
		os.Exit(serveWorker(os.Stdin, os.Stdout, os.Stderr))
	}
}

func serveWorker(in io.Reader, out, errw io.Writer) int {
	src, err := io.ReadAll(in)
	if err == nil {
		err = start()
	}
	var res string
	if err == nil {
		res, err = pylib.Run(resolveScript, string(src))
	}
	if err != nil {
		fmt.Fprintln(errw, err)
		return 1
	}
	if _, err := io.WriteString(out, res); err != nil {
		fmt.Fprintln(errw, err)
		return 1
	}
	return 0
}

// start unpacks jedi and starts the interpreter, both under the user's
// cache directory, once per process.
func start() error {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	cache := filepath.Join(base, "alidade")
	lib, err := pylib.Unpack(cache, "jedi", jedi)
	if err != nil {
		return err
	}
	return pylib.Start(cache, lib)
}

// inProcess is the size below which a tree is read here rather than by
// workers: starting one costs more than it saves. A variable for the
// tests, as executable is.
var inProcess int64 = 256 << 10

// executable is the worker's program: this one.
var executable = os.Executable

// Analyze reads files, repo-relative under root: their declarations,
// imports and calls. A tree past inProcess bytes is shared between up to
// workers processes, each this executable with its own interpreter; a
// worker that fails has the files it did not finish read here instead,
// and says so in the result.
func Analyze(root string, files []File, workers int) (*Result, error) {
	return AnalyzeEach(context.Background(), root, files, workers, nil)
}

// AnalyzeEach is Analyze, calling each, when it is not nil, with a file's
// own result as soon as every job of the file is done (#269): a worker's
// files as it finishes them, not when it exits. Calls come one at a time,
// from any goroutine. Cancelling ctx stops the workers; the files they
// did not finish are then not read.
func AnalyzeEach(ctx context.Context, root string, files []File, workers int, each func(rel string, r *Result)) (*Result, error) {
	c := newCollector(each)
	var total int64
	for _, f := range files {
		total += f.Size
	}
	if workers <= 1 || total < inProcess {
		parts := plan(files, 1)
		c.expect(parts...)
		if err := here(root, parts, c, "", nil); err != nil {
			return nil, err
		}
		return c.result(1), nil
	}
	parts := plan(files, workers)
	c.expect(parts...)
	for _, jobs := range parts {
		// Smallest first: a worker's jobs take as long in any order, and a
		// file's answers do not depend on what its worker read before
		// (#272), but this way most files are done early, for a reader
		// answering while they arrive (#269).
		slices.Reverse(jobs)
	}
	redo, err := spawnAll(ctx, root, parts, c)
	if err != nil {
		return nil, err
	}
	if len(redo) > 0 {
		if err := here(root, [][]job{redo}, c, "", nil); err != nil {
			return nil, err
		}
	}
	return c.result(len(parts)), nil
}

// spawnAll runs each part in a worker process at once, and returns the
// jobs of the workers that failed that they did not finish, to be read
// here.
func spawnAll(ctx context.Context, root string, parts [][]job, c *collector) ([]job, error) {
	rests := make([][]job, len(parts))
	errs := make([]error, len(parts))
	var wg sync.WaitGroup
	for i, jobs := range parts {
		wg.Go(func() { rests[i], errs[i] = spawn(ctx, root, jobs, c, "stream", nil) })
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var redo []job
	for i, err := range errs {
		if err != nil {
			c.workerErrors = append(c.workerErrors, err.Error())
			redo = append(redo, rests[i]...)
		}
	}
	return redo, c.err
}

// Names answers files by the resolver's warm-up pass (#269): their
// declarations, and their imports and calls resolved by name alone, in
// seconds where Analyze takes minutes. others are the rest of the tree's
// files, which a name can resolve into: the pass reads one only when an
// answer reaches it. Each file's result is keyed by the file. A tree past
// inProcess bytes is read by one worker process.
func Names(root string, files, others []File) (map[string]*Result, error) {
	c := newCollector(nil)
	jobs := make([]job, len(files))
	for i, f := range files {
		jobs[i] = job{rel: f.Rel, shards: 1}
	}
	c.expect(jobs)
	var total int64
	for _, f := range files {
		total += f.Size
	}
	table := make([]string, len(others))
	for i, f := range others {
		table[i] = f.Rel
	}
	if total < inProcess {
		if err := here(root, [][]job{jobs}, c, "names", table); err != nil {
			return nil, err
		}
	} else if _, err := spawn(context.Background(), root, jobs, c, "names", table); err != nil {
		return nil, err
	}
	if c.err != nil {
		return nil, c.err
	}
	return c.results, nil
}

// A collector gathers the resolver's lines job by job, from every process
// reading them, into each file's result.
type collector struct {
	mu   sync.Mutex
	each func(string, *Result)
	// left is how many of a file's jobs are not done, lines the lines of
	// its jobs that are, results the files whose jobs are all done.
	left    map[string]int
	lines   map[string][]string
	results map[string]*Result
	// workerErrors are the workers that failed; err is the first line no
	// parser reads, which fails the whole analysis.
	workerErrors []string
	err          error
}

func newCollector(each func(string, *Result)) *collector {
	return &collector{each: each, left: map[string]int{}, lines: map[string][]string{}, results: map[string]*Result{}}
}

// expect counts the jobs to be done, once each however many processes
// try them: plan splits a file into its shards.
func (c *collector) expect(parts ...[]job) {
	for _, jobs := range parts {
		for _, j := range jobs {
			c.left[j.rel]++
		}
	}
}

// done takes a finished job's lines, and finishes its file when it was the
// file's last job.
func (c *collector) done(j job, lines []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines[j.rel] = append(c.lines[j.rel], lines...)
	if c.left[j.rel]--; c.left[j.rel] > 0 {
		return
	}
	r := &Result{Skipped: map[string]string{}, Workers: 1}
	if err := parse(strings.Join(c.lines[j.rel], "\n"), r); err != nil {
		if c.err == nil {
			c.err = err
		}
		return
	}
	delete(c.lines, j.rel)
	sortResult(r)
	c.results[j.rel] = r
	if c.each != nil {
		c.each(j.rel, r)
	}
}

// result is every file's result joined, as one process would give it.
func (c *collector) result(workers int) *Result {
	return Join(c.results, workers, c.workerErrors)
}

// A reader splits one process's output into its jobs: every line up to a
// DONE line is that job's.
type reader struct {
	c       *collector
	pending []string
	jobs    map[string]job
	// finished are the jobs whose DONE line was read.
	finished map[string]bool
}

func newReader(c *collector, jobs []job) *reader {
	rd := &reader{c: c, jobs: map[string]job{}, finished: map[string]bool{}}
	for _, j := range jobs {
		rd.jobs[jobKey(j.rel, j.shard)] = j
	}
	return rd
}

func jobKey(rel string, shard int) string { return rel + "\t" + strconv.Itoa(shard) }

func (rd *reader) line(l string) error {
	if l == "" {
		return nil
	}
	if rest, ok := strings.CutPrefix(l, "DONE\t"); ok {
		j, ok := rd.jobs[rest]
		if !ok {
			return fmt.Errorf("python: resolver line %q: no such job", l)
		}
		rd.finished[rest] = true
		rd.c.done(j, rd.pending)
		rd.pending = nil
		return nil
	}
	rd.pending = append(rd.pending, l)
	return nil
}

// rest is the jobs the process did not finish.
func (rd *reader) rest() []job {
	var out []job
	for k, j := range rd.jobs {
		if !rd.finished[k] {
			out = append(out, j)
		}
	}
	slices.SortFunc(out, func(a, b job) int { return cmp.Or(cmp.Compare(a.rel, b.rel), cmp.Compare(a.shard, b.shard)) })
	return out
}

// here runs jobs in this process's interpreter, in the resolver's mode:
// "" for jedi, or "names".
func here(root string, parts [][]job, c *collector, mode string, table []string) error {
	if err := start(); err != nil {
		return err
	}
	for _, jobs := range parts {
		rd := newReader(c, jobs)
		out, err := pylib.Run(resolveScript, input(root, jobs, mode, table))
		if err != nil {
			return err
		}
		for _, l := range strings.Split(out, "\n") {
			if err := rd.line(l); err != nil {
				return err
			}
		}
		if c.err != nil {
			return c.err
		}
	}
	return nil
}

// lastLine is a worker's stderr cut to what says why it failed: the last
// line, which for a Python traceback is the exception.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	return s[strings.LastIndexByte(s, '\n')+1:]
}

// spawn runs jobs in a worker process in the resolver's mode, "stream" or
// "names", handing each to c as the worker finishes it. A worker that
// fails returns the jobs it did not finish.
func spawn(ctx context.Context, root string, jobs []job, c *collector, mode string, table []string) ([]job, error) {
	rd := newReader(c, jobs)
	exe, err := executable()
	if err != nil {
		return rd.rest(), err
	}
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = append(os.Environ(), workerEnv+"=1")
	cmd.Stdin = strings.NewReader(input(root, jobs, mode, table))
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.StdoutPipe()
	if err != nil {
		return rd.rest(), err
	}
	if err := cmd.Start(); err != nil {
		return rd.rest(), err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var lineErr error
	for sc.Scan() {
		if lineErr == nil {
			lineErr = rd.line(sc.Text())
		}
	}
	err = cmd.Wait()
	if err == nil {
		err = cmp.Or(sc.Err(), lineErr)
	}
	rest := rd.rest()
	if err == nil && len(rest) > 0 {
		// Its output ended early, though it exited cleanly.
		err = fmt.Errorf("exited with %d of its %d jobs unfinished", len(rest), len(jobs))
	}
	if err != nil {
		return rest, fmt.Errorf("python worker: %w: %s", err, lastLine(errb.String()))
	}
	return nil, nil
}

// input is the resolver's IN for a root and its jobs, in a mode: "stream"
// has a worker write each job's lines as it finishes it, "names" answers
// by the warm-up pass, which also knows the files in table.
func input(root string, jobs []job, mode string, table []string) string {
	var b strings.Builder
	if mode != "" {
		b.WriteString(mode + "\n")
	}
	b.WriteString(root)
	for _, j := range jobs {
		fmt.Fprintf(&b, "\n%s\t%d\t%d", j.rel, j.shard, j.shards)
	}
	if len(table) > 0 {
		b.WriteString("\nTABLE\n" + strings.Join(table, "\n"))
	}
	return b.String()
}
