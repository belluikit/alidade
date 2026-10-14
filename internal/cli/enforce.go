// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The grep hook's settings (#208): two PreToolUse entries running
// `alidade hook`, one for the Grep tool and one for Bash. Ours are told
// apart from a person's own hooks by their command ending in " hook" and
// naming alidade, so -enforce replaces them and -no-enforce removes them
// and nothing else.

// hookEntries are the PreToolUse entries -enforce adds.
func hookEntries(exe string) []json.RawMessage {
	var out []json.RawMessage
	for _, matcher := range []string{"Grep", "Bash"} {
		b, _ := json.Marshal(map[string]any{"matcher": matcher, "hooks": []any{map[string]any{"type": "command", "command": exe + " hook"}}})
		out = append(out, b)
	}
	return out
}

// ourEntry reports whether a PreToolUse entry is alidade's.
func ourEntry(raw json.RawMessage) bool {
	var e struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if json.Unmarshal(raw, &e) != nil || len(e.Hooks) != 1 {
		return false
	}
	c := e.Hooks[0].Command
	return strings.HasSuffix(c, " hook") && strings.Contains(filepath.Base(strings.TrimSuffix(c, " hook")), "alidade")
}

// settingsPath is Claude Code's user settings file.
func settingsPath() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// enforce adds (on) or removes (!on) the grep hook in the settings file,
// keeping every other key, and the order of the file's keys, as they
// were; a file it changes is first copied to settings.json.bak-alidade.
func enforce(on bool, exe string, stdout, stderr io.Writer) int {
	path, err := settingsPath()
	if err != nil {
		fmt.Fprintf(stderr, "alidade setup: %v\n", err)
		return 1
	}
	before, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "alidade setup: %v\n", err)
		return 1
	}
	after, err := withHook(before, on, exe)
	if err != nil {
		fmt.Fprintf(stderr, "alidade setup: %s: %v\n", path, err)
		return 1
	}
	verb := map[bool]string{true: "added alidade's grep hook to", false: "removed alidade's grep hook from"}[on]
	if bytes.Equal(before, after) {
		fmt.Fprintf(stdout, "%s already as asked\n", path)
		return 0
	}
	if len(before) > 0 {
		if err := os.WriteFile(path+".bak-alidade", before, 0o644); err != nil {
			fmt.Fprintf(stderr, "alidade setup: %v\n", err)
			return 1
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(stderr, "alidade setup: %v\n", err)
		return 1
	}
	if err := os.WriteFile(path, after, 0o644); err != nil {
		fmt.Fprintf(stderr, "alidade setup: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s %s\n", verb, path)
	return 0
}

// withHook is a settings document with alidade's hook entries added or
// removed. An empty document is {}; a document already as asked is
// returned byte for byte.
func withHook(doc []byte, on bool, exe string) ([]byte, error) {
	if len(bytes.TrimSpace(doc)) == 0 {
		doc = []byte("{}")
	}
	top, err := objectFields(doc)
	if err != nil {
		return nil, err
	}
	hooksRaw := top.get("hooks")
	if hooksRaw == nil {
		hooksRaw = json.RawMessage("{}")
	}
	hooks, err := objectFields(hooksRaw)
	if err != nil {
		return nil, fmt.Errorf("hooks: %w", err)
	}
	var pre []json.RawMessage
	if raw := hooks.get("PreToolUse"); raw != nil {
		if err := json.Unmarshal(raw, &pre); err != nil {
			return nil, fmt.Errorf("hooks.PreToolUse: %w", err)
		}
	}
	kept, changed := editEntries(pre, on, exe)
	if !changed {
		return doc, nil
	}
	setOrRemove(&hooks, "PreToolUse", kept)
	if len(hooks) == 0 {
		top.remove("hooks")
	} else {
		top.set("hooks", hooks.marshal())
	}
	var out bytes.Buffer
	if err := json.Indent(&out, top.marshal(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// editEntries is a PreToolUse list without alidade's entries, with them
// appended when on, and whether that changed the list.
func editEntries(pre []json.RawMessage, on bool, exe string) ([]json.RawMessage, bool) {
	kept := pre[:0:0]
	for _, e := range pre {
		if !ourEntry(e) {
			kept = append(kept, e)
		}
	}
	if !on {
		return kept, len(kept) != len(pre)
	}
	kept = append(kept, hookEntries(exe)...)
	return kept, !sameEntries(pre, kept)
}

// setOrRemove sets a list under key, or removes the key for an empty one.
func setOrRemove(f *fields, key string, list []json.RawMessage) {
	if len(list) == 0 {
		f.remove(key)
		return
	}
	b, _ := json.Marshal(list)
	f.set(key, b)
}

// sameEntries reports whether adding the hook left the entries as they
// were: ours already present, and last.
func sameEntries(before, after []json.RawMessage) bool {
	if len(before) != len(after) {
		return false
	}
	for i := range before {
		var a, b any
		if json.Unmarshal(before[i], &a) != nil || json.Unmarshal(after[i], &b) != nil {
			return false
		}
		if fmt.Sprint(a) != fmt.Sprint(b) {
			return false
		}
	}
	return true
}

// fields is a JSON object's members in their order.
type fields []field

type field struct {
	key string
	val json.RawMessage
}

func objectFields(raw json.RawMessage) (fields, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not a JSON object")
	}
	var out fields
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, field{k.(string), v})
	}
	return out, nil
}

func (f fields) get(k string) json.RawMessage {
	for _, x := range f {
		if x.key == k {
			return x.val
		}
	}
	return nil
}

func (f *fields) set(k string, v json.RawMessage) {
	for i, x := range *f {
		if x.key == k {
			(*f)[i].val = v
			return
		}
	}
	*f = append(*f, field{k, v})
}

func (f *fields) remove(k string) {
	out := (*f)[:0]
	for _, x := range *f {
		if x.key != k {
			out = append(out, x)
		}
	}
	*f = out
}

func (f fields) marshal() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, x := range f {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(x.key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(x.val)
	}
	b.WriteByte('}')
	return b.Bytes()
}
