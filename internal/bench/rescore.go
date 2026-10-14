// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bench

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// Read reads a results file back: the header and every run. Summaries
// are not read; Summarize derives them again from the runs.
//
// It exists so a corrected reference can be applied to answers already
// paid for (Rescore) rather than by running the sessions again.
func Read(path string) (Header, []*Run, error) {
	var h Header
	b, err := os.ReadFile(path)
	if err != nil {
		return h, nil, err
	}
	doc, err := pxf.Parse(b)
	if err != nil {
		return h, nil, fmt.Errorf("bench: %s: %w", path, err)
	}
	var runs []*Run
	for _, e := range doc.Entries {
		switch e := e.(type) {
		case *pxf.Assignment:
			h.set(e)
		case *pxf.Block:
			if e.Name == "run" {
				runs = append(runs, readRun(e.Entries))
			}
		}
	}
	return h, runs, nil
}

func (h *Header) set(a *pxf.Assignment) {
	v := str(a.Value)
	switch a.Key {
	case "repo":
		h.Repo = v
	case "commit":
		h.Commit = v
	case "model":
		h.Model = v
	case "alidade":
		h.Alidade = v
	case "claude":
		h.Claude = v
	case "started":
		h.Started, _ = time.Parse(time.RFC3339, v)
	}
}

func readRun(entries []pxf.Entry) *Run {
	r := &Run{Tools: map[string]int{}, MCP: map[string]string{}}
	for _, e := range entries {
		switch e := e.(type) {
		case *pxf.Assignment:
			r.assign(e)
		case *pxf.Block:
			if e.Name == "call" {
				r.Calls = append(r.Calls, readCall(e.Entries))
				continue
			}
			for _, me := range e.Entries {
				m, ok := me.(*pxf.MapEntry)
				if !ok {
					continue
				}
				switch e.Name {
				case "tools":
					r.Tools[m.Key] = num(m.Value)
				case "mcp":
					r.MCP[m.Key] = str(m.Value)
				}
			}
		}
	}
	return r
}

func readCall(entries []pxf.Entry) Call {
	var c Call
	for _, e := range entries {
		if a, ok := e.(*pxf.Assignment); ok {
			switch a.Key {
			case "tool":
				c.Tool = str(a.Value)
			case "input":
				c.Input = str(a.Value)
			case "bytes":
				c.Bytes = num(a.Value)
			}
		}
	}
	return c
}

func (r *Run) assign(a *pxf.Assignment) {
	ints := map[string]*int{"n": &r.N, "input_tokens": &r.Input, "cache_write_tokens": &r.CacheWrite,
		"cache_read_tokens": &r.CacheRead, "output_tokens": &r.Output, "turns": &r.Turns}
	if p, ok := ints[a.Key]; ok {
		*p = num(a.Value)
		return
	}
	switch a.Key {
	case "question":
		r.Question = str(a.Value)
	case "arm":
		r.Arm = str(a.Value)
	case "correct":
		if bv, ok := a.Value.(*pxf.BoolVal); ok {
			r.Correct = bv.Value
		}
	case "missing", "denied", "requests":
		r.list(a)
	case "cost_usd":
		if f, ok := a.Value.(*pxf.FloatVal); ok {
			r.CostUSD, _ = strconv.ParseFloat(f.Raw, 64)
		}
	case "duration_ms":
		r.Duration = time.Duration(num(a.Value)) * time.Millisecond
	case "error":
		r.Error = str(a.Value)
	case "answer":
		r.Answer = str(a.Value)
	}
}

// list reads the run's list-valued fields.
func (r *Run) list(a *pxf.Assignment) {
	switch a.Key {
	case "missing":
		r.Missing = strs(a.Value)
	case "denied":
		r.Denied = strs(a.Value)
	case "requests":
		if l, ok := a.Value.(*pxf.ListVal); ok {
			for _, v := range l.Elements {
				r.Requests = append(r.Requests, num(v))
			}
		}
	}
}

func num(v pxf.Value) int {
	if i, ok := v.(*pxf.IntVal); ok {
		n, _ := strconv.Atoi(i.Raw)
		return n
	}
	return 0
}

func strs(v pxf.Value) []string {
	l, ok := v.(*pxf.ListVal)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range l.Elements {
		out = append(out, str(e))
	}
	return out
}

// Rescore scores every run's answer again against the set's current
// references, and returns the runs whose verdict changed. A run of a
// question the set no longer has is an error: its verdict could not be
// checked.
func Rescore(set *Set, runs []*Run) (changed []*Run, err error) {
	byID := map[string]Question{}
	for _, q := range set.Questions {
		byID[q.ID] = q
	}
	for _, r := range runs {
		q, ok := byID[r.Question]
		if !ok {
			return nil, fmt.Errorf("bench: run of %q, which the set does not have", r.Question)
		}
		was := r.Correct
		r.Missing = Score(r.Answer, q.Want)
		r.Correct = r.Error == "" && len(r.Denied) == 0 && len(r.Missing) == 0
		if r.Correct != was {
			changed = append(changed, r)
		}
	}
	return changed, nil
}
