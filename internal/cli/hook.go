// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/belluikit/alidade/internal/mcp"
	"github.com/belluikit/alidade/internal/scope"
	"github.com/belluikit/alidade/internal/worktree"
)

// hook is a Claude Code PreToolUse hook (#208, the owner's decision):
// it denies a text search — the Grep tool, or a Bash command running
// grep, egrep, fgrep, rg, ag, ack or git grep — whose target lies in the
// git repository the session runs in, which alidade serves, and tells
// the model what to use instead. A search anywhere else — a log, a
// directory outside any repository, another repository, a directory
// search_text leaves out — is let through, as is everything when
// ALIDADE_ALLOW_GREP=1. Measured (#206): an agent with alidade and no
// grep answered every benchmark question correctly.
//
// It reads the hook input on stdin and writes the decision on stdout; a
// call it lets through writes nothing. It never fails a session: input
// it cannot read is let through.
func hook(stdin io.Reader, stdout, stderr io.Writer) int {
	var in hookInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil || os.Getenv("ALIDADE_ALLOW_GREP") == "1" {
		return 0
	}
	targets, ok := in.searchTargets()
	if !ok {
		return 0
	}
	repo := worktree.Top(in.sessionDir())
	target, top, ok := served(targets, repo)
	if !ok {
		return 0
	}
	logBlocked(in, target)
	out := map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       "deny",
		"permissionDecisionReason": reasonFor(in.ToolInput.Command, top, repo),
	}}
	if err := json.NewEncoder(stdout).Encode(out); err != nil {
		fmt.Fprintf(stderr, "alidade hook: %v\n", err)
	}
	return 0
}

const denyReason = "Text search is off in this repository: alidade serves it (and answered every benchmark question correctly without grep). " +
	"For text — a literal, a regular expression, a log message — use mcp__alidade__search_text: every hit comes with the declaration holding it. " +
	"For how code relates — callers, implementations, uses, schema to code — use mcp__alidade__find_nodes, then neighbors or shortest_path. " +
	"search_text also searches another git worktree of the repository (root, its top directory) and a commit's files (ref, such as origin/main). " +
	escapeNote

// escapeNote says how a session gets grep back: by its environment when
// it starts, which is the person's call, not a command's. An agent read
// the reason's earlier wording — "a session that truly needs grep runs
// with ALIDADE_ALLOW_GREP=1" — as a prefix to add, and wrote
// ALIDADE_ALLOW_GREP=1 grep …, which was refused without saying why.
const escapeNote = "Grep comes back only for a session started with ALIDADE_ALLOW_GREP=1 in its environment " +
	"(ALIDADE_ALLOW_GREP=1 claude); a command that sets it itself is still refused."

// inlineEscapeRE is ALIDADE_ALLOW_GREP=1 written into a command, as an
// assignment before a program or an export.
var inlineEscapeRE = regexp.MustCompile(`(^|[\s;&|(])(export\s+)?ALIDADE_ALLOW_GREP=1\b`)

// reasonFor is the refusal's reason, saying so when the command set
// ALIDADE_ALLOW_GREP=1 itself: it is not honoured there, since an agent
// could otherwise lift the refusal at will. A search of another worktree
// than the session's is told the root to pass (#23).
func reasonFor(command, top, repo string) string {
	reason := denyReason
	if top != repo {
		reason = fmt.Sprintf("%s is a worktree of this repository: pass root=%q to mcp__alidade__search_text. ", top, top) + reason
	}
	if inlineEscapeRE.MatchString(command) {
		return "ALIDADE_ALLOW_GREP=1 set inside a command is not honoured: it is read from the session's environment when the session starts. " + reason
	}
	return reason
}

