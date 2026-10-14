// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package cli

import (
	"bytes"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

var fixture = filepath.Join("..", "adapter", "golang", "testdata", "fixture")

func TestGraphWritesPXF(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"graph", "-root", fixture}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if _, err := pxf.Parse(out.Bytes()); err != nil {
		t.Fatalf("output does not parse as PXF: %v", err)
	}
	if !strings.Contains(out.String(), "go://example.com/fixture/shapes#Total") {
		t.Error("output lacks the fixture's Total node")
	}
	if strings.Contains(out.String(), "TestTotal") || strings.Contains(out.String(), "test = true") {
		t.Error("tests included without -tests")
	}
}

// TestGraphWritesBuildBlock: the PXF carries a build block under the
// keys graph_stats uses, module edges included, so a session with only
// the CLI reads what an MCP session would.
func TestGraphWritesBuildBlock(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"graph", "-root", fixture}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	s := out.String()
	if strings.Count(s, "\nbuild {\n") != 1 || strings.Index(s, "\nbuild {\n") < strings.Index(s, "stats {") {
		t.Errorf("want one build block after stats; got:\n%s", s[:min(len(s), 600)])
	}
	for _, want := range []string{
		`layers = "go"`, `go_build = "`, `go_modules = "example.com/fixture"`, "go_module_edges {",
		`from_module = "example.com/fixture"`, `to_module = "external"`, `kind = "calls"`, `kind = "imports"`, "count = 2", "build_ms = ",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("build block lacks %s", want)
		}
	}
	if strings.Contains(s, "go_modules_failed") || strings.Contains(s, "layers_rebuilt") {
		t.Error("empty fields were written")
	}
}

func TestGraphFlags(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"graph", "-root", fixture, "-tests"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "TestTotal") {
		t.Error("-tests did not include TestTotal")
	}
	if s := out.String(); !strings.Contains(s, "test = true") || !strings.Contains(s, "go_test_nodes = ") {
		t.Error("-tests did not mark test nodes or report go_test_nodes")
	}
	if !strings.Contains(out.String(), "GenThing") {
		t.Error("generated GenThing is not in the graph")
	}
}

// TestTestsExactFlag: -tests-exact loads tests (it implies -tests) and
// says the production view is exact in the build block (#133).
func TestTestsExactFlag(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"graph", "-root", fixture, "-tests-exact"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if s := out.String(); !strings.Contains(s, "TestTotal") || !strings.Contains(s, `go_tests = "exact"`) {
		t.Errorf("-tests-exact: TestTotal present %v, go_tests exact %v", strings.Contains(s, "TestTotal"), strings.Contains(s, `go_tests = "exact"`))
	}
	out.Reset()
	if code := Run([]string{"graph", "-root", fixture, "-tests"}, &out, &errb); code != 0 || strings.Contains(out.String(), "go_tests =") {
		t.Errorf("-tests alone: exit %d, says go_tests: %v", code, strings.Contains(out.String(), "go_tests ="))
	}
}

// TestNoGeneratedIsRemoved pins #126: the option is gone, and asking for
// it says so and what to use instead, rather than "flag provided but not
// defined".
func TestNoGeneratedIsRemoved(t *testing.T) {
	for _, flag := range []string{"-no-generated", "--no-generated", "-no-generated=true"} {
		var out, errb bytes.Buffer
		code := Run([]string{"graph", "-root", fixture, flag}, &out, &errb)
		if code != 2 || !strings.Contains(errb.String(), "-no-generated was removed (#126)") || !strings.Contains(errb.String(), "exclude_generated") {
			t.Errorf("%s: exit %d, %q", flag, code, errb.String())
		}
	}
}

func TestBuildFuncReportsLayers(t *testing.T) {
	g, info, err := buildFunc(fixture, defaultOpts())()
	if err != nil {
		t.Fatal(err)
	}
	if g.NodeCount() == 0 || strings.Join(info.Layers, ",") != "go" || !filepath.IsAbs(info.Root) {
		t.Errorf("info = %+v with %d nodes", info, g.NodeCount())
	}
	if _, _, err := buildFunc(filepath.Join(fixture, "go.mod"), defaultOpts())(); err == nil {
		t.Error("building from a file as root succeeded")
	}
}

func TestUsageAndErrors(t *testing.T) {
	for _, tt := range []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"bogus"}, 2},
		{[]string{"graph", "-nope"}, 2},
		{[]string{"help"}, 0},
	} {
		var out, errb bytes.Buffer
		if code := Run(tt.args, &out, &errb); code != tt.code {
			t.Errorf("Run(%v) = %d, want %d (stderr %q)", tt.args, code, tt.code, errb.String())
		}
		if !strings.Contains(out.String()+errb.String(), "usage:") {
			t.Errorf("Run(%v) printed no usage", tt.args)
		}
	}
}

func TestGraphBuildError(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"graph", "-root", filepath.Join(fixture, "go.mod")}, &out, &errb); code != 1 {
		t.Errorf("exit %d, want 1; stderr %q", code, errb.String())
	}
}

// TestVersion: every spelling prints the binary's identity on stdout,
// naming the running toolchain, and exits 0 (#70).
func TestVersion(t *testing.T) {
	for _, spelling := range []string{"version", "-version", "--version", "-v"} {
		var out, errb bytes.Buffer
		if code := Run([]string{spelling}, &out, &errb); code != 0 || errb.Len() != 0 {
			t.Errorf("%s: exit %d, stderr %q", spelling, code, errb.String())
		}
		if s := out.String(); !strings.HasPrefix(s, "alidade ") || !strings.Contains(s, runtime.Version()) || !strings.HasSuffix(s, ")\n") {
			t.Errorf("%s printed %q", spelling, s)
		}
	}
}
