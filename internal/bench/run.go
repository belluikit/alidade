// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// An Arm is one side of the comparison: a name, the MCP servers its
// sessions get, its built-in tools, and any line appended to the system
// prompt.
type Arm struct {
	Name string
	// Servers is the mcpServers object of Claude Code's --mcp-config,
	// empty for the arm without alidade.
	Servers map[string]any
	Tools   string
	System  string
	// Dir is where the arm's sessions run, when not the runner's
	// repository: a copy of it holding what the arm's tool keeps there.
	Dir string
	// Allow are permission rules beyond the arm's MCP servers, such as
	// "Bash(graphify:*)"; Settings is a Claude Code settings object the
	// sessions get (its hooks run even in restricted mode); Path is a
	// directory put first on the sessions' PATH.
	Allow    []string
	Settings map[string]any
	Path     string
}

// Files are the built-in tools every arm has: reading and searching
// files.
const Files = "Read,Grep,Glob"

// Arms returns the arms for a repository, alidade served from bin over
// root:
//
//   - alidade: as an ordinary session has it. Claude Code defers MCP
//     tools' schemas behind ToolSearch, which lists them by name only, so
//     the model must choose to load them.
//   - loaded: the schemas in context from the start (no ToolSearch), at
//     a fixed cost of about 5,400 tokens a session.
//   - told: as alidade, plus one system-prompt line saying the repository
//     is indexed — what a user would write in CLAUDE.md.
//   - only: alidade and Read, no Grep: what alidade answers when it
//     is the way to find things, the ceiling the other arms are read
//     against.
//   - none: file reading and search alone.
//
// The pilot found the model never loaded alidade's deferred tools on its
// own (#149), which is why loaded, told and only exist.
func Arms(bin, root string) map[string]Arm {
	served := map[string]any{"alidade": map[string]any{"command": bin, "args": []string{"serve", "-root", root}}}
	return map[string]Arm{
		"alidade": {Name: "alidade", Servers: served, Tools: Files + ",ToolSearch"},
		"loaded":  {Name: "loaded", Servers: served, Tools: Files},
		"told": {Name: "told", Servers: served, Tools: Files + ",ToolSearch",
			System: "This repository is indexed by the alidade MCP server: a graph of its declarations, calls, implementations and schema-to-code links. Prefer its tools for questions about how code relates."},
		"only": {Name: "only", Servers: served, Tools: "Read"},
		"none": {Name: "none", Servers: map[string]any{}, Tools: Files},
	}
}

// Graphify returns the arms of the three-way comparison (#284), over
// graphify's MCP server (the graphify-mcp binary) and a graph.json built
// beforehand:
//
//   - graphify: the server and Read, no Grep: what only is for alidade.
//   - graphify_grep: the server with Read, Grep and Glob. graphify has no
//     text search of its own, where alidade has search_text.
//
// The graph is built outside the session, so its build time is not in a
// run's duration, where alidade's first build is.
func Graphify(server, graph string) map[string]Arm {
	served := map[string]any{"graphify": map[string]any{"command": server, "args": []string{graph}}}
	return map[string]Arm{
		"graphify":      {Name: "graphify", Servers: served, Tools: "Read"},
		"graphify_grep": {Name: "graphify_grep", Servers: served, Tools: Files},
	}
}

// GraphifyInstalled is graphify as its installer sets it up for Claude
// Code (graphify 0.9.51, `graphify install`), in dir, a copy of the
// repository holding graphify-out: instructions is the section it writes
// to CLAUDE.md, given as system prompt; its two PreToolUse hooks, which
// tell the model to query the graph before it searches or reads; and
// its command-line tool through Bash, the session's only Bash use. It
// registers no MCP server. exe is the graphify binary.
func GraphifyInstalled(exe, dir, instructions string) Arm {
	hook := func(matcher, kind string) map[string]any {
		return map[string]any{"matcher": matcher, "hooks": []any{map[string]any{"type": "command", "command": exe + " hook-guard " + kind}}}
	}
	return Arm{Name: "graphify_installed", Servers: map[string]any{}, Tools: Files + ",Bash", System: instructions,
		Dir: dir, Allow: []string{"Bash(graphify:*)"}, Path: filepath.Dir(exe),
		Settings: map[string]any{"hooks": map[string]any{"PreToolUse": []any{hook("Bash|Grep", "search"), hook("Read|Glob", "read")}}}}
}

// A Runner runs sessions in Repo with Claude (the claude binary) on
// Model, each capped at Budget dollars.
type Runner struct {
	// Language is the question set's, for the prompt.
	Language            string
	Repo, Claude, Model string
	Budget              float64
	// Scratch holds each arm's MCP configuration.
	Scratch string
}

