// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/worktree/gittest"
)

// hookRepo is a git work tree with a subdirectory, a testdata directory,
// a Go package and build output that search_text reads, a virtual
// environment it does not, and a directory outside it, for the hook's
// cases (#208, #275, #266).
func hookRepo(t *testing.T) (repo, outside string) {
	t.Helper()
	base := t.TempDir()
	repo, outside = filepath.Join(base, "repo"), filepath.Join(base, "elsewhere")
	for _, d := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, "internal", "x"), filepath.Join(repo, "testdata"), filepath.Join(repo, "third_party", "sub"), filepath.Join(repo, "mods", "sub", ".git"),
		filepath.Join(repo, "internal", "build"), filepath.Join(repo, "web", "dist"), filepath.Join(repo, "env", "lib"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "env", "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, outside
}

func runHook(t *testing.T, cwd, tool string, input map[string]any) (denied bool, reason string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"cwd": cwd, "tool_name": tool, "tool_input": input, "hook_event_name": "PreToolUse"})
	var out, errb bytes.Buffer
	if code := hook(bytes.NewReader(b), &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if out.Len() == 0 {
		return false, ""
	}
	var d struct {
		H struct {
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &d); err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	return d.H.Decision == "deny", d.H.Reason
}

// TestHookDeniesGrepInTheRepository: every way a session searches the
// repository's text is denied, with the way to alidade's tools; a search
// anywhere alidade does not serve, or of a command's output, is not.
func TestHookDeniesGrepInTheRepository(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("ALIDADE_ALLOW_GREP", "")
	repo, outside := hookRepo(t)
	sub := filepath.Join(repo, "internal", "x")
	for _, c := range []struct {
		name, cwd, tool string
		input           map[string]any
		deny            bool
	}{
		{"Grep, the session's directory", repo, "Grep", map[string]any{"pattern": "Foo"}, true},
		{"Grep, a subdirectory", repo, "Grep", map[string]any{"pattern": "Foo", "path": "internal/x"}, true},
		{"Grep from a subdirectory of the repository", sub, "Grep", map[string]any{"pattern": "Foo"}, true},
		{"Grep, outside the repository", repo, "Grep", map[string]any{"pattern": "Foo", "path": outside}, false},
		{"Grep, under testdata", repo, "Grep", map[string]any{"pattern": "Foo", "path": "testdata"}, false},
		{"Grep, under third_party", repo, "Grep", map[string]any{"pattern": "Foo", "path": "third_party/sub"}, false},
		{"Grep, in a submodule, which alidade reads", repo, "Grep", map[string]any{"pattern": "Foo", "path": "mods/sub"}, true},
		{"Grep, a Go package named build", repo, "Grep", map[string]any{"pattern": "Foo", "path": "internal/build"}, true},
		{"Grep, build output search_text reads", repo, "Grep", map[string]any{"pattern": "Foo", "path": "web/dist"}, true},
		{"Grep, in a virtual environment", repo, "Grep", map[string]any{"pattern": "Foo", "path": "env/lib"}, false},
		{"grep a file in a virtual environment", repo, "Bash", map[string]any{"command": "grep Foo env/lib/x.py"}, false},
		{"Grep, no repository", outside, "Grep", map[string]any{"pattern": "Foo"}, false},
		{"grep -rn", repo, "Bash", map[string]any{"command": "grep -rn Foo ."}, true},
		{"rg", repo, "Bash", map[string]any{"command": "rg Foo internal"}, true},
		{"git grep", repo, "Bash", map[string]any{"command": "git grep -n Foo"}, true},
		{"an env assignment before", repo, "Bash", map[string]any{"command": "LC_ALL=C grep -r Foo ."}, true},
		{"find -exec grep", repo, "Bash", map[string]any{"command": "find . -name '*.go' -exec grep -l Foo {} +"}, true},
		{"xargs grep", repo, "Bash", map[string]any{"command": "ls *.go | xargs grep Foo"}, true},
		{"after && in the repository", repo, "Bash", map[string]any{"command": "go build ./... && grep -r Foo ."}, true},
		{"after ||", repo, "Bash", map[string]any{"command": "false || grep -r Foo ."}, true},
		{"cd elsewhere first", repo, "Bash", map[string]any{"command": "cd " + outside + " && grep -r Foo ."}, false},
		{"cd to a variable", repo, "Bash", map[string]any{"command": "D=/tmp/x; cd $D && grep -r Foo ."}, false},
		{"cd to a command's output", repo, "Bash", map[string]any{"command": "cd `mktemp -d` && grep -r Foo ."}, false},
		{"a pipe into grep", repo, "Bash", map[string]any{"command": "go test ./... 2>&1 | grep FAIL"}, false},
		{"grep as an argument", repo, "Bash", map[string]any{"command": "echo 'use grep here'"}, false},
		{"not a search", repo, "Bash", map[string]any{"command": "go test ./..."}, false},
		// A search naming its files is placed by them, not by the
		// session's directory.
		{"grep a log outside", repo, "Bash", map[string]any{"command": "grep ERROR " + outside + "/server.log"}, false},
		{"grep -n -A 3 a log outside", repo, "Bash", map[string]any{"command": "grep -n -A 3 ERROR " + outside + "/server.log"}, false},
		{"grep -A3 attached, a log outside", repo, "Bash", map[string]any{"command": "grep -A3 ERROR " + outside + "/server.log"}, false},
		{"grep -e, a log outside", repo, "Bash", map[string]any{"command": "grep -e ERROR -e WARN " + outside + "/a.log"}, false},
		{"rg -g value, a directory outside", repo, "Bash", map[string]any{"command": "rg -g '*.log' timeout " + outside}, false},
		{"grep from a redirect outside", repo, "Bash", map[string]any{"command": "grep ERROR < " + outside + "/server.log"}, false},
		{"grep a log under testdata", repo, "Bash", map[string]any{"command": "grep ERROR testdata/run.log"}, false},
		{"grep a file in the repository", repo, "Bash", map[string]any{"command": "grep Foo internal/x/a.go"}, true},
		{"grep one outside, one inside", repo, "Bash", map[string]any{"command": "grep Foo " + outside + "/a.log internal/x/a.go"}, true},
		{"grep -A 3 a file in the repository", repo, "Bash", map[string]any{"command": "grep -A 3 Foo internal/x/a.go"}, true},
		{"grep from a redirect inside", repo, "Bash", map[string]any{"command": "grep Foo < internal/x/a.go"}, true},
		{"grep -c, output redirected", repo, "Bash", map[string]any{"command": "grep -rc Foo . > " + outside + "/count"}, true},
		{"git grep -- a path", repo, "Bash", map[string]any{"command": "git grep -n Foo -- internal"}, true},
		{"git -C a directory of the repository grep", repo, "Bash", map[string]any{"command": "cd " + outside + " && git -C " + repo + "/internal grep -n Foo"}, true},
		{"git -C a relative directory grep", repo, "Bash", map[string]any{"command": "git -C internal grep Foo"}, true},
		{"git -C elsewhere grep", repo, "Bash", map[string]any{"command": "git -C " + outside + " grep -n Foo"}, false},
		{"git -C a variable grep", repo, "Bash", map[string]any{"command": "git -C $D grep Foo"}, false},
		{"git --no-pager grep", repo, "Bash", map[string]any{"command": "git --no-pager grep -n Foo"}, true},
		{"git -c key=value grep", repo, "Bash", map[string]any{"command": "git -c core.quotepath=off grep Foo -- internal"}, true},
		{"git log --grep, not a search of files", repo, "Bash", map[string]any{"command": "git log --grep Foo"}, false},
		{"cd outside, grep a path back in", outside, "Bash", map[string]any{"command": "grep Foo " + repo + "/internal"}, false},
		{"a variable operand", repo, "Bash", map[string]any{"command": "grep ERROR $LOG"}, false},
		// A here-document's body is a command's input, not commands.
		{"a commit message naming grep", repo, "Bash", map[string]any{"command": "git commit -F - <<'EOF'\nhook: fix\n\ngrep ERROR app.log was denied\nEOF"}, false},
		{"cat <<EOF writing a script", repo, "Bash", map[string]any{"command": "cat > x.sh <<EOF\nrg Foo internal\nEOF\nchmod +x x.sh"}, false},
		{"<<- with a tab before the end", repo, "Bash", map[string]any{"command": "cat <<-END\n\tgrep -r Foo .\n\tEND"}, false},
		{"a search after the here-document", repo, "Bash", map[string]any{"command": "cat > x.txt <<EOF\nhello\nEOF\ngrep -r Foo ."}, true},
		{"<< inside quotes starts nothing", repo, "Bash", map[string]any{"command": "echo \"a << B\"\ngrep -r Foo .\nB"}, true},
		{"a here-string is not a here-document", repo, "Bash", map[string]any{"command": "cat <<<EOF\ngrep -r Foo .\nEOF"}, true},
		{"Glob", repo, "Glob", map[string]any{"pattern": "**/*.go"}, false},
	} {
		denied, reason := runHook(t, c.cwd, c.tool, c.input)
		if denied != c.deny {
			t.Errorf("%s: denied %v, want %v", c.name, denied, c.deny)
		}
		if denied && !strings.Contains(reason, "mcp__alidade__search_text") {
			t.Errorf("%s: reason %q does not name search_text", c.name, reason)
		}
	}
}

// TestHookLetsThrough: the escape and unreadable input both let a call
// through, and a denied call is logged.
func TestHookLetsThrough(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	repo, _ := hookRepo(t)
	t.Setenv("ALIDADE_ALLOW_GREP", "1")
	if denied, _ := runHook(t, repo, "Grep", map[string]any{"pattern": "Foo"}); denied {
		t.Error("ALIDADE_ALLOW_GREP=1 still denied")
	}
	t.Setenv("ALIDADE_ALLOW_GREP", "")
	var out bytes.Buffer
	if code := hook(strings.NewReader("not json"), &out, &out); code != 0 || out.Len() != 0 {
		t.Errorf("unreadable input: exit %d, output %q", code, out.String())
	}
	if denied, _ := runHook(t, repo, "Bash", map[string]any{"command": "rg Foo"}); !denied {
		t.Fatal("rg in the repository let through")
	}
	b, err := os.ReadFile(filepath.Join(state, "alidade", "hook.log"))
	if err != nil || !strings.Contains(string(b), `"command":"rg Foo"`) || strings.Count(string(b), "\n") != 1 {
		t.Errorf("hook.log %q, %v; want the one denied call", b, err)
	}
}

// TestHookSaysTheEscapeIsTheSessions: the refusal says grep comes back
// only for a session started with ALIDADE_ALLOW_GREP=1, and a command
// that sets it itself — inline or exported — is refused, told so. An
// agent read the earlier wording as a prefix to add (found in the hook log, 2026-10-09).
func TestHookSaysTheEscapeIsTheSessions(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("ALIDADE_ALLOW_GREP", "")
	repo, _ := hookRepo(t)
	for _, c := range []struct {
		command string
		inline  bool
	}{
		{"grep -r Foo .", false},
		{"ALIDADE_ALLOW_GREP=1 grep -r Foo .", true},
		{"cd internal && ALIDADE_ALLOW_GREP=1 grep -rn Foo .", true},
		{"export ALIDADE_ALLOW_GREP=1; grep -r Foo .", true},
		{"MY_ALIDADE_ALLOW_GREP=1 grep -r Foo .", false},
	} {
		denied, reason := runHook(t, repo, "Bash", map[string]any{"command": c.command})
		if !denied {
			t.Errorf("%q let through", c.command)
			continue
		}
		if !strings.Contains(reason, "session started with ALIDADE_ALLOW_GREP=1") {
			t.Errorf("%q: the reason does not say the escape is the session's: %q", c.command, reason)
		}
		if got := strings.Contains(reason, "not honoured"); got != c.inline {
			t.Errorf("%q: says the inline escape is not honoured: %v, want %v", c.command, got, c.inline)
		}
	}
}

// TestHookCoversWorktrees pins #23: a search in another worktree of the
// session's repository is refused as one in the repository is, and told
// the root to pass, from the main checkout or from the worktree; a
// search in another repository, or under a directory search_text leaves
// out, is not. Before, the worktree lay outside the session's repository
// and every search there went through without a word.
func TestHookCoversWorktrees(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("ALIDADE_ALLOW_GREP", "")
	base := gittest.TempDir(t)
	main, wt, other := filepath.Join(base, "main"), filepath.Join(base, "wt"), filepath.Join(base, "other")
	gittest.Init(t, main, map[string]string{"a.go": "package a\n", "vendor/v/v.go": "package v\n"})
	gittest.Git(t, main, "worktree", "add", "-q", "-b", "feature", wt)
	// Worktrees inside the main checkout: where Claude Code puts them,
	// and under .tmp, which a search of the main checkout leaves out.
	nested, hidden := filepath.Join(main, ".claude", "worktrees", "n"), filepath.Join(main, ".tmp", "h")
	gittest.Git(t, main, "worktree", "add", "-q", "-b", "nested", nested)
	gittest.Git(t, main, "worktree", "add", "-q", "-b", "hidden", hidden)
	gittest.Init(t, other, map[string]string{"a.go": "package a\n"})
	for _, c := range []struct {
		name, cwd, tool string
		input           map[string]any
		root            string // the root the refusal names; "" when let through
	}{
		{"Grep in the worktree", main, "Grep", map[string]any{"pattern": "Foo", "path": wt}, wt},
		{"cd to the worktree, git grep", main, "Bash", map[string]any{"command": "cd " + wt + " && git grep -n -E 'a|b' -- .github"}, wt},
		{"git -C the worktree grep", main, "Bash", map[string]any{"command": "git -C " + wt + " grep -n -E 'go#56|linked externally'"}, wt},
		{"grep a worktree file", main, "Bash", map[string]any{"command": "grep -n Foo " + wt + "/a.go"}, wt},
		{"from the worktree, grep the main checkout", wt, "Bash", map[string]any{"command": "grep -rn Foo " + main}, main},
		{"from the worktree, its own files", wt, "Bash", map[string]any{"command": "grep -rn Foo ."}, ""},
		{"another repository", main, "Bash", map[string]any{"command": "grep -rn Foo " + other}, "-"},
		{"under vendor in the worktree", main, "Bash", map[string]any{"command": "grep -rn Foo " + wt + "/vendor"}, "-"},
		{"reading a worktree file", main, "Bash", map[string]any{"command": "sed -n 1,20p " + wt + "/a.go"}, "-"},
		{"a worktree inside the main checkout", main, "Bash", map[string]any{"command": "grep -rn Foo .claude/worktrees/n"}, nested},
		{"a worktree under a directory the main checkout's search leaves out", main, "Grep", map[string]any{"pattern": "Foo", "path": hidden}, hidden},
		{"git --work-tree the worktree grep", main, "Bash", map[string]any{"command": "git --git-dir " + main + "/.git --work-tree " + wt + " grep Foo"}, wt},
		{"git --work-tree= the repository, from elsewhere", main, "Bash", map[string]any{"command": "cd " + other + " && git --work-tree=" + main + " grep Foo"}, ""},
		{"git --work-tree another repository", main, "Bash", map[string]any{"command": "git --git-dir " + other + "/.git --work-tree " + other + " grep Foo"}, "-"},
	} {
		checkHookRoot(t, c.name, c.cwd, c.tool, c.input, c.root)
	}
}

// checkHookRoot runs the hook and checks the root its refusal names: ""
// for a refusal naming none, "-" for a call let through.
func checkHookRoot(t *testing.T, name, cwd, tool string, input map[string]any, root string) {
	t.Helper()
	denied, reason := runHook(t, cwd, tool, input)
	if denied != (root != "-") {
		t.Errorf("%s: denied %v", name, denied)
		return
	}
	if names := root != "" && root != "-"; names != strings.Contains(reason, "root=\""+root+"\"") {
		t.Errorf("%s: reason %q, want it to name root %q: %v", name, reason, root, names)
	}
}
