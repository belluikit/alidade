// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package py

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/belluikit/alidade/internal/python"
)

// cacheText is l written as a cache.
func cacheText(t *testing.T, l *Live) []byte {
	t.Helper()
	var b bytes.Buffer
	w := bufio.NewWriter(&b)
	l.mu.Lock()
	writeCache(w, l)
	l.mu.Unlock()
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestCacheRoundTrip (#279): what a Live knows reads back from its cache
// unchanged, whatever its strings hold.
func TestCacheRoundTrip(t *testing.T) {
	odd := "a \"quoted\"\tname\n\\ é ✓"
	l := &Live{root: "/r", files: []python.File{{Rel: "a.py", Size: 10}, {Rel: "b.py", Size: 0}},
		sums: map[string]string{"a.py": "1f", "b.py": "", "lib/x.py": "2e"},
		jedi: map[string]*python.Result{
			"a.py": {Skipped: map[string]string{}, Workers: 1, ImportsOutside: 1, ImportsUnresolved: 2, Outside: 3, Unplaced: 4,
				Unresolved: 5, BasesOutside: 6, BasesUnplaced: 7, BasesUnresolved: 8,
				Decls:   []python.Decl{{File: "a.py", Path: "C." + odd, Kind: "method", Line: 1, Col: 2, EndLine: 3}},
				Imports: []python.Import{{File: "a.py", Line: 1, Target: "lib/x.py"}},
				Calls:   []python.Call{{File: "a.py", Line: 4, Col: 5, Caller: "C.m", Target: "lib/x.py", TargetLine: 6, TargetName: odd, TargetType: "function"}},
				Refs:    []python.Call{{File: "a.py", Line: 7, Col: 8, Target: "a.py", TargetLine: 1, TargetName: "C", TargetType: "class"}},
				Bases:   []python.Call{{File: "a.py", Line: 9, Col: 1, Caller: "C", Target: "lib/x.py", TargetLine: 2, TargetName: "B", TargetType: "class"}},
				Deps:    map[string]*python.Deps{"a.py": {Files: []string{"lib/x.py"}, Modules: []string{"x"}, Names: []string{odd}, All: true}}},
			"b.py": {Skipped: map[string]string{"b.py": "invalid syntax: " + odd}, Workers: 1},
		}}
	got, err := readCache(bytes.NewReader(cacheText(t, l)), "/r")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.files, l.files) || !reflect.DeepEqual(got.sums, l.sums) || !reflect.DeepEqual(got.jedi, l.jedi) {
		t.Errorf("read back:\n%+v %+v %+v\nwant:\n%+v %+v %+v", got.files, got.sums, got.jedi, l.files, l.sums, l.jedi)
	}
}

// TestCacheIgnored (#279): a cache of another engine, format or root, or
// one damaged, is not read.
func TestCacheIgnored(t *testing.T) {
	l := &Live{root: "/r", files: []python.File{{Rel: "a.py", Size: 1}}, sums: map[string]string{"a.py": "1f"},
		jedi: map[string]*python.Result{"a.py": {Skipped: map[string]string{}, Workers: 1}}}
	text := string(cacheText(t, l))
	head := `(1, "` + python.Engine() + `", "/r")`
	if !strings.Contains(text, head) {
		t.Fatalf("no row %s in\n%s", head, text)
	}
	for name, c := range map[string]struct{ text, root string }{
		"another engine": {strings.Replace(text, head, `(1, "another", "/r")`, 1), "/r"},
		"another format": {strings.Replace(text, head, `(2, "`+python.Engine()+`", "/r")`, 1), "/r"},
		"another root":   {text, "/s"},
		"cut short":      {text[:len(text)-5], "/r"},
		"a row unknown":  {strings.Replace(text, `("a.py", "1f")`, `("a.py", 3)`, 1), "/r"},
		"a table gone":   {text[:strings.Index(text, "@dataset alidade.py.Decl")], "/r"},
	} {
		if _, err := readCache(strings.NewReader(c.text), c.root); err == nil {
			t.Errorf("%s: read", name)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(cachePath(dir, "/r"), []byte(text[:len(text)/2]), 0o644); err != nil {
		t.Fatal(err)
	}
	if loadLive(dir, "/r") != nil {
		t.Error("a damaged cache file loaded")
	}
	if loadLive(t.TempDir(), "/r") != nil {
		t.Error("a cache loaded where there is none")
	}
}

// TestCacheClose (#279): a session that ends before jedi is done keeps the
// files jedi answered, and the next session reads only the rest.
func TestCacheClose(t *testing.T) {
	if !python.Available() {
		t.Skip("built without the python tag")
	}
	dir, err := filepath.Abs(filepath.Join("testdata", "proj"))
	if err != nil {
		t.Fatal(err)
	}
	real := analyzeEach
	t.Cleanup(func() { analyzeEach = real })
	// jedi answers app.py, then never finishes.
	analyzeEach = func(ctx context.Context, root string, files []python.File, workers int, each func(string, *python.Result)) (*python.Result, error) {
		i := slices.IndexFunc(files, func(f python.File) bool { return f.Rel == "app.py" })
		if _, err := real(ctx, root, files[i:i+1], 1, each); err != nil {
			return nil, err
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	opts := DefaultOptions()
	opts.Background, opts.Cache = true, t.TempDir()
	rep, err := Extract(graph.NewBuilder(dir), dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	for done, _, _ := rep.Live.Progress(); done == 0; done, _, _ = rep.Live.Progress() {
		time.Sleep(time.Millisecond) // until app.py is answered
	}
	rep.Live.Close()
	l := loadLive(opts.Cache, dir)
	if l == nil {
		t.Fatal("nothing kept")
	}
	if got := sortedKeys(l.jedi); !slices.Equal(got, []string{"app.py"}) {
		t.Errorf("kept the answers of %v, want [app.py]", got)
	}

	analyzeEach = real
	again, err := Extract(graph.NewBuilder(dir), dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	again.Live.Wait()
	if again.Reused != 1 {
		t.Errorf("Reused %d, want 1 (app.py)", again.Reused)
	}
}