// A Run is one session's measures.
type Run struct {
	Question, Arm string
	N             int
	// Missing are the wants the answer lacks; Correct is none missing
	// and no error.
	Missing []string
	Correct bool
	// Input, CacheWrite, CacheRead and Output are the session's tokens,
	// summed over its API calls.
	Input, CacheWrite, CacheRead, Output int
	CostUSD                              float64
	Turns                                int
	Duration                             time.Duration
	// Tools counts tool calls by tool name, as the model made them: a
	// call to a tool its arm lacks is counted, and fails. MCP is each
	// MCP server's status at session start: "connected" is the evidence
	// an arm had the tools it is named for.
	Tools map[string]int
	MCP   map[string]string
	// Calls are the session's tool calls in order, each with the size
	// of what came back: what a tool adds to the context the model then
	// carries for the rest of the session (#157).
	Calls []Call
	// Requests is the context of each API request the session made, in
	// tokens (input, cache write and cache read together), one per
	// assistant message id. num_turns is not this count (#157).
	Requests []int
	// Denied names the tool calls the session's permissions refused. A
	// run with any is not a measurement of its arm.
	Denied []string
	// Error is a session that did not finish: over budget, an API error.
	Error  string
	Answer string
}

// Tokens is the session's total tokens, cached or not.
func (r *Run) Tokens() int { return r.Input + r.CacheWrite + r.CacheRead + r.Output }

// Ask runs one session of q in arm and scores it.
func (r *Runner) Ask(ctx context.Context, arm Arm, q Question, n int) (*Run, error) {
	cfg, err := r.config(arm)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, r.Claude, "-p", Prompt(r.Language, q),
		"--output-format", "stream-json", "--verbose",
		"--model", r.Model,
		"--restricted", "--tools", arm.Tools,
		"--strict-mcp-config", "--mcp-config", cfg,
		// A headless session has no one to approve a tool call, and an MCP
		// tool is not approved by default: without this every alidade call
		// is denied (the pilot's first alidade-only runs, #149).
		"--allowedTools", arm.allowed(),
		"--max-budget-usd", fmt.Sprint(r.Budget),
		"--no-session-persistence")
	if arm.System != "" {
		cmd.Args = append(cmd.Args, "--append-system-prompt", arm.System)
	}
	if arm.Settings != nil {
		b, err := json.Marshal(arm.Settings)
		if err != nil {
			return nil, err
		}
		cmd.Args = append(cmd.Args, "--settings", string(b))
	}
	cmd.Dir = r.Repo
	if arm.Dir != "" {
		cmd.Dir = arm.Dir
	}
	// --restricted already ignores the user's settings files, CLAUDE.md
	// and auto-memory (measured: the same context with and without
	// these); they are set as well so a change to that default cannot
	// leak a person's notes about the repository into one arm's context.
	cmd.Env = append(os.Environ(), "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1", "CLAUDE_CODE_DISABLE_CLAUDE_MDS=1")
	if arm.Path != "" {
		cmd.Env = append(cmd.Env, "PATH="+arm.Path+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	out, err := cmd.Output()
	run := &Run{Question: q.ID, Arm: arm.Name, N: n, Duration: time.Since(start), Tools: map[string]int{}, MCP: map[string]string{}}
	if perr := run.read(out); perr != nil {
		return nil, fmt.Errorf("bench: %s/%s: %v (exit: %v; stderr: %s)", q.ID, arm.Name, perr, err, bytes.TrimSpace(stderr.Bytes()))
	}
	run.Missing = Score(run.Answer, q.Want)
	run.Correct = run.Error == "" && len(run.Missing) == 0
	return run, nil
}

// allowed is what the arm's sessions may use without asking: its own
// rules, and its MCP servers, each as the prefix of its tools.
func (a Arm) allowed() string {
	out := slices.Clone(a.Allow)
	for _, name := range slices.Sorted(maps.Keys(a.Servers)) {
		out = append(out, "mcp__"+name)
	}
	return strings.Join(out, ",")
}

func (r *Runner) config(arm Arm) (string, error) {
	b, err := json.Marshal(map[string]any{"mcpServers": arm.Servers})
	if err != nil {
		return "", err
	}
	p := filepath.Join(r.Scratch, "mcp-"+arm.Name+".json")
	return p, os.WriteFile(p, b, 0o644)
}

// A Call is one tool call: the tool, its input as the model wrote it
// (JSON, the tool protocol's own form), and the bytes of its result's
// text, or -1 when the stream carried no result for it.
type Call struct {
	Tool, Input string
	Bytes       int
}

// event is the part of Claude Code's stream-json lines a run reads.
type event struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype"`
	MCPServers []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"mcp_servers"`
	Message struct {
		ID      string `json:"id"`
		Usage   usage  `json:"usage"`
		Content []struct {
			Type      string          `json:"type"`
			Name      string          `json:"name"`
			ID        string          `json:"id"`
			ToolUseID string          `json:"tool_use_id"`
			Input     json.RawMessage `json:"input"`
			Content   json.RawMessage `json:"content"`
		} `json:"content"`
	} `json:"message"`
	Result     string            `json:"result"`
	IsError    bool              `json:"is_error"`
	Cost       float64           `json:"total_cost_usd"`
	Turns      int               `json:"num_turns"`
	Usage      usage             `json:"usage"`
	ModelUsage map[string]mUsage `json:"modelUsage"`
	Errors     []json.RawMessage `json:"errors"`
	Denials    []struct {
		Tool string `json:"tool_name"`
	} `json:"permission_denials"`
}

type usage struct {
	Input      int `json:"input_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	Output     int `json:"output_tokens"`
}

