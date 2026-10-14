// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package bench is the A/B benchmark (#149): the same questions about a
// real repository, asked of the same model twice, once with alidade's
// MCP tools and once with file reading and search alone, each answer
// scored against a reference written by hand.
//
// The questions and their references live in a PXF file (testdata).
// Each run is one headless Claude Code session, and its tokens, cost,
// turns, wall time and tool calls come from Claude Code's own stream —
// JSON, because that is the edge Claude Code speaks. Results are PXF.
package bench

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// A Set is a question file: the repository and the commit its references
// were written against, and the questions.
type Set struct {
	Repo, Commit string
	// Language is the repository's, "go" when unset: it words the prompt.
	Language  string
	Questions []Question
}

// A Question is one question with its reference: the names a correct
// answer must contain (Want, each "a|b" accepting either), and a note
// saying where each one lives.
type Question struct {
	ID, Shape, Text, Note string
	Want                  []string
}

// Load reads a question file.
func Load(path string) (*Set, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := pxf.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("bench: %s: %w", path, err)
	}
	s := &Set{}
	for _, e := range doc.Entries {
		switch e := e.(type) {
		case *pxf.Assignment:
			s.set(e)
		case *pxf.Block:
			if e.Name != "question" {
				return nil, fmt.Errorf("bench: %s: unknown block %q", path, e.Name)
			}
			q := question(e.Entries)
			if q.ID == "" || q.Text == "" || len(q.Want) == 0 {
				return nil, fmt.Errorf("bench: %s: question %q needs id, text and want", path, q.ID)
			}
			s.Questions = append(s.Questions, q)
		}
	}
	if s.Repo == "" || s.Commit == "" || len(s.Questions) == 0 {
		return nil, fmt.Errorf("bench: %s: needs repo, commit and at least one question", path)
	}
	return s, nil
}

func (s *Set) set(a *pxf.Assignment) {
	switch a.Key {
	case "repo":
		s.Repo = str(a.Value)
	case "commit":
		s.Commit = str(a.Value)
	case "language":
		s.Language = str(a.Value)
	}
}

func question(entries []pxf.Entry) Question {
	var q Question
	for _, e := range entries {
		a, ok := e.(*pxf.Assignment)
		if !ok {
			continue
		}
		switch a.Key {
		case "id":
			q.ID = str(a.Value)
		case "shape":
			q.Shape = str(a.Value)
		case "text":
			q.Text = str(a.Value)
		case "note":
			q.Note = str(a.Value)
		case "want":
			if l, ok := a.Value.(*pxf.ListVal); ok {
				for _, v := range l.Elements {
					q.Want = append(q.Want, str(v))
				}
			}
		}
	}
	return q
}

// str reads a string or a bare identifier.
func str(v pxf.Value) string {
	switch v := v.(type) {
	case *pxf.StringVal:
		return v.Value
	case *pxf.IdentVal:
		return v.Name
	}
	return ""
}

// Score returns the wants an answer lacks: none means it is correct. A
// want matches as a whole word, so "Store" is not found in "Stores" and
// "foo" is found in "App.foo".
func Score(answer string, want []string) (missing []string) {
	for _, w := range want {
		if !found(answer, w) {
			missing = append(missing, w)
		}
	}
	return missing
}

func found(answer, want string) bool {
	for _, alt := range strings.Split(want, "|") {
		re := regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(alt) + `($|[^A-Za-z0-9_])`)
		if re.MatchString(answer) {
			return true
		}
	}
	return false
}

// Prompt is what every arm is asked: the question, with the same
// instructions about scope and naming, and nothing about which tools to
// use. language words the scope: "python", or Go for anything else.
func Prompt(language string, q Question) string {
	repo, tests, name := "Go", "ignore _test.go files", "function, method or type in your answer by its Go name"
	if language == "python" {
		repo, tests, name = "Python", "ignore test files (tests directories, test_*.py, conftest.py)", "function, method or class in your answer by its Python name"
	}
	return "Answer this question about the " + repo + " repository in the current directory. " +
		"Consider production code only: " + tests + ".\n\n" +
		"Question: " + q.Text + "\n\n" +
		"Name every " + name + ", with the file it is declared in. " +
		"Be complete: list every one that qualifies."
}
