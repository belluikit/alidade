// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bench

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// The committed question file loads, and every question in it carries
// what scoring needs.
func TestLoadSet(t *testing.T) {
	s, err := Load("testdata/protocompile.pxf")
	if err != nil {
		t.Fatal(err)
	}
	if s.Repo != "protocompile" || len(s.Commit) != 40 {
		t.Errorf("repo %q, commit %q", s.Repo, s.Commit)
	}
	ids := map[string]bool{}
	for _, q := range s.Questions {
		if ids[q.ID] {
			t.Errorf("question %q twice", q.ID)
		}
		ids[q.ID] = true
		if q.Shape == "" || q.Note == "" {
			t.Errorf("%s: shape %q, note %q", q.ID, q.Shape, q.Note)
		}
		// A want the question already spells is satisfied by an answer
		// that repeats the question.
		for _, w := range q.Want {
			for _, alt := range strings.Split(w, "|") {
				if found(q.Text, alt) {
					t.Errorf("%s: want %q is in the question's own text", q.ID, alt)
				}
			}
		}
	}
	if len(s.Questions) < 2 {
		t.Errorf("%d questions", len(s.Questions))
	}
}

func TestScore(t *testing.T) {
	for _, c := range []struct {
		answer  string
		want    []string
		missing []string
	}{
		{"`App.suggestedVersionLine` in slash.go", []string{"suggestedVersionLine"}, nil},
		{"SuggestNextSemver", []string{"SuggestNextSemver", "suggestedVersionLine"}, []string{"suggestedVersionLine"}},
		// whole words: a longer name is not the name.
		{"the Stores type", []string{"Store"}, []string{"Store"}},
		{"createSessionLocked", []string{"createSession"}, []string{"createSession"}},
		// alternatives
		{"defined in internal/judge/internal/ledger/store.go", []string{"ledger.Store|ledger/store.go"}, nil},
		{"`ledger.Store`", []string{"ledger.Store|ledger/store.go"}, nil},
		{"memstore.Store", []string{"ledger.Store|ledger/store.go"}, []string{"ledger.Store|ledger/store.go"}},
	} {
		if got := Score(c.answer, c.want); !slices.Equal(got, c.missing) {
			t.Errorf("Score(%q, %q) = %q, want %q", c.answer, c.want, got, c.missing)
		}
	}
}

// A stream as Claude Code writes it: the init line, assistant messages
// with tool calls, and the result line.
const stream = `{"type":"system","subtype":"init","mcp_servers":[{"name":"alidade","status":"connected"}]}
{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":2,"cache_creation_input_tokens":40,"cache_read_input_tokens":500},"content":[{"type":"tool_use","id":"t1","name":"mcp__alidade__find_nodes","input":{"query":"x"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"0123456789"}]}}
{"type":"assistant","message":{"id":"m2","usage":{"input_tokens":2,"cache_creation_input_tokens":60,"cache_read_input_tokens":500},"content":[{"type":"text"},{"type":"tool_use","id":"t2","name":"Read"},{"type":"tool_use","id":"t3","name":"mcp__alidade__neighbors"}]}}
{"type":"assistant","message":{"id":"m2","usage":{"input_tokens":2,"cache_creation_input_tokens":60,"cache_read_input_tokens":500},"content":[{"type":"text"}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"abc"},{"type":"text","text":"de"}]}]}}
{"type":"result","subtype":"success","is_error":false,"result":"SuggestNextSemver and App.suggestedVersionLine","total_cost_usd":0.25,"num_turns":3,"usage":{"input_tokens":4,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":50},"modelUsage":{"m":{"inputTokens":4,"cacheCreationInputTokens":100,"cacheReadInputTokens":1000,"outputTokens":50}}}
`

func TestRead(t *testing.T) {
	r := &Run{Tools: map[string]int{}, MCP: map[string]string{}}
	if err := r.read([]byte(stream)); err != nil {
		t.Fatal(err)
	}
	if r.Tokens() != 1154 || r.CostUSD != 0.25 || r.Turns != 3 || r.Error != "" {
		t.Errorf("tokens %d, cost %v, turns %d, error %q", r.Tokens(), r.CostUSD, r.Turns, r.Error)
	}
	if r.AlidadeCalls() != 2 || r.Tools["Read"] != 1 || r.MCP["alidade"] != "connected" {
		t.Errorf("tools %v, mcp %v", r.Tools, r.MCP)
	}
	// Each call with its result's size, in order; neighbors got no
	// result in this stream.
	want := []Call{{"mcp__alidade__find_nodes", `{"query":"x"}`, 10}, {"Read", "", 5}, {"mcp__alidade__neighbors", "", -1}}
	if !slices.Equal(r.Calls, want) {
		t.Errorf("calls %v, want %v", r.Calls, want)
	}
	// One request per assistant message id, counted once however many
	// stream lines carry it; the duplicate m2 line below is the same request.
	if !slices.Equal(r.Requests, []int{542, 562}) {
		t.Errorf("requests %v", r.Requests)
	}
}

