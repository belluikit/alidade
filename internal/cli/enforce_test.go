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
)

// mySettings is a settings file shaped like a real one: other keys, and
// a PreToolUse hook of the person's own.
const mySettings = `{
  "model": "opus",
  "hooks": {
    "PreToolUse": [
      {"hooks": [{"type": "command", "command": "/Users/me/.config/iterm2/cc-status"}]}
    ],
    "Stop": [{"hooks": [{"type": "command", "command": "/x/stop"}]}]
  },
  "permissions": {"defaultMode": "auto"}
}
`

func preToolUse(t *testing.T, doc []byte) []map[string]any {
	t.Helper()
	var s struct {
		Hooks struct {
			Pre []map[string]any `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(doc, &s); err != nil {
		t.Fatalf("%v: %s", err, doc)
	}
	return s.Hooks.Pre
}

// TestEnforceKeepsTheRestOfTheSettings is #208's setup half: -enforce
// adds the two entries and keeps every other key, hook and the keys'
// order; again changes nothing; a new executable path replaces the old
// entries; -no-enforce removes only them.
func TestEnforceKeepsTheRestOfTheSettings(t *testing.T) {
	on, err := withHook([]byte(mySettings), true, "/opt/homebrew/bin/alidade")
	if err != nil {
		t.Fatal(err)
	}
	pre := preToolUse(t, on)
	if len(pre) != 3 || !strings.Contains(string(on), "cc-status") || !strings.Contains(string(on), `"/opt/homebrew/bin/alidade hook"`) {
		t.Fatalf("after -enforce:\n%s", on)
	}
	if strings.Index(string(on), `"model"`) > strings.Index(string(on), `"hooks"`) || strings.Index(string(on), `"hooks"`) > strings.Index(string(on), `"permissions"`) || !strings.Contains(string(on), `"Stop"`) {
		t.Errorf("keys reordered or lost:\n%s", on)
	}
	checkEnforceAgain(t, on)
}

// checkEnforceAgain: twice changes nothing, a new path replaces the old
// entries, -no-enforce removes only them.
func checkEnforceAgain(t *testing.T, on []byte) {
	t.Helper()
	again, _ := withHook(on, true, "/opt/homebrew/bin/alidade")
	if !bytes.Equal(again, on) {
		t.Errorf("-enforce twice changed the file:\n%s", again)
	}
	moved, _ := withHook(on, true, "/usr/local/bin/alidade")
	if n := strings.Count(string(moved), " hook\""); n != 2 || strings.Contains(string(moved), "/opt/homebrew") {
		t.Errorf("a new path: %d entries, old path kept:\n%s", n, moved)
	}
	off, _ := withHook(moved, false, "")
	if pre := preToolUse(t, off); len(pre) != 1 || !strings.Contains(string(off), "cc-status") || strings.Contains(string(off), "alidade") {
		t.Errorf("after -no-enforce:\n%s", off)
	}
	untouched, _ := withHook([]byte(mySettings), false, "")
	if !bytes.Equal(untouched, []byte(mySettings)) {
		t.Errorf("-no-enforce on a file without the hook changed it")
	}
	fresh, _ := withHook(nil, true, "alidade")
	if len(preToolUse(t, fresh)) != 2 {
		t.Errorf("no settings file: %s", fresh)
	}
}

// TestSetupEnforceWritesTheFile: setup -no-enforce and -enforce with
// -print over a real settings file, which a change backs up first.
func TestSetupEnforceWritesTheFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, "settings.json")
	on, _ := withHook([]byte(mySettings), true, "/bin/alidade")
	if err := os.WriteFile(path, on, 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := Run([]string{"setup", "-no-enforce"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	got, _ := os.ReadFile(path)
	bak, _ := os.ReadFile(path + ".bak-alidade")
	if strings.Contains(string(got), "alidade hook") || !bytes.Equal(bak, on) {
		t.Errorf("after -no-enforce: %s; backup %s", got, bak)
	}
	out.Reset()
	if code := Run([]string{"setup", "-enforce", "-print"}, &out, &errb); code != 0 || !strings.Contains(out.String(), `"PreToolUse"`) {
		t.Errorf("-enforce -print: exit %d, %s", code, out.String())
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, got) {
		t.Error("-print wrote the file")
	}
	if code := Run([]string{"setup", "-enforce", "-no-enforce"}, &out, &errb); code != 2 {
		t.Errorf("both flags: exit %d", code)
	}
}

// TestSetupEnforceRegistersThenHooks: -enforce registers as setup does,
// then adds the hook with the registered executable's path; a failed
// registration adds no hook.
func TestSetupEnforceRegistersThenHooks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	calls := stub(t, "/usr/local/bin/claude", "/opt/homebrew/bin/alidade")
	var out, errb bytes.Buffer
	if code := Run([]string{"setup", "-enforce"}, &out, &errb); code != 0 || len(*calls) != 2 {
		t.Fatalf("exit %d, calls %v: %s", code, *calls, errb.String())
	}
	got, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if !strings.Contains(string(got), `"/opt/homebrew/bin/alidade hook"`) {
		t.Errorf("settings after -enforce: %s", got)
	}
	dir2 := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir2)
	stub(t, "/usr/local/bin/claude", "/opt/homebrew/bin/alidade")
	addFails = true
	if code := Run([]string{"setup", "-enforce"}, &out, &errb); code == 0 {
		t.Error("a failed registration reported success")
	}
	if _, err := os.Stat(filepath.Join(dir2, "settings.json")); err == nil {
		t.Error("a failed registration still added the hook")
	}
}
