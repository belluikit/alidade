// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bench

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestBench runs the benchmark: every question of the set in both arms,
// ALIDADE_BENCH_RUNS times each. It spends money, so it runs only when
// ALIDADE_BENCH names the repository checkout to ask about
// (scripts/bench.sh). The checkout must be at the set's commit.
//
//	ALIDADE_BENCH        the repository checkout
//	ALIDADE_BENCH_SET    the question file (testdata/protocompile.pxf)
//	ALIDADE_BENCH_ONLY   question ids to run, comma-separated (all)
//	ALIDADE_BENCH_ARMS   arms to run, comma-separated (alidade,none)
//	ALIDADE_BENCH_RUNS   runs per question per arm (1)
//	ALIDADE_BENCH_JOBS   sessions at once (2)
//	ALIDADE_BENCH_OUT    the results file (required)
//	ALIDADE_BENCH_BIN    the alidade binary (alidade on PATH)
//	ALIDADE_BENCH_MODEL  the model (claude-sonnet-5-5)
//	ALIDADE_BENCH_BUDGET dollars per session (1)
//
// The graphify and graphify_grep arms (#284) need two more: ALIDADE_BENCH_GRAPHIFY, the
// graphify-mcp binary, and ALIDADE_BENCH_GRAPH, the graph.json it built
// for the checkout. graphify_installed needs ALIDADE_BENCH_GRAPHIFY_EXE,
// the graphify binary; ALIDADE_BENCH_GRAPHIFY_DIR, a copy of the checkout
// at the same commit holding graphify-out; and ALIDADE_BENCH_GRAPHIFY_MD,
// the CLAUDE.md section graphify installs (graphify/always_on/claude-md.md).
func TestBench(t *testing.T) {
	repo := os.Getenv("ALIDADE_BENCH")
	if repo == "" {
		t.Skip("ALIDADE_BENCH is unset; scripts/bench.sh runs the benchmark")
	}
	set, err := Load(env("ALIDADE_BENCH_SET", "testdata/protocompile.pxf"))
	if err != nil {
		t.Fatal(err)
	}
	out := os.Getenv("ALIDADE_BENCH_OUT")
	if out == "" {
		t.Fatal("ALIDADE_BENCH_OUT is unset")
	}
	h, r := setup(t, repo, set)
	questions := selected(t, set)
	runs := run(t, r, questions, arms(t, h.Alidade, repo))
	h.Alidade = version(t, h.Alidade, "version")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := Write(f, h, runs); err != nil {
		t.Fatal(err)
	}
	for _, s := range Summarize(runs) {
		t.Logf("%-24s %-8s correct %d/%d  tokens %8d  $%.4f  alidade calls %d", s.Question, s.Arm, s.Correct, s.Runs, s.Tokens, s.CostUSD, s.AlidadeCalls)
	}
}

// setup checks the checkout is at the set's commit and builds the header
// and the runner. The header's Alidade holds the binary's path until the
// runs are done.
func setup(t *testing.T, repo string, set *Set) (Header, *Runner) {
	head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse in %s: %v", repo, err)
	}
	if got := strings.TrimSpace(string(head)); got != set.Commit {
		t.Fatalf("%s is at %s; the references were written against %s", repo, got, set.Commit)
	}
	bin, err := exec.LookPath(env("ALIDADE_BENCH_BIN", "alidade"))
	if err != nil {
		t.Fatal(err)
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	budget, err := strconv.ParseFloat(env("ALIDADE_BENCH_BUDGET", "1"), 64)
	if err != nil {
		t.Fatal(err)
	}
	h := Header{Repo: set.Repo, Commit: set.Commit, Model: env("ALIDADE_BENCH_MODEL", "claude-sonnet-5-5"),
		Alidade: bin, Claude: version(t, claude, "--version"), Started: time.Now()}
	return h, &Runner{Language: set.Language, Repo: repo, Claude: claude, Model: h.Model, Budget: budget, Scratch: t.TempDir()}
}

func arms(t *testing.T, bin, repo string) []Arm {
	all := Arms(bin, repo)
	if server, graph := os.Getenv("ALIDADE_BENCH_GRAPHIFY"), os.Getenv("ALIDADE_BENCH_GRAPH"); server != "" && graph != "" {
		maps.Copy(all, Graphify(server, graph))
	}
	if exe, dir := os.Getenv("ALIDADE_BENCH_GRAPHIFY_EXE"), os.Getenv("ALIDADE_BENCH_GRAPHIFY_DIR"); exe != "" && dir != "" {
		text, err := os.ReadFile(os.Getenv("ALIDADE_BENCH_GRAPHIFY_MD"))
		if err != nil {
			t.Fatalf("ALIDADE_BENCH_GRAPHIFY_MD: %v", err)
		}
		all["graphify_installed"] = GraphifyInstalled(exe, dir, string(text))
	}
	var out []Arm
	for _, name := range strings.Split(env("ALIDADE_BENCH_ARMS", "alidade,none"), ",") {
		a, ok := all[name]
		if !ok {
			t.Fatalf("ALIDADE_BENCH_ARMS: no arm %q", name)
		}
		out = append(out, a)
	}
	return out
}