func TestReadRefuses(t *testing.T) {
	for name, s := range map[string]string{
		"no result line":  strings.Join(strings.Split(stream, "\n")[:4], "\n"),
		"counts disagree": strings.Replace(stream, `"outputTokens":50`, `"outputTokens":51`, 1),
	} {
		r := &Run{Tools: map[string]int{}, MCP: map[string]string{}}
		if err := r.read([]byte(s)); err == nil {
			t.Errorf("%s: read accepted it", name)
		}
	}
}

// A session that did not finish is an error and never correct, whatever
// its partial answer says.
func TestReadDenied(t *testing.T) {
	s := strings.Replace(stream, `"num_turns":3,`, `"num_turns":3,"permission_denials":[{"tool_name":"mcp__alidade__find_nodes","tool_use_id":"x","tool_input":{}}],`, 1)
	r := &Run{Tools: map[string]int{}, MCP: map[string]string{}}
	if err := r.read([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Denied, []string{"mcp__alidade__find_nodes"}) {
		t.Errorf("denied %q", r.Denied)
	}
}

func TestReadError(t *testing.T) {
	s := strings.Replace(stream, `"subtype":"success","is_error":false`, `"subtype":"error_max_budget_usd","is_error":true`, 1)
	r := &Run{Tools: map[string]int{}, MCP: map[string]string{}}
	if err := r.read([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if r.Error != "error_max_budget_usd" {
		t.Errorf("error %q", r.Error)
	}
}

func TestSummarize(t *testing.T) {
	runs := []*Run{
		{Question: "q", Arm: "alidade", Correct: true, Input: 100, CostUSD: 1},
		{Question: "q", Arm: "alidade", Correct: false, Input: 200, CostUSD: 2},
		{Question: "q", Arm: "none", Correct: false, Input: 50},
	}
	s := Summarize(runs)
	if len(s) != 2 {
		t.Fatalf("%d summaries", len(s))
	}
	// The wrong run's spend is charged to the right one.
	if a := s[0]; a.Runs != 2 || a.Correct != 1 || a.TokensPerCorrect != 300 || a.CostPerCorrect != 3 {
		t.Errorf("alidade: %+v", a)
	}
	if n := s[1]; n.Correct != 0 || n.TokensPerCorrect != 0 {
		t.Errorf("none: %+v", n)
	}
}

// The results file is PXF that parses back, with a summary per question
// and arm and a run per run, and no per-correct figure where nothing was
// correct.
func TestWrite(t *testing.T) {
	runs := []*Run{
		{Question: "q", Arm: "alidade", N: 1, Correct: true, Input: 10, Tools: map[string]int{"Read": 1}, MCP: map[string]string{"alidade": "connected"}, Answer: "a \"quoted\"\nanswer", Duration: time.Second},
		{Question: "q", Arm: "none", N: 1, Missing: []string{"x"}, Answer: "b"},
	}
	var buf bytes.Buffer
	if err := Write(&buf, Header{Repo: "steward", Commit: "c", Model: "m", Started: time.Unix(0, 0)}, runs); err != nil {
		t.Fatal(err)
	}
	doc, err := pxf.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	counts := map[string]int{}
	for _, e := range doc.Entries {
		if b, ok := e.(*pxf.Block); ok {
			counts[b.Name]++
		}
	}
	if counts["summary"] != 2 || counts["run"] != 2 {
		t.Errorf("blocks %v", counts)
	}
	if got := strings.Count(buf.String(), "tokens_per_correct"); got != 1 {
		t.Errorf("tokens_per_correct %d times, want once:\n%s", got, buf.String())
	}
}

// A results file reads back to the runs that were written, and a
// rescore against a changed reference flips exactly the runs it should.
func TestReadRescore(t *testing.T) {
	runs := []*Run{
		{Question: "q", Arm: "alidade", N: 2, Correct: false, Missing: []string{"web/web.go"}, Input: 3, CacheWrite: 4, CacheRead: 5, Output: 6,
			CostUSD: 0.0123, Turns: 7, Duration: 1500 * time.Millisecond, Tools: map[string]int{"Read": 2}, Calls: []Call{{"Read", `{"file_path":"a \"b\""}`, 120}, {"Read", "", -1}}, Requests: []int{900, 1400}, MCP: map[string]string{"alidade": "connected"}, Answer: "`Run` in `web.go`"},
		{Question: "q", Arm: "none", N: 1, Correct: true, Answer: "nothing", Tools: map[string]int{}, MCP: map[string]string{}, Denied: []string{"x"}},
	}
	path := t.TempDir() + "/r.pxf"
	var buf bytes.Buffer
	h := Header{Repo: "steward", Commit: "c", Model: "m", Alidade: "a", Claude: "cl", Started: time.Unix(100, 0).UTC()}
	if err := Write(&buf, h, runs); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	gh, got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if gh != h {
		t.Errorf("header %+v, want %+v", gh, h)
	}
	if !reflect.DeepEqual(got, runs) {
		t.Errorf("runs differ:\n got %+v\nwant %+v", got[0], runs[0])
	}
	changed, err := Rescore(&Set{Questions: []Question{{ID: "q", Want: []string{"web.go"}}}}, got)
	if err != nil {
		t.Fatal(err)
	}
	// The first now passes; the second had a denial and cannot.
	if len(changed) != 2 || !got[0].Correct || got[1].Correct {
		t.Errorf("changed %d, correct %v %v", len(changed), got[0].Correct, got[1].Correct)
	}
	if _, err := Rescore(&Set{}, got); err == nil {
		t.Error("rescore against a set without the question passed")
	}
}

// Table gives one line per arm and shape and one over all shapes, with
// the median and the 90th percentile by nearest rank (#284).
func TestTable(t *testing.T) {
	var runs []*Run
	for i := 1; i <= 10; i++ {
		runs = append(runs, &Run{Question: "q1", Arm: "only", Correct: i > 2, Input: 100, CostUSD: 0.5, Duration: time.Duration(i) * time.Second})
	}
	runs = append(runs, &Run{Question: "q2", Arm: "only", Correct: true, Input: 7, CostUSD: 1, Duration: time.Minute})
	lines, err := Table(runs, map[string]string{"q1": "graph", "q2": "doc"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Line{
		{Arm: "only", Shape: "all", Runs: 11, Correct: 9, Tokens: 1007, CostUSD: 6, Median: 6 * time.Second, P90: 10 * time.Second},
		{Arm: "only", Shape: "doc", Runs: 1, Correct: 1, Tokens: 7, CostUSD: 1, Median: time.Minute, P90: time.Minute},
		{Arm: "only", Shape: "graph", Runs: 10, Correct: 8, Tokens: 1000, CostUSD: 5, Median: 5 * time.Second, P90: 9 * time.Second},
	}
	if len(lines) != len(want) {
		t.Fatalf("%d lines, want %d", len(lines), len(want))
	}
	for i, l := range lines {
		if *l != want[i] {
			t.Errorf("line %d: %+v, want %+v", i, *l, want[i])
		}
	}
	if _, err := Table(runs, map[string]string{"q1": "graph"}); err == nil {
		t.Error("a run of a question no set has: no error")
	}
}

// Each graphify arm is allowed its own server's tools and no other.
func TestGraphifyArm(t *testing.T) {
	for name, tools := range map[string]string{"graphify": "Read", "graphify_grep": Files} {
		a := Graphify("/bin/graphify-mcp", "/g/graph.json")[name]
		if a.Name != name || a.allowed() != "mcp__graphify" || a.Tools != tools {
			t.Errorf("%s: name %q, allowed %q, tools %q", name, a.Name, a.allowed(), a.Tools)
		}
	}
	if got := Arms("alidade", "/r")["only"].allowed(); got != "mcp__alidade" {
		t.Errorf("only: allowed %q", got)
	}
}

// An arm's name is written to a results file as a bare identifier, so
// every arm's name must read back: a hyphen in one made a file of paid
// sessions unreadable (#284).
func TestArmNamesReadBack(t *testing.T) {
	arms := Arms("alidade", "/r")
	maps.Copy(arms, Graphify("graphify-mcp", "/g/graph.json"))
	arms["graphify_installed"] = GraphifyInstalled("/v/bin/graphify", "/copy", "rules")
	if a := arms["graphify_installed"]; a.allowed() != "Bash(graphify:*)" || a.Path != "/v/bin" || a.Dir != "/copy" {
		t.Errorf("graphify_installed: %+v", a)
	}
	for name := range arms {
		path := filepath.Join(t.TempDir(), "r.pxf")
		var buf bytes.Buffer
		if err := Write(&buf, Header{Repo: "r", Commit: "c", Model: "m"}, []*Run{{Question: "q", Arm: name, N: 1}}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		_, runs, err := Read(path)
		if err != nil || len(runs) != 1 || runs[0].Arm != name {
			t.Errorf("arm %q: read back %+v, %v", name, runs, err)
		}
	}
}

// The generated sets (#284) load, and no two questions of testdata share
// an id: TestReport reads shapes by id across every set.
func TestGeneratedSetsLoad(t *testing.T) {
	paths, _ := filepath.Glob("testdata/*.pxf")
	ids := map[string]string{}
	gen := 0
	for _, p := range paths {
		s, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(filepath.Base(p), "gen-") {
			gen++
		}
		for _, q := range s.Questions {
			if was, ok := ids[q.ID]; ok {
				t.Errorf("question %q in %s and %s", q.ID, was, p)
			}
			ids[q.ID] = p
			if !answerable(q) {
				t.Errorf("%s: %s: a want is in the question's text", p, q.ID)
			}
		}
	}
	if gen != 4 {
		t.Errorf("%d generated sets, want 4", gen)
	}
}
