// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

// TestBuildScriptFails reads testdata/buildfail, a workspace whose package
// a has a build script that prints and panics, with a package c added
// whose script exits 3 printing nothing. Each failed package is reported
// at its manifest with how its script exited and what it printed, and
// nothing that differs between two builds; package b beside them is read
// (#85, the owner's decision). The index says nothing of the failures:
// rust-analyzer's server does, which pins that its log names them. The
// server is started for the build scripts alone, and a user's RA_LOG does
// not silence it.
func TestBuildScriptFails(t *testing.T) {
	t.Setenv("RA_LOG", "off")
	dir := fixtureOf(t, "buildfail", map[string]string{
		"Cargo.toml":   "[workspace]\nmembers = [\"a\", \"b\", \"c\"]\nresolver = \"2\"\n",
		"c/Cargo.toml": "[package]\nname = \"c\"\nversion = \"0.2.0\"\nedition = \"2021\"\n",
		"c/build.rs":   "fn main() {\n    std::process::exit(3)\n}\n",
		"c/src/lib.rs": "pub fn c() {}\n",
	})
	g, rep := extract(t, dir)
	if rep.BuildScriptsFailed != 2 {
		t.Errorf("BuildScriptsFailed %d; want 2", rep.BuildScriptsFailed)
	}
	var got []string
	for _, s := range rep.Skipped {
		got = append(got, s.File+": "+s.Reason)
		if strings.Contains(s.Reason, dir) || strings.Contains(s.Reason, "/private/") || strings.Contains(s.Reason, "(1") {
			t.Errorf("reason %q holds what differs between builds: a path or a thread id", s.Reason)
		}
	}
	check(t, "skipped", got, []string{
		"a/Cargo.toml: the build script of a v0.1.0 failed (exit status: 101), so the code it writes has no nodes; " +
			"its stdout: need PROTOBUF; its stderr: protoc not found / thread 'main' panicked at a/build.rs:6:5: / no protoc",
		"c/Cargo.toml: the build script of c v0.2.0 failed (exit status: 3), so the code it writes has no nodes",
	})
	for _, want := range []string{"b/src/lib.rs#b() -> b/src/lib.rs#helper()"} {
		if !slices.Contains(edges(g, graph.KindCalls), want) {
			t.Errorf("calls %v; want %s: the package beside the failed ones is read", edges(g, graph.KindCalls), want)
		}
	}
	if !slices.Contains(nodes(g), "a/src/lib.rs#a() func") {
		t.Errorf("nodes %v; want a's own a(), read though its build script failed", nodes(g))
	}
}