func selected(t *testing.T, set *Set) []Question {
	only := os.Getenv("ALIDADE_BENCH_ONLY")
	if only == "" {
		return set.Questions
	}
	var out []Question
	for _, q := range set.Questions {
		if strings.Contains(","+only+",", ","+q.ID+",") {
			out = append(out, q)
		}
	}
	if len(out) == 0 {
		t.Fatalf("ALIDADE_BENCH_ONLY=%s names no question of the set", only)
	}
	return out
}

// run asks every question in every arm, runs times, jobs at once. The
// runs come back in question, run, arm order whatever order they
// finished in. A session that could not be read fails the test after
// the others finish: its money is spent, and the rest are still worth
// recording.
func run(t *testing.T, r *Runner, questions []Question, arms []Arm) []*Run {
	n, _ := strconv.Atoi(env("ALIDADE_BENCH_RUNS", "1"))
	jobs, _ := strconv.Atoi(env("ALIDADE_BENCH_JOBS", "2"))
	type job struct {
		i int
		q Question
		a Arm
		n int
	}
	var all []job
	for _, q := range questions {
		for k := 1; k <= n; k++ {
			for _, a := range arms {
				all = append(all, job{len(all), q, a, k})
			}
		}
	}
	results := make([]*Run, len(all))
	work := make(chan job)
	var wg sync.WaitGroup
	for range max(jobs, 1) {
		wg.Go(func() {
			for j := range work {
				res, err := r.Ask(context.Background(), j.a, j.q, j.n)
				if err != nil {
					t.Error(err)
					continue
				}
				// An arm with rules of its own (Bash for one command) has
				// its other Bash calls refused by design.
				if len(res.Denied) > 0 && len(j.a.Allow) == 0 {
					t.Errorf("%s/%s/%d: permissions denied %q: the run did not have its arm's tools", j.q.ID, j.a.Name, j.n, res.Denied)
				}
				t.Logf("%s/%s/%d: correct %v, %d tokens, $%.4f, %s", j.q.ID, j.a.Name, j.n, res.Correct, res.Tokens(), res.CostUSD, res.Duration.Round(time.Second))
				results[j.i] = res
			}
		})
	}
	for _, j := range all {
		work <- j
	}
	close(work)
	wg.Wait()
	var out []*Run
	for _, res := range results {
		if res != nil {
			out = append(out, res)
		}
	}
	return out
}

func version(t *testing.T, bin, arg string) string {
	b, err := exec.Command(bin, arg).Output()
	if err != nil {
		t.Fatalf("%s %s: %v", filepath.Base(bin), arg, err)
	}
	return strings.TrimSpace(string(b))
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// TestRescore scores the answers of a results file, ALIDADE_BENCH_RESCORE,
// again against the set's current references, and writes the result
// beside it as <name>.rescored.pxf. No session runs: it applies a
// corrected reference to answers already paid for.
func TestRescore(t *testing.T) {
	path := os.Getenv("ALIDADE_BENCH_RESCORE")
	if path == "" {
		t.Skip("ALIDADE_BENCH_RESCORE is unset")
	}
	set, err := Load(env("ALIDADE_BENCH_SET", "testdata/protocompile.pxf"))
	if err != nil {
		t.Fatal(err)
	}
	h, runs, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := Rescore(set, runs)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range changed {
		t.Logf("%s/%s/%d: correct now %v (missing %q)", r.Question, r.Arm, r.N, r.Correct, r.Missing)
	}
	out := strings.TrimSuffix(path, ".pxf") + ".rescored.pxf"
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := Write(f, h, runs); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d runs, %d changed, written to %s", len(runs), len(changed), out)
}

// TestReport prints the three-way table (#284) for the results files
// ALIDADE_BENCH_REPORT names, comma-separated: one line per arm and
// question shape. No session runs. Shapes come from every set in
// testdata.
func TestReport(t *testing.T) {
	paths := os.Getenv("ALIDADE_BENCH_REPORT")
	if paths == "" {
		t.Skip("ALIDADE_BENCH_REPORT is unset")
	}
	sets, err := filepath.Glob("testdata/*.pxf")
	if err != nil || len(sets) == 0 {
		t.Fatalf("no question set in testdata: %v", err)
	}
	shape := map[string]string{}
	for _, p := range sets {
		set, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range set.Questions {
			shape[q.ID] = q.Shape
		}
	}
	var runs []*Run
	for _, p := range strings.Split(paths, ",") {
		_, r, err := Read(p)
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, r...)
	}
	lines, err := Table(runs, shape)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d runs from %d files", len(runs), len(strings.Split(paths, ",")))
	for _, l := range lines {
		per := "-"
		if l.Correct > 0 {
			per = fmt.Sprintf("%d tokens, $%.4f", l.Tokens/l.Correct, l.CostUSD/float64(l.Correct))
		}
		t.Logf("%-6s %-9s correct %3d/%-3d  per correct: %s  median %s  p90 %s  total $%.2f", l.Shape, l.Arm, l.Correct, l.Runs, per,
			l.Median.Round(100*time.Millisecond), l.P90.Round(100*time.Millisecond), l.CostUSD)
	}
}
