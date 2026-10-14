// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// stub replaces the PATH lookup and the command runner for one test.
// registered says whether the stubbed claude already has an alidade
// registration, which "mcp remove" then reports removing.
var registered bool

// addFails makes the stubbed "mcp add" fail.
var addFails bool

func stub(t *testing.T, claude string, exe string) (calls *[][]string) {
	t.Helper()
	calls = &[][]string{}
	registered, addFails = false, false
	origLook, origExe, origRun := lookPath, executable, runCommand
	lookPath = func(name string) (string, error) {
		switch name {
		case "claude":
			if claude == "" {
				return "", errors.New("not found")
			}
			return claude, nil
		case "alidade":
			if exe == "" {
				return "", errors.New("not found")
			}
			return exe, nil
		}
		return "", errors.New("not found")
	}
	executable = func() (string, error) { return "/fallback/alidade", nil }
	runCommand = func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		if len(args) > 1 && args[1] == "remove" {
			if registered {
				return []byte("Removed MCP server alidade\n"), nil
			}
			return []byte("No MCP server found with name: alidade\n"), errors.New("exit status 1")
		}
		if addFails {
			return []byte("Error: something went wrong\n"), errors.New("exit status 1")
		}
		return []byte("Added stdio MCP server alidade\n"), nil
	}
	t.Cleanup(func() { lookPath, executable, runCommand = origLook, origExe, origRun })
	return calls
}