// TestDependencyBuildScriptFails: a dependency from outside the tree
// whose build script fails is reported at the workspace's manifest, by
// package and version, in each of the three shapes Cargo names a package:
// a path's beyond the root, by its directory, written from the root; a
// git repository's, by its URL, its checkout written from $CARGO_HOME;
// and a registry's, by nothing. A directory source stands in for
// crates.io, as `cargo vendor` sets one up (#117).
func TestDependencyBuildScriptFails(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on the PATH")
	}
	base := tempDir(t)
	root := filepath.Join(base, "ws")
	failing := func(name string) map[string]string {
		return map[string]string{
			"Cargo.toml": "[package]\nname = \"" + name + "\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
			"build.rs":   "fn main() {\n    panic!(\"" + name + " fails\");\n}\n",
			"src/lib.rs": "pub fn f() {}\n",
		}
	}
	writeTree(t, filepath.Join(base, "outside", "pathdep"), failing("pathdep"))
	writeTree(t, filepath.Join(base, "gitdep"), failing("gitdep"))
	git := exec.Command("sh", "-c", "git init -q && git add . && git -c user.name=t -c user.email=t@t commit -qm init")
	git.Dir = filepath.Join(base, "gitdep")
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git: %v: %s", err, out)
	}
	writeTree(t, filepath.Join(root, "vendor", "regdep"), failing("regdep"))
	writeTree(t, root, map[string]string{
		"Cargo.toml": "[package]\nname = \"probe\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\n" +
			"pathdep = { path = \"../outside/pathdep\" }\ngitdep = { git = \"file://" + filepath.ToSlash(base) + "/gitdep\" }\nregdep = \"0.1.0\"\n",
		"build.rs":                           "fn main() {}\n", // so that the server is asked
		"src/lib.rs":                         "pub fn probe() {}\n",
		"vendor/regdep/.cargo-checksum.json": `{"files":{},"package":null}`,
		".cargo/config.toml":                 "[source.crates-io]\nreplace-with = \"vendored\"\n\n[source.vendored]\ndirectory = \"vendor\"\n",
	})
	if why := toolchainMissing(root); why != "" {
		t.Skip(why)
	}
	t.Setenv("CARGO_HOME", filepath.Join(base, "cargo-home"))
	_, rep := extract(t, root)
	if rep.BuildScriptsFailed != 3 {
		t.Errorf("BuildScriptsFailed %d; want 3", rep.BuildScriptsFailed)
	}
	var got []string
	for _, s := range rep.Skipped {
		got = append(got, s.File+": "+strings.ReplaceAll(s.Reason, "file://"+filepath.ToSlash(base), "file://BASE"))
	}
	tail := " failed (exit status: 101), so rust-analyzer reads it without the code it writes; its stderr: thread 'main' panicked at "
	want := []*regexp.Regexp{
		regexp.MustCompile(`^Cargo\.toml: the build script of dependency gitdep v0\.1\.0 \(file://BASE/gitdep#[0-9a-f]{8}\)` + regexp.QuoteMeta(tail) +
			`\$CARGO_HOME/git/checkouts/gitdep-[0-9a-f]+/[0-9a-f]+/build\.rs:2:5: / gitdep fails$`),
		regexp.MustCompile("^" + regexp.QuoteMeta("Cargo.toml: the build script of dependency pathdep v0.1.0 (../outside/pathdep)"+tail+"../outside/pathdep/build.rs:2:5: / pathdep fails") + "$"),
		regexp.MustCompile("^" + regexp.QuoteMeta("Cargo.toml: the build script of dependency regdep v0.1.0"+tail+"vendor/regdep/build.rs:2:5: / regdep fails") + "$"),
	}
	if len(got) != len(want) {
		t.Fatalf("skipped\n%s\nwant %d entries", strings.Join(got, "\n"), len(want))
	}
	for i, re := range want {
		if !re.MatchString(got[i]) {
			t.Errorf("skipped[%d]\n%s\nwant it to match\n%s", i, got[i], re)
		}
	}
}

// TestDependencyFailureWhereServerRuns pins where a dependency's failing
// build script is reported (#127, the owner's pick of option b): in every
// workspace a step starts rust-analyzer's server on, since its log is what
// names the failure. A trait is enough for the implementations step to
// start one, with no build script of the workspace's own. A workspace with
// no trait, no include! and no build script starts none, and the failure
// is not reported: the measured exception, documented in ADR 009 and
// docs/usage.md, where a server would cost 17% of tokio's build for two
// fuzz workspaces whose dependency writes no Rust.
func TestDependencyFailureWhereServerRuns(t *testing.T) {
	for _, tc := range []struct {
		name, lib string
		reported  bool
	}{
		{"trait", "pub trait Shape {\n    fn area(&self) -> u32;\n}\n", true},
		{"nothing to ask", "pub fn probe() {}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := tempDir(t)
			root := filepath.Join(base, "ws")
			writeTree(t, filepath.Join(base, "outside", "pathdep"), map[string]string{
				"Cargo.toml": "[package]\nname = \"pathdep\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
				"build.rs":   "fn main() {\n    panic!(\"pathdep fails\");\n}\n",
				"src/lib.rs": "pub fn f() {}\n",
			})
			writeTree(t, root, map[string]string{
				"Cargo.toml": "[package]\nname = \"probe\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\npathdep = { path = \"../outside/pathdep\" }\n",
				"src/lib.rs": tc.lib,
			})
			if why := toolchainMissing(root); why != "" {
				t.Skip(why)
			}
			_, rep := extract(t, root)
			var got []string
			for _, s := range rep.Skipped {
				got = append(got, s.File+": "+s.Reason)
			}
			want := 0
			if tc.reported {
				want = 1
			}
			if rep.BuildScriptsFailed != want || len(got) != want ||
				(tc.reported && !strings.HasPrefix(got[0], "Cargo.toml: the build script of dependency pathdep v0.1.0 (../outside/pathdep) failed")) {
				t.Errorf("BuildScriptsFailed %d, skipped %q; want %d entries naming pathdep", rep.BuildScriptsFailed, got, want)
			}
		})
	}
}