type mUsage struct {
	Input      int `json:"inputTokens"`
	CacheWrite int `json:"cacheCreationInputTokens"`
	CacheRead  int `json:"cacheReadInputTokens"`
	Output     int `json:"outputTokens"`
}

// read takes a session's stream: the MCP servers from its init line, the
// tool calls from its assistant messages, and the measures from its
// result line. A stream with no result line is an error, not a run.
func (run *Run) read(out []byte) error {
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	seen := false
	pending := map[string]int{} // a tool call's id → its index in Calls
	requests := map[string]bool{}
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch e.Type {
		case "system":
			for _, s := range e.MCPServers {
				run.MCP[s.Name] = s.Status
			}
		case "assistant", "user":
			run.message(&e, pending)
			if e.Type == "assistant" && e.Message.ID != "" && !requests[e.Message.ID] {
				requests[e.Message.ID] = true
				u := e.Message.Usage
				run.Requests = append(run.Requests, u.Input+u.CacheWrite+u.CacheRead)
			}
		case "result":
			seen = true
			if err := run.result(&e); err != nil {
				return err
			}
		}
	}
	if !seen {
		return fmt.Errorf("no result line in %d bytes of output", len(out))
	}
	return sc.Err()
}

// message takes an assistant message's tool calls and a user message's
// tool results, matched by the call's id.
func (run *Run) message(e *event, pending map[string]int) {
	for _, c := range e.Message.Content {
		switch c.Type {
		case "tool_use":
			run.Tools[c.Name]++
			pending[c.ID] = len(run.Calls)
			run.Calls = append(run.Calls, Call{Tool: c.Name, Input: string(c.Input), Bytes: -1})
		case "tool_result":
			if i, ok := pending[c.ToolUseID]; ok {
				run.Calls[i].Bytes = resultBytes(c.Content)
			}
		}
	}
}

// resultBytes measures a tool result's content: a string, or a list of
// parts whose text is counted (any other part by its encoded size).
func resultBytes(raw json.RawMessage) int {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return len(s)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return len(raw)
	}
	n := 0
	for _, p := range parts {
		if p.Type == "text" {
			n += len(p.Text)
		} else {
			n += len(raw) / len(parts)
		}
	}
	return n
}

// result reads the result line, and checks its token counts against the
// per-model ones: two counts of the same session that disagree mean the
// stream changed shape, and a number from it would not be evidence.
func (run *Run) result(e *event) error {
	run.Answer, run.CostUSD, run.Turns = e.Result, e.Cost, e.Turns
	run.Input, run.CacheWrite, run.CacheRead, run.Output = e.Usage.Input, e.Usage.CacheWrite, e.Usage.CacheRead, e.Usage.Output
	if e.IsError || e.Subtype != "success" {
		run.Error = e.Subtype
		if len(e.Errors) > 0 {
			run.Error += ": " + string(e.Errors[0])
		}
	}
	for _, d := range e.Denials {
		run.Denied = append(run.Denied, d.Tool)
	}
	var m mUsage
	for _, u := range e.ModelUsage {
		m.Input += u.Input
		m.CacheWrite += u.CacheWrite
		m.CacheRead += u.CacheRead
		m.Output += u.Output
	}
	if m != (mUsage(e.Usage)) {
		return fmt.Errorf("token counts disagree: usage %+v, modelUsage %+v", e.Usage, m)
	}
	return nil
}