func TestSetupRegistersWithClaude(t *testing.T) {
	calls := stub(t, "/usr/local/bin/claude", "/opt/homebrew/bin/alidade")
	var out, errb bytes.Buffer
	if code := Run([]string{"setup"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want := "/usr/local/bin/claude mcp add --scope user alidade -- /opt/homebrew/bin/alidade serve -root ."
	if len(*calls) != 2 || strings.Join((*calls)[1], " ") != want {
		t.Errorf("claude called with %v, want a remove then %q", *calls, want)
	}
	if rm := strings.Join((*calls)[0], " "); rm != "/usr/local/bin/claude mcp remove alidade -s user" {
		t.Errorf("first call = %q, want the remove", rm)
	}
	if !strings.Contains(out.String(), "registered alidade with Claude Code (user scope)") {
		t.Errorf("stdout = %q", out.String())
	}
	// Project scope.
	*calls = nil
	if code := Run([]string{"setup", "-scope", "project"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if len(*calls) != 2 || (*calls)[1][4] != "project" || (*calls)[0][5] != "project" {
		t.Errorf("project scope: %v", *calls)
	}
}

func TestSetupPrintsWithoutClaude(t *testing.T) {
	calls := stub(t, "", "/opt/homebrew/bin/alidade")
	var out, errb bytes.Buffer
	if code := Run([]string{"setup"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if len(*calls) != 0 {
		t.Errorf("claude was run: %v", *calls)
	}
	if !strings.Contains(errb.String(), "claude is not on PATH") {
		t.Errorf("stderr = %q", errb.String())
	}
	for _, want := range []string{`"mcpServers"`, `"alidade"`, `"command": "/opt/homebrew/bin/alidade"`, `"serve"`, `"-root"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout lacks %s:\n%s", want, out.String())
		}
	}
	// -print prints even when claude is present, and registers nothing.
	calls = stub(t, "/usr/local/bin/claude", "")
	out.Reset()
	if code := Run([]string{"setup", "-print"}, &out, &errb); code != 0 || len(*calls) != 0 {
		t.Errorf("-print: exit %d, calls %v", code, *calls)
	}
	if !strings.Contains(out.String(), `"command": "/fallback/alidade"`) {
		t.Errorf("without alidade on PATH the executable path should be used:\n%s", out.String())
	}
}

func TestSetupRejectsBadScope(t *testing.T) {
	stub(t, "/usr/local/bin/claude", "/opt/homebrew/bin/alidade")
	var out, errb bytes.Buffer
	if code := Run([]string{"setup", "-scope", "global"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "user or project") {
		t.Errorf("exit %d, stderr %q", code, errb.String())
	}
	if code := Run([]string{"setup", "-bogus"}, &out, &errb); code != 2 {
		t.Errorf("bad flag exit %d", code)
	}
}

// TestSetupSchemaRoots pins #53: a schema root goes into the registered
// command, implies project scope, is refused with user scope, and shows
// in -print; setup without it is unchanged (the tests above).
func TestSetupSchemaRoots(t *testing.T) {
	calls := stub(t, "/usr/local/bin/claude", "/opt/homebrew/bin/alidade")
	var out, errb bytes.Buffer
	if code := Run([]string{"setup", "-schema-root", "../protolsp", "-schema-root", "/abs/schemas"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want := "/usr/local/bin/claude mcp add --scope project alidade -- /opt/homebrew/bin/alidade serve -root . -schema-root ../protolsp -schema-root /abs/schemas"
	if len(*calls) != 2 || strings.Join((*calls)[1], " ") != want {
		t.Errorf("claude called with %v, want a remove then %q", *calls, want)
	}
	if !strings.Contains(out.String(), "(project scope) as /opt/homebrew/bin/alidade serve -root . -schema-root ../protolsp") {
		t.Errorf("stdout = %q", out.String())
	}
	*calls = nil
	out.Reset()
	errb.Reset()
	if code := Run([]string{"setup", "-scope", "user", "-schema-root", "../protolsp"}, &out, &errb); code != 2 || len(*calls) != 0 || !strings.Contains(errb.String(), "needs -scope project") {
		t.Errorf("user scope with a schema root: exit %d, calls %v, stderr %q", code, *calls, errb.String())
	}
	out.Reset()
	if code := Run([]string{"setup", "-print", "-schema-root", "../protolsp"}, &out, &errb); code != 0 || len(*calls) != 0 {
		t.Fatalf("-print: exit %d, calls %v", code, *calls)
	}
	if !strings.Contains(out.String(), `"-schema-root",`) || !strings.Contains(out.String(), `"../protolsp"`) {
		t.Errorf("-print lacks the schema root:\n%s", out.String())
	}
}

// TestSetupReplacesAnExistingRegistration pins #65: with a registration
// already in the scope, setup removes it and adds the new one, and says
// so; without one, the failed remove is not an error and the wording is
// "registered".
func TestSetupReplacesAnExistingRegistration(t *testing.T) {
	calls := stub(t, "/usr/local/bin/claude", "/opt/homebrew/bin/alidade")
	registered = true
	var out, errb bytes.Buffer
	if code := Run([]string{"setup"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if len(*calls) != 2 || (*calls)[0][2] != "remove" || (*calls)[1][2] != "add" {
		t.Errorf("calls = %v, want remove then add", *calls)
	}
	if !strings.HasPrefix(out.String(), "re-registered alidade with Claude Code (user scope)") {
		t.Errorf("stdout = %q", out.String())
	}
	registered = false
	out.Reset()
	if code := Run([]string{"setup"}, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "registered alidade") {
		t.Errorf("no existing registration: exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
	}
}

// TestSetupSaysWhenItLeftNothingRegistered pins #100: setup removes the
// old registration before adding the new one, so a failed add leaves
// none, and the error says so.
func TestSetupSaysWhenItLeftNothingRegistered(t *testing.T) {
	stub(t, "/usr/local/bin/claude", "/opt/homebrew/bin/alidade")
	registered, addFails = true, true
	var out, errb bytes.Buffer
	if code := Run([]string{"setup"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "was removed first, so none is registered now") {
		t.Errorf("stderr = %q", errb.String())
	}
	registered = false
	errb.Reset()
	Run([]string{"setup"}, &out, &errb)
	if strings.Contains(errb.String(), "was removed first") {
		t.Errorf("nothing was removed, yet stderr = %q", errb.String())
	}
}

// TestUnexpectedArgumentsAreRefused pins #100: a positional argument is a
// mistake — "setup project" for "-scope project", "graph ../repo" for
// "-root ../repo" — and doing something else silently is worse than
// refusing.
func TestUnexpectedArgumentsAreRefused(t *testing.T) {
	calls := stub(t, "/usr/local/bin/claude", "/opt/homebrew/bin/alidade")
	for _, args := range [][]string{{"setup", "project"}, {"graph", "/nonexistent"}, {"serve", "."}} {
		var out, errb bytes.Buffer
		if code := Run(args, &out, &errb); code != 2 || !strings.Contains(errb.String(), "unexpected argument") {
			t.Errorf("%v: exit %d, stderr %q", args, code, errb.String())
		}
	}
	if len(*calls) != 0 {
		t.Errorf("a refused setup ran %v", *calls)
	}
}

// TestFileSystemRootIsRefused pins #100: "-root ." from a client started
// in "/" would read the whole disk.
func TestFileSystemRootIsRefused(t *testing.T) {
	for _, cmd := range []string{"graph", "serve"} {
		var out, errb bytes.Buffer
		if code := Run([]string{cmd, "-root", "/"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "is the file system root") {
			t.Errorf("%s -root /: exit %d, stderr %q", cmd, code, errb.String())
		}
	}
}
