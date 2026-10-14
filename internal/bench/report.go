// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bench

import (
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// A Header says what produced a results file: the set, the repository's
// commit, the model, and the alidade and Claude Code builds.
type Header struct {
	Repo, Commit, Model, Alidade, Claude string
	Started                              time.Time
}

// A Summary is one question in one arm over its runs. Tokens and cost
// are summed over every run, correct or not; per correct answer they are
// those sums divided by the correct runs, the headline measure (#149),
// so a wrong answer's spend is charged to the right ones. With no
// correct run there is no per-correct figure.
type Summary struct {
	Question, Arm    string
	Runs, Correct    int
	Tokens           int
	CostUSD          float64
	AlidadeCalls     int
	TokensPerCorrect int
	CostPerCorrect   float64
	// Median is the median wall time of its runs (#284).
	Median time.Duration
}

// Summarize folds runs into one summary per question and arm, in the
// order the runs first name them.
func Summarize(runs []*Run) []*Summary {
	var out []*Summary
	index := map[[2]string]*Summary{}
	for _, r := range runs {
		k := [2]string{r.Question, r.Arm}
		s := index[k]
		if s == nil {
			s = &Summary{Question: r.Question, Arm: r.Arm}
			index[k] = s
			out = append(out, s)
		}
		s.Runs++
		if r.Correct {
			s.Correct++
		}
		s.Tokens += r.Tokens()
		s.CostUSD += r.CostUSD
		s.AlidadeCalls += r.AlidadeCalls()
	}
	times := map[*Summary][]time.Duration{}
	for _, r := range runs {
		s := index[[2]string{r.Question, r.Arm}]
		times[s] = append(times[s], r.Duration)
	}
	for _, s := range out {
		s.Median = quantile(times[s], 0.5)
		if s.Correct > 0 {
			s.TokensPerCorrect = s.Tokens / s.Correct
			s.CostPerCorrect = s.CostUSD / float64(s.Correct)
		}
	}
	return out
}

// quantile is the q-th quantile of d by nearest rank: the smallest value
// with at least that share of the values at or below it. Zero for none.
func quantile(d []time.Duration, q float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := slices.Sorted(slices.Values(d))
	return s[max(int(math.Ceil(q*float64(len(s))))-1, 0)]
}

// A Line is one arm over the questions of one shape, or of every shape
// when Shape is "all": what the three-way comparison reports (#284).
// Tokens and cost are summed over every run, as in a Summary.
type Line struct {
	Arm, Shape    string
	Runs, Correct int
	Tokens        int
	CostUSD       float64
	Median, P90   time.Duration
}

// Table folds runs into one line per arm and shape, and one per arm over
// all shapes, sorted by shape then arm. shape gives each question's
// shape; a run of a question it lacks is an error.
func Table(runs []*Run, shape map[string]string) ([]*Line, error) {
	index := map[[2]string]*Line{}
	times := map[*Line][]time.Duration{}
	for _, r := range runs {
		sh, ok := shape[r.Question]
		if !ok {
			return nil, fmt.Errorf("bench: run of %q, which no set has", r.Question)
		}
		for _, k := range [][2]string{{sh, r.Arm}, {"all", r.Arm}} {
			l := index[k]
			if l == nil {
				l = &Line{Shape: k[0], Arm: k[1]}
				index[k] = l
			}
			l.Runs++
			if r.Correct {
				l.Correct++
			}
			l.Tokens += r.Tokens()
			l.CostUSD += r.CostUSD
			times[l] = append(times[l], r.Duration)
		}
	}
	var out []*Line
	for _, k := range slices.SortedFunc(maps.Keys(index), func(a, b [2]string) int { return slices.Compare(a[:], b[:]) }) {
		l := index[k]
		l.Median, l.P90 = quantile(times[l], 0.5), quantile(times[l], 0.9)
		out = append(out, l)
	}
	return out, nil
}

// AlidadeCalls counts the run's calls to alidade's tools.
func (r *Run) AlidadeCalls() int {
	n := 0
	for name, c := range r.Tools {
		if strings.HasPrefix(name, "mcp__alidade__") {
			n += c
		}
	}
	return n
}

// Write writes a results file: the header, a summary per question and
// arm, then every run with its answer.
func Write(w io.Writer, h Header, runs []*Run) error {
	doc := &pxf.Document{LeadingComments: []pxf.Comment{{Text: "# alidade A/B benchmark results (#149)."}}}
	doc.Entries = append(doc.Entries,
		field("repo", h.Repo), field("commit", h.Commit), field("model", h.Model),
		field("alidade", h.Alidade), field("claude", h.Claude),
		field("started", h.Started.UTC().Format(time.RFC3339)))
	for _, s := range Summarize(runs) {
		entries := []pxf.Entry{
			field("question", s.Question), ident("arm", s.Arm),
			integer("runs", s.Runs), integer("correct", s.Correct),
			integer("tokens", s.Tokens), dollars("cost_usd", s.CostUSD),
		}
		if s.Correct > 0 {
			entries = append(entries, integer("tokens_per_correct", s.TokensPerCorrect), dollars("cost_per_correct_usd", s.CostPerCorrect))
		}
		entries = append(entries, integer("alidade_calls", s.AlidadeCalls), integer("duration_ms_median", int(s.Median.Milliseconds())))
		doc.Entries = append(doc.Entries, block("summary", entries...))
	}
	for _, r := range runs {
		doc.Entries = append(doc.Entries, runBlock(r))
	}
	_, err := w.Write(pxf.FormatDocument(doc))
	return err
}

func runBlock(r *Run) pxf.Entry {
	var missing []pxf.Value
	for _, m := range r.Missing {
		missing = append(missing, &pxf.StringVal{Value: m})
	}
	var tools []pxf.Entry
	for _, name := range slices.Sorted(maps.Keys(r.Tools)) {
		tools = append(tools, &pxf.MapEntry{Key: name, Value: &pxf.IntVal{Raw: strconv.Itoa(r.Tools[name])}})
	}
	var mcp []pxf.Entry
	for _, name := range slices.Sorted(maps.Keys(r.MCP)) {
		mcp = append(mcp, &pxf.MapEntry{Key: name, Value: &pxf.StringVal{Value: r.MCP[name]}})
	}
	entries := []pxf.Entry{
		field("question", r.Question), ident("arm", r.Arm), integer("n", r.N),
		&pxf.Assignment{Key: "correct", Value: &pxf.BoolVal{Value: r.Correct}},
	}
	if len(missing) > 0 {
		entries = append(entries, &pxf.Assignment{Key: "missing", Value: &pxf.ListVal{Elements: missing}})
	}
	entries = append(entries,
		integer("input_tokens", r.Input), integer("cache_write_tokens", r.CacheWrite),
		integer("cache_read_tokens", r.CacheRead), integer("output_tokens", r.Output),
		integer("tokens", r.Tokens()), dollars("cost_usd", r.CostUSD),
		integer("turns", r.Turns), integer("duration_ms", int(r.Duration.Milliseconds())))
	if len(tools) > 0 {
		entries = append(entries, &pxf.Block{Name: "tools", Entries: tools})
	}
	if len(mcp) > 0 {
		entries = append(entries, &pxf.Block{Name: "mcp", Entries: mcp})
	}
	if len(r.Requests) > 0 {
		var req []pxf.Value
		for _, n := range r.Requests {
			req = append(req, &pxf.IntVal{Raw: strconv.Itoa(n)})
		}
		entries = append(entries, &pxf.Assignment{Key: "requests", Value: &pxf.ListVal{Elements: req}})
	}
	for _, c := range r.Calls {
		entries = append(entries, block("call", field("tool", c.Tool), field("input", c.Input), integer("bytes", c.Bytes)))
	}
	if len(r.Denied) > 0 {
		var denied []pxf.Value
		for _, d := range r.Denied {
			denied = append(denied, &pxf.StringVal{Value: d})
		}
		entries = append(entries, &pxf.Assignment{Key: "denied", Value: &pxf.ListVal{Elements: denied}})
	}
	entries = append(entries, field("error", r.Error), field("answer", r.Answer))
	return block("run", entries...)
}

func block(name string, entries ...pxf.Entry) pxf.Entry {
	return &pxf.Block{Name: name, Entries: slices.DeleteFunc(entries, func(e pxf.Entry) bool { return e == nil })}
}

func field(key, v string) pxf.Entry {
	if v == "" {
		return nil
	}
	return &pxf.Assignment{Key: key, Value: &pxf.StringVal{Value: v}}
}

func ident(key, v string) pxf.Entry {
	return &pxf.Assignment{Key: key, Value: &pxf.IdentVal{Name: v}}
}

// integer writes every count, zero included: in a results file a zero
// is a measurement, not an absent field.
func integer(key string, v int) pxf.Entry {
	return &pxf.Assignment{Key: key, Value: &pxf.IntVal{Raw: strconv.Itoa(v)}}
}

func dollars(key string, v float64) pxf.Entry {
	return &pxf.Assignment{Key: key, Value: &pxf.FloatVal{Raw: strconv.FormatFloat(v, 'f', 4, 64)}}
}
