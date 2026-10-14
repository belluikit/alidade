// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build v8

package svelte

import (
	"strings"
	"testing"
)

// TestConvertMapsCallsBack: svelte2tsx in V8 translates a component, a
// call in its script and one in its markup are in the TypeScript, and the
// source map puts each back on its own text in the component (#221).
func TestConvertMapsCallsBack(t *testing.T) {
	src := "<script lang=\"ts\">\n  function save() { return 1; }\n  save();\n</script>\n\n<button onclick={() => save()}>{label()}</button>\n"
	if !Available() {
		t.Fatal("a v8 build is not Available")
	}
	o, err := Convert(src, "/repo/src/Form.svelte")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(o.Code, "\n")
	srcLines := strings.Split(src, "\n")
	found := map[string]bool{}
	for i, l := range lines {
		for _, name := range []string{"save()", "label()"} {
			j := strings.Index(l, name)
			if j < 0 || strings.Contains(l, "function save") {
				continue
			}
			ol, oc, ok := o.Original(i, j)
			if !ok {
				t.Errorf("%s at %d:%d maps nowhere", name, i, j)
				continue
			}
			if got := srcLines[ol][oc:min(len(srcLines[ol]), oc+len(name))]; got != name {
				t.Errorf("%s maps to %d:%d, %q", name, ol, oc, got)
			}
			found[name] = true
		}
	}
	if !found["save()"] || !found["label()"] {
		t.Errorf("calls found in the translation: %v\n%s", found, o.Code)
	}
	again, _ := Convert(src, "/repo/src/Form.svelte")
	if again != o {
		t.Error("a component translated twice was not cached")
	}
}
