// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bench

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/belluikit/alidade/internal/oracle"
)

// Generate takes each stratum's candidates by rule: a unique name, a
// method name five declarations share, a chain one shortest path joins;
// and passes over a target with one caller, with nine, or whose answer
// is in its question.
func TestGenerate(t *testing.T) {
	s := Generate("m", strings.Repeat("c", 40), genCalls(), 3)
	got := map[string][]string{}
	for _, q := range s.Questions {
		got[q.Shape+" "+q.Text] = q.Want
	}
	want := map[string][]string{
		"unique In m, which functions and methods call the function p.Lone directly?":                                {"A", "B"},
		"shared In m, which functions and methods call the Close method of p.T1 directly?":                           {"Other", "UserT1"},
		"chain In m, how does q.Start end up calling q.End? Name each function or method in the chain between them.": {"Mid1", "Mid2"},
		// one caller, nine callers, the answer in the question, one caller of a shared name
		"unique In m, which functions and methods call the function p.Solo directly?":      nil,
		"unique In m, which functions and methods call the function p.Crowd directly?":     nil,
		"unique In m, which functions and methods call the function r.Echo directly?":      nil,
		"shared In m, which functions and methods call the Close method of p.T2 directly?": nil,
	}
	for text, w := range want {
		if g, ok := got[text]; ok != (w != nil) || !slices.Equal(g, w) {
			t.Errorf("%s: want %v, got %v (present %v)", text, w, g, ok)
		}
	}
	if len(got) != 3 {
		t.Errorf("%d questions, want 3: %v", len(got), got)
	}
}

// genCalls is a call graph with one candidate of each stratum and one of
// each exclusion.
func genCalls() []Edge {
	var calls []Edge
	add := func(from, to string) { calls = append(calls, Edge{"go://m/" + from, "go://m/" + to}) }
	// Lone has two callers and a name of its own.
	add("p#A", "p#Lone")
	add("p#B", "p#Lone")
	// Solo has one caller; Crowd has nine.
	add("p#A", "p#Solo")
	for _, c := range []string{"C1", "C2", "C3", "C4", "C5", "C6", "C7", "C8", "C9"} {
		add("p#"+c, "p#Crowd")
	}
	// Five types declare Close; T1's has two callers.
	for _, typ := range []string{"T1", "T2", "T3", "T4", "T5"} {
		add("p#User"+typ, "p#"+typ+".Close")
	}
	add("p#Other", "p#T1.Close")
	// Start -> Mid1 -> Mid2 -> End, and no other way.
	add("q#Start", "q#Mid1")
	add("q#Mid1", "q#Mid2")
	add("q#Mid2", "q#End")
	// Echo's only callers are named Echo too: its answer is in its question.
	add("r#X.Echo", "r#Echo")
	add("r#Y.Echo", "r#Echo")
	return calls
}

// A generated set reads back as it was written.
func TestMarshalReadsBack(t *testing.T) {
	s := Generate("m", strings.Repeat("c", 40), genCalls(), 3)
	path := filepath.Join(t.TempDir(), "m.pxf")
	if err := os.WriteFile(path, s.Marshal("A header\n\nof two paragraphs."), 0o644); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Repo != s.Repo || back.Commit != s.Commit || len(back.Questions) != len(s.Questions) {
		t.Fatalf("read back %+v", back)
	}
	for i, q := range back.Questions {
		if w := s.Questions[i]; q.ID != w.ID || q.Shape != w.Shape || q.Text != w.Text || q.Note != w.Note || !slices.Equal(q.Want, w.Want) {
			t.Errorf("question %d: %+v, want %+v", i, q, w)
		}
	}
}

// The prompt of a Go set is the one every earlier run was asked, word
// for word: results stay comparable across runs.
func TestPromptGoUnchanged(t *testing.T) {
	want := "Answer this question about the Go repository in the current directory. " +
		"Consider production code only: ignore _test.go files.\n\n" +
		"Question: Q?\n\n" +
		"Name every function, method or type in your answer by its Go name, with the file it is declared in. " +
		"Be complete: list every one that qualifies."
	if got := Prompt("", Question{Text: "Q?"}); got != want {
		t.Errorf("prompt:\n%s", got)
	}
	if got := Prompt("python", Question{Text: "Q?"}); !strings.Contains(got, "Python repository") || strings.Contains(got, "Go") {
		t.Errorf("python prompt:\n%s", got)
	}
}

// TestGenerateSet writes a question set for the Go repository
// ALIDADE_BENCH_GEN names, to ALIDADE_BENCH_GEN_OUT, from the type
// checker's static calls (internal/oracle): ALIDADE_BENCH_GEN_PER
// questions of each stratum (5).
func TestGenerateSet(t *testing.T) {
	root := os.Getenv("ALIDADE_BENCH_GEN")
	if root == "" {
		t.Skip("ALIDADE_BENCH_GEN is unset")
	}
	head, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	calls, err := oracle.GoCalls(graph.NewMinter(root), root)
	if err != nil {
		t.Fatal(err)
	}
	var edges []Edge
	for _, c := range calls.Calls {
		if c.Kind == oracle.Static {
			edges = append(edges, Edge{string(c.From), string(c.To)})
		}
	}
	if len(edges) == 0 {
		t.Fatalf("no static call in %s (%d packages, failed %v)", root, calls.Packages, calls.Failed)
	}
	per := 5
	repo := filepath.Base(root)
	s := Generate(repo, strings.TrimSpace(string(head)), edges, per)
	header := "Questions over " + repo + " drawn by rule, not picked (#284): bench.Generate over the\n" +
		"type checker's static calls (internal/oracle), without tests, at the commit\n" +
		"below. Strata: unique (callers of a name no other declaration shares),\n" +
		"shared (callers of a method name five or more declarations share), chain\n" +
		"(the functions between two declarations three calls apart). Candidates are\n" +
		"taken in order of the SHA-256 of \"repo|stratum|id\"."
	if err := os.WriteFile(os.Getenv("ALIDADE_BENCH_GEN_OUT"), s.Marshal(header), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %d packages, %d static calls, %d failed packages, %d questions", repo, calls.Packages, len(edges), len(calls.Failed), len(s.Questions))
}