type hookInput struct {
	Cwd       string `json:"cwd"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Path    string `json:"path"`
		Command string `json:"command"`
	} `json:"tool_input"`
}

// searchTargets are the directories and files a call searches, and
// whether the call is a text search at all.
func (in hookInput) searchTargets() ([]string, bool) {
	switch in.ToolName {
	case "Grep":
		return []string{in.resolve(in.ToolInput.Path)}, true
	case "Bash":
		return bashSearch(in.Cwd, in.ToolInput.Command)
	}
	return nil, false
}

// served is the first target alidade serves, and the checkout it lies
// in: one inside the repository, or inside another worktree of it, which
// search_text's root searches (#23); not under a directory search_text
// leaves out.
func served(targets []string, repo string) (string, string, bool) {
	if repo == "" {
		return "", "", false
	}
	for _, t := range targets {
		if top := checkoutOf(t, repo); top != "" && !underSearchSkip(t, top) {
			return t, top, true
		}
	}
	return "", "", false
}

// checkoutOf is the checkout of the repository a target lies in: the
// repository's own, or another worktree of it; "" for neither. The
// nearest checkout of the repository is asked first, since a worktree
// can lie inside the repository's own, as Claude Code's
// .claude/worktrees/<name> does, and a search there is one of that
// worktree, even in a submodule inside it (#24).
func checkoutOf(target, repo string) string {
	for top := worktree.Top(target); top != ""; top = worktree.Top(filepath.Dir(top)) {
		if worktree.Same(top, repo) {
			return top
		}
		if top == filepath.Dir(top) {
			break
		}
	}
	if inside(target, repo) {
		return repo
	}
	return ""
}

// sessionDir is the directory the session started in, which alidade
// serves (setup registers `serve -root .` there): CLAUDE_PROJECT_DIR,
// which Claude Code gives its hooks and its MCP servers alike. The
// input's cwd follows the Bash tool's cd, so after cd into a worktree it
// names that worktree, and a search there would not be told its root. A
// hook run without the variable falls back to cwd.
func (in hookInput) sessionDir() string {
	if dir := os.Getenv("CLAUDE_PROJECT_DIR"); dir != "" {
		return dir
	}
	return in.Cwd
}

func (in hookInput) resolve(p string) string {
	if p == "" {
		return in.Cwd
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(in.Cwd, p)
	}
	return filepath.Clean(p)
}

// searchCommands are the programs a Bash command searches text with.
var searchCommands = map[string]bool{"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "ack": true}

// bashSearch reports whether a shell command runs a text search, and
// what it searches: the files and directories it names — grep ERROR
// /tmp/server.log searches a log, wherever the session is — or, naming
// none, the directory it runs in: the session's, or the last cd before
// it. A search fed by a pipe — go test | grep FAIL — filters a command's
// output rather than searching files, which alidade cannot do, and is
// let through.
func bashSearch(cwd, command string) ([]string, bool) {
	dir := cwd
	for _, seg := range shellSegments(withoutHeredocs(command)) {
		words := strings.Fields(seg.text)
		for len(words) > 0 && strings.Contains(words[0], "=") {
			words = words[1:] // FOO=bar grep …
		}
		if len(words) == 0 {
			continue
		}
		if words[0] == "cd" && len(words) > 1 {
			if strings.ContainsAny(words[1], "$`") {
				// cd $D: where it goes is the shell's to say, not ours,
				// and a search we cannot place is not refused.
				return nil, false
			}
			dir = resolveFrom(dir, words[1])
			continue
		}
		// xargs makes a pipe's lines the search's file names: a search
		// of files after all.
		if seg.piped && filepath.Base(words[0]) != "xargs" {
			continue
		}
		if i, ok := runsSearch(words); ok {
			return placeSearch(dir, words, i)
		}
	}
	return nil, false
}

// placeSearch is what a search command searches: the files it names,
// resolved from dir, or dir itself when it names none. Through xargs or
// find -exec, the files searched are another command's output, under the
// directory it runs in.
func placeSearch(dir string, words []string, prog int) ([]string, bool) {
	operands, placed := searchOperands(words[prog:])
	if !placed {
		return nil, false
	}
	if filepath.Base(words[prog]) == "git" {
		// git -C dir grep searches dir, and its paths are dir's; git
		// --work-tree dir grep searches dir.
		_, cdirs, _ := gitGrep(words[prog+1:])
		for _, c := range cdirs {
			if strings.ContainsAny(c, "$`") {
				return nil, false
			}
			dir = resolveFrom(dir, c)
		}
	}
	if lead := filepath.Base(words[0]); len(operands) == 0 || lead == "xargs" || lead == "find" {
		return []string{dir}, true
	}
	targets := make([]string, 0, len(operands))
	for _, o := range operands {
		targets = append(targets, resolveFrom(dir, o))
	}
	return targets, true
}

// runsSearch reports whether a command's words run a search program —
// itself, after git, or through xargs, find -exec, sudo, env, time or
// command — and the index of the program's word: grep's, or git's.
func runsSearch(words []string) (int, bool) {
	for i, w := range words {
		base := filepath.Base(w)
		lead := i == 0 || after(words[i-1])
		switch {
		case searchCommands[base] && lead:
			return i, true
		case base == "git" && lead:
			if _, _, ok := gitGrep(words[i+1:]); ok {
				return i, true
			}
		}
	}
	return 0, false
}