// writeTree writes files, by slash-separated path, under dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestBuildFailuresFallBack: a log with no entry naming a package, as a
// rust-analyzer that words it otherwise would write, reports the server's
// status message on the workspace instead; a healthy one reports nothing.
func TestBuildFailuresFallBack(t *testing.T) {
	x := &extractor{m: graph.NewBuilder("/repo").Minter(), root: "/repo", report: &Report{}}
	x.reportBuild("/repo", &lsp{stderr: &logBuffer{}, health: "warning",
		status: "Failed to run build scripts of some packages.\n\nPlease refer to the language server logs for more details on the errors."})
	x.reportBuild("/repo", &lsp{stderr: &logBuffer{}, health: "ok"})
	if len(x.report.Skipped) != 1 || x.report.Skipped[0].Reason != "rust-analyzer's server reported warning: Failed to run build scripts of some packages." {
		t.Errorf("skipped %+v; want the warning's first paragraph, once", x.report.Skipped)
	}
	if x.report.BuildScriptsFailed != 0 {
		t.Errorf("BuildScriptsFailed %d; want 0: no package was named", x.report.BuildScriptsFailed)
	}
}

// TestHasBuildScript decides as Cargo does: build.rs beside the manifest
// unless `build = false`, or the script `build` names.
func TestHasBuildScript(t *testing.T) {
	for _, c := range []struct {
		name, manifest string
		buildRS, want  bool
	}{
		{"build.rs", "[package]\nname = \"p\"\n", true, true},
		{"none", "[package]\nname = \"p\"\n", false, false},
		{"turned off", "[package]\nname = \"p\"\nbuild = false\n", true, false},
		{"named", "[package]\nname = \"p\"\nbuild = \"src/gen.rs\"\n", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(c.manifest), 0o644); err != nil {
				t.Fatal(err)
			}
			if c.buildRS {
				if err := os.WriteFile(filepath.Join(dir, "build.rs"), []byte("fn main() {}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := hasBuildScript(filepath.Join(dir, "Cargo.toml")); got != c.want {
				t.Errorf("got %v; want %v", got, c.want)
			}
		})
	}
}

// recordedLog is rust-analyzer 1.100's log of TestBuildScriptFails's
// workspace, as measured, its backtrace frames cut: one entry naming both
// packages, c first (the order varies from run to run), and a line of a's
// output holding the workspace's path.
const recordedLog = `2026-11-24T21:49:24.578153-08:00  WARN notify error: No path was found. about ["/ws/rust-analyzer.toml"]
2026-11-24T21:49:46.161943-08:00 ERROR FetchBuildDataError: error: failed to run custom build command for ` + "`c v0.2.0 (/ws/c)`" + `
note: To improve backtraces for build dependencies, set the CARGO_PROFILE_DEV_BUILD_OVERRIDE_DEBUG=true environment variable to enable debug information generation.

Caused by:
  process didn't exit successfully: ` + "`/ws/target/debug/build/c/32fe21c9c9dcea50/out/build_script_build`" + ` (exit status: 3)
error: failed to run custom build command for ` + "`a v0.1.0 (/ws/a)`" + `
note: To improve backtraces for build dependencies, set the CARGO_PROFILE_DEV_BUILD_OVERRIDE_DEBUG=true environment variable to enable debug information generation.

Caused by:
  process didn't exit successfully: ` + "`/ws/target/debug/build/a/935b0e6bb13f6381/out/build_script_build`" + ` (exit status: 101)
  --- stdout
  need PROTOBUF in /ws/a/proto

  --- stderr
  protoc not found

  thread 'main' (151298150) panicked at a/build.rs:6:5:
  no protoc
  stack backtrace:
     0: __rustc::rust_begin_unwind
               at /rustc/0ef1bc386059596dada8fb65b50425aab2ea637a/library/std/src/panicking.rs:676:5
  note: Some details are omitted, run with ` + "`RUST_BACKTRACE=full`" + ` for a verbose backtrace.


2026-11-24T21:49:46.284057-08:00  WARN notify error: No path was found. about ["/ws/rust-analyzer.toml"]
`