// gitGrep reads the words after git: its own options before the command
// — -C dir, -c key=value, --no-pager — and reports whether the command is
// grep, how many words that took, grep's included, and the directories
// -C and --work-tree move it to, in order.
func gitGrep(words []string) (int, []string, bool) {
	var cdirs []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "grep":
			return i + 1, cdirs, true
		case !strings.HasPrefix(w, "-"):
			return 0, nil, false
		case (w == "-C" || w == "--work-tree") && i+1 < len(words):
			i++
			cdirs = append(cdirs, words[i])
		case strings.HasPrefix(w, "--work-tree="):
			cdirs = append(cdirs, strings.TrimPrefix(w, "--work-tree="))
		case gitValueOptions[w]:
			i++
		}
	}
	return 0, nil, false
}

// gitValueOptions are git's own options that take the next word as
// their value.
var gitValueOptions = map[string]bool{"-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true, "--config-env": true}

// valueFlags are each search program's one-letter options that take a
// value: grep -A 3, rg -g '*.go'.
var valueFlags = map[string]string{
	"grep": "efmABCdD", "egrep": "efmABCdD", "fgrep": "efmABCdD", "git": "efmABC",
	"rg": "efgtTmABCjMrdE", "ag": "ABCGgm", "ack": "ABCm",
}

// searchOperands reads a search's own words — grep -n -A 3 Foo a.go b.go,
// or git grep Foo -- internal — and returns the files and directories it
// names, past its options and its pattern, and whether they can be
// placed: an operand the shell expands, $F or `ls`, cannot. A redirect's
// file, grep ERROR < app.log, is searched too; one written, > out, is
// not.
func searchOperands(words []string) ([]string, bool) {
	prog := filepath.Base(words[0])
	words = words[1:]
	if prog == "git" {
		n, _, _ := gitGrep(words)
		words = words[n:] // git's own options, and grep
	}
	r := &operandReader{prog: prog, values: valueFlags[prog]}
	for i := 0; i < len(words); i++ {
		if r.next {
			r.next = false
			continue
		}
		w := strings.Trim(words[i], `"'`)
		if strings.HasPrefix(w, "<") && !strings.HasPrefix(w, "<<") || strings.Contains(w, ">") {
			i += r.redirect(w, words[i+1:])
			continue
		}
		if !r.option(w) && !r.operand(w) {
			return nil, false
		}
	}
	return r.operands, true
}

// An operandReader reads one search command's words.
type operandReader struct {
	prog, values string
	// pattern is set once the pattern is read, positionally or by -e or
	// -f; onlyOperands is set by --; next skips an option's value.
	pattern, onlyOperands, next bool
	operands                    []string
}

// redirect reads a redirect, < in or > out, and returns how many of the
// words after it are its file.
func (r *operandReader) redirect(w string, rest []string) int {
	if strings.Contains(w, ">") {
		if strings.HasSuffix(w, ">") {
			return 1 // > out: the file written, not searched
		}
		return 0
	}
	if f := strings.TrimPrefix(w, "<"); f != "" {
		r.operands = append(r.operands, f)
		return 0
	}
	if len(rest) > 0 {
		r.operands = append(r.operands, strings.Trim(rest[0], `"'`))
		return 1
	}
	return 0
}

// option reads w when it is an option, or --, and reports whether it was.
func (r *operandReader) option(w string) bool {
	switch {
	case r.onlyOperands || !strings.HasPrefix(w, "-") || w == "-":
		return false
	case w == "--":
		r.onlyOperands = true
		r.pattern = r.pattern || r.prog == "git" // git grep: what follows -- is paths
	case strings.HasPrefix(w, "--"):
		name, _, hasValue := strings.Cut(w[2:], "=")
		r.pattern = r.pattern || name == "regexp" || name == "file"
		r.next = !hasValue && longValue[name]
	default:
		r.shortOptions(w[1:])
	}
	return true
}

// shortOptions reads a cluster of one-letter options, -rn or -A3: -e and
// -f give the pattern, and an option taking a value takes the rest of
// the cluster, or the next word when it ends the cluster.
func (r *operandReader) shortOptions(cluster string) {
	for j, c := range cluster {
		r.pattern = r.pattern || c == 'e' || c == 'f'
		if strings.ContainsRune(r.values, c) {
			r.next = j == len(cluster)-1
			return
		}
	}
}

// operand reads the pattern or a file, reporting false for a file the
// shell expands.
func (r *operandReader) operand(w string) bool {
	if !r.pattern {
		r.pattern = true
		return true
	}
	if strings.ContainsAny(w, "$`") {
		return false
	}
	r.operands = append(r.operands, w)
	return true
}

// longValue are the long options that take the next word as their value
// when not written --name=value.
var longValue = map[string]bool{
	"regexp": true, "file": true, "max-count": true, "after-context": true, "before-context": true,
	"context": true, "include": true, "exclude": true, "exclude-dir": true, "label": true,
	"glob": true, "iglob": true, "type": true, "type-not": true, "replace": true, "max-depth": true,
	"devices": true, "directories": true, "binary-files": true, "encoding": true, "threads": true,
}

func after(w string) bool {
	switch filepath.Base(w) {
	case "xargs", "-exec", "-execdir", "sudo", "env", "time", "command", "nice":
		return true
	}
	return false
}

// heredocRE finds a here-document's start, <<EOF, <<-EOF or <<'EOF',
// and not a here-string, <<<.
var heredocRE = regexp.MustCompile(`(^|[^<])<<(-?)[ \t]*["']?([A-Za-z_][A-Za-z0-9_]*)["']?`)

// withoutHeredocs drops the bodies of a command's here-documents: they
// are a command's input — a commit message, a file written by cat — not
// commands, whatever words they hold.
func withoutHeredocs(command string) string {
	lines := strings.Split(command, "\n")
	var out []string
	var ends []string // the delimiters still open, in order
	for _, line := range lines {
		if len(ends) > 0 {
			if strings.TrimLeft(line, "\t") == ends[0] {
				ends = ends[1:]
			}
			continue
		}
		out = append(out, line)
		for _, m := range heredocRE.FindAllStringSubmatchIndex(line, -1) {
			if quoteBefore(line[:m[3]]) == 0 { // the quote state where << starts
				ends = append(ends, line[m[6]:m[7]])
			}
		}
	}
	return strings.Join(out, "\n")
}

// quoteBefore is the quote a line is inside at its end.
func quoteBefore(s string) rune {
	var q rune
	for _, r := range s {
		if q != 0 || r == '\'' || r == '"' {
			q = quoteState(q, r)
		}
	}
	return q
}

// A segment is one simple command of a shell line, and whether its input
// is the previous command's output.
type segment struct {
	text  string
	piped bool
}

// shellSegments cuts a command at ; & | ( ) and newlines, outside quotes.
func shellSegments(command string) []segment {
	var out []segment
	var cur strings.Builder
	var quote rune
	piped := false
	rs := []rune(command)
	for i, r := range rs {
		if quote != 0 || r == '\'' || r == '"' {
			quote = quoteState(quote, r)
			cur.WriteRune(r)
			continue
		}
		cut, pipe := separator(rs, i)
		if !cut {
			cur.WriteRune(r)
			continue
		}
		out = append(out, segment{cur.String(), piped})
		cur.Reset()
		piped = pipe
	}
	return append(out, segment{cur.String(), piped})
}

// quoteState is the quote a shell line is inside after r.
func quoteState(quote, r rune) rune {
	switch {
	case quote == 0:
		return r
	case r == quote:
		return 0
	}
	return quote
}

// separator reports whether the rune at i ends a simple command, and
// whether the next one reads its output: a single |, not ||.
func separator(rs []rune, i int) (cut, pipe bool) {
	switch rs[i] {
	case '|':
		double := i+1 < len(rs) && rs[i+1] == '|' || i > 0 && rs[i-1] == '|'
		return true, !double
	case ';', '&', '\n', '(', ')':
		return true, false
	}
	return false, false
}

func resolveFrom(dir, p string) string {
	p = strings.Trim(p, `"'`)
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = home + p[1:]
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return filepath.Clean(p)
}

func inside(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// underSearchSkip reports whether a target lies under a directory
// search_text leaves out, where grep is the only search there is: one it
// skips by name, one the repository's .alidade.pxf leaves out — vendor
// and third_party by default (#215) — or installed packages (#266).
func underSearchSkip(target, repo string) bool {
	rel, err := filepath.Rel(repo, target)
	if err != nil || rel == "." {
		return false
	}
	for _, d := range strings.Split(filepath.ToSlash(rel), "/") {
		if mcp.SearchSkips(d) {
			return true
		}
	}
	skip, err := scope.Load(repo)
	if err != nil {
		skip = scope.New(scope.Default)
	}
	return skip.SkipsUnder(repo, target)
}

// logBlocked appends the denied call to $XDG_STATE_HOME/alidade/hook.log
// (~/.local/state/alidade/hook.log): what agents tried to grep, for the
// review that finds what alidade does not answer yet.
func logBlocked(in hookInput, target string) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		dir = filepath.Join(home, ".local", "state")
	}
	dir = filepath.Join(dir, "alidade")
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "hook.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(map[string]any{"at": time.Now().UTC().Format(time.RFC3339), "cwd": in.Cwd, "tool": in.ToolName,
		"target": target, "path": in.ToolInput.Path, "command": in.ToolInput.Command})
	fmt.Fprintf(f, "%s\n", line)
}