// TestBuildFailuresRecorded reads the recorded log: both packages, in
// their packages' order, the workspace's path and the thread id taken out,
// the backtrace left out, and nothing after the entry read as output.
func TestBuildFailuresRecorded(t *testing.T) {
	x := &extractor{m: graph.NewBuilder("/ws").Minter(), root: "/ws", report: &Report{}}
	b := &logBuffer{}
	if _, err := b.Write([]byte(recordedLog)); err != nil {
		t.Fatal(err)
	}
	x.reportBuild("/ws", &lsp{stderr: b, health: "warning", status: "Failed to run build scripts of some packages."})
	var got []string
	for _, s := range x.report.Skipped {
		got = append(got, s.File+": "+s.Reason)
	}
	want := []string{
		"a/Cargo.toml: the build script of a v0.1.0 failed (exit status: 101), so the code it writes has no nodes; " +
			"its stdout: need PROTOBUF in a/proto; its stderr: protoc not found / thread 'main' panicked at a/build.rs:6:5: / no protoc",
		"c/Cargo.toml: the build script of c v0.2.0 failed (exit status: 3), so the code it writes has no nodes",
	}
	if !slices.Equal(got, want) {
		t.Errorf("skipped\n%s\nwant, in this order\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestBuildFailureReasonCapped: a reason quotes the last lines of a
// stream a script printed, where a script that panics says why.
func TestBuildFailureReasonCapped(t *testing.T) {
	f := buildFailure{pkg: "a v0.1.0", dir: "/ws/a", exit: "exit status: 1"}
	for i := range 12 {
		f.stdout = append(f.stdout, "line"+strconv.Itoa(i))
	}
	want := "the build script of a v0.1.0 failed (exit status: 1), so the code it writes has no nodes; its stdout: " +
		"line2 / line3 / line4 / line5 / line6 / line7 / line8 / line9 / line10 / line11"
	if got := f.reason("/ws", ""); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// anyhowLog is prost-reflect's (@f82c954), as measured, its frames cut to
// three: the conformance tests' build script returns an anyhow error,
// whose backtrace has no closing note.
const anyhowLog = `2026-11-24T21:55:24.585119-08:00 ERROR FetchBuildDataError: error: failed to run custom build command for ` + "`prost-reflect-conformance-tests v0.0.0 (/ws/prost-reflect-conformance-tests)`" + `
note: To improve backtraces for build dependencies, set the CARGO_PROFILE_DEV_BUILD_OVERRIDE_DEBUG=true environment variable to enable debug information generation.

Caused by:
  process didn't exit successfully: ` + "`/ws/target/debug/build/prost-reflect-conformance-tests/0a1b/out/build_script_build`" + ` (exit status: 1)
  --- stderr
  Error: protobuf sources are not checked out; Try ` + "`git submodule update --init --recursive`" + `

  Stack backtrace:
     0: std::backtrace_rs::backtrace::libunwind::trace
               at /rustc/0ef1bc386059596dada8fb65b50425aab2ea637a/library/std/src/../../backtrace/src/backtrace/libunwind.rs:117:9
     4: <anyhow::Error>::msg::<&str>
    20: _main



`

// TestBuildFailuresAnyhow: a script returning an error prints its message
// and anyhow's backtrace; the reason has the message alone.
func TestBuildFailuresAnyhow(t *testing.T) {
	got := buildFailures(anyhowLog)
	if len(got) != 1 {
		t.Fatalf("%d failures; want 1", len(got))
	}
	want := "the build script of prost-reflect-conformance-tests v0.0.0 failed (exit status: 1), so the code it writes has no nodes; " +
		"its stderr: Error: protobuf sources are not checked out; Try `git submodule update --init --recursive`"
	if r := got[0].reason("/ws", ""); r != want {
		t.Errorf("got  %q\nwant %q", r, want)
	}
}

// TestBuildFailureAfterTrace: a line that is no frame, location or note
// ends a backtrace, and is read as output again.
func TestBuildFailureAfterTrace(t *testing.T) {
	var f buildFailure
	stream := ""
	for _, line := range []string{"  --- stderr", "  boom", "  stack backtrace:", "     0: main", "        at ./build.rs:3:5", "  after"} {
		stream = f.read(line, stream)
	}
	if !slices.Equal(f.stderr, []string{"boom", "after"}) {
		t.Errorf("stderr %q; want boom, after", f.stderr)
	}
}
