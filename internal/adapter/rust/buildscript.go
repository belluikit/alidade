// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// buildScripts starts rust-analyzer's server on each workspace with a
// build script that no step has asked it about yet, so that a script that
// fails is reported (#85, the owner's decision): the index says nothing
// when one does, and the code it would have written is simply missing.
// A server costs about 3.4 s on prost-reflect; this pays it only where no
// other step has started one.
// What the servers said is read when they are closed (reportBuild).
func (x *extractor) buildScripts() error {
	for _, ws := range x.indexed {
		if slices.ContainsFunc(x.manifests[ws], hasBuildScript) {
			x.server(ws)
		}
	}
	return nil
}

var buildKey = regexp.MustCompile(`(?m)^\s*build\s*=\s*(false|"[^"]*")`)

// hasBuildScript says whether the package of a Cargo manifest has a build
// script, as Cargo decides: a build.rs beside the manifest unless the
// manifest says `build = false`, or the script it names.
func hasBuildScript(manifest string) bool {
	text, err := os.ReadFile(manifest)
	if err != nil {
		return false
	}
	if m := buildKey.FindSubmatch(text); m != nil {
		return string(m[1]) != "false"
	}
	_, err = os.Stat(filepath.Join(filepath.Dir(manifest), "build.rs"))
	return err == nil
}

// reportBuild reports each package of a workspace whose build script
// failed, with what the script printed: from the server's log, which
// names them (`failed to run custom build command for `a v0.1.0 (…)“).
// That is log text, not an API, so when no such entry is there and the
// server's status still says something went wrong, the status message is
// reported alone, on the workspace. Each package's entry is at its
// manifest, and says nothing that differs between two builds of one tree:
// no absolute path, no build hash, no thread id, and they are in the order
// of their packages, where the log's order varies from run to run.
func (x *extractor) reportBuild(ws string, l *lsp) {
	failed := buildFailures(l.stderr.String())
	slices.SortFunc(failed, func(a, b buildFailure) int { return strings.Compare(a.pkg, b.pkg) })
	for _, f := range failed {
		x.report.BuildScriptsFailed++
		x.skip(filepath.Join(f.dir, "Cargo.toml"), 0, errors.New(f.reason(x.root)))
	}
	if len(failed) == 0 && l.health != "" && l.health != "ok" && l.status != "" {
		x.skip(ws, 0, fmt.Errorf("rust-analyzer's server reported %s: %s", l.health, firstParagraph(l.status)))
	}
}

// A buildFailure is one package whose build script failed, as the log
// says: its name and version, its directory, how the script exited, and
// what it printed.
type buildFailure struct {
	pkg, dir, exit string
	stdout, stderr []string
	// trace says a backtrace is being read, which a reason leaves out.
	trace bool
}

var (
	failedRE  = regexp.MustCompile("^(?:.*ERROR FetchBuildDataError: )?error: failed to run custom build command for `([^ `]+ [^ `]+) \\(([^)`]*)\\)`")
	logLineRE = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\S+\s+(TRACE|DEBUG|INFO|WARN|ERROR) `)
	exitRE    = regexp.MustCompile(`didn't exit successfully: .*\((exit status: \d+|signal: [^)]*)\)`)
	frameRE   = regexp.MustCompile(`^(\d+: |at \S+:\d+(:\d+)?$)`)
	threadRE  = regexp.MustCompile(`thread '([^']*)' \(\d+\)`)
)

// buildFailures reads the packages whose build scripts failed from the
// server's log, in the order it names them: one entry can name several,
// each from its `error: failed to run custom build command` line to the
// next such line or the next entry.
func buildFailures(log string) []buildFailure {
	var out []buildFailure
	var cur *buildFailure
	stream := ""
	for _, line := range strings.Split(log, "\n") {
		if m := failedRE.FindStringSubmatch(line); m != nil {
			out = append(out, buildFailure{pkg: m[1], dir: m[2]})
			cur, stream = &out[len(out)-1], ""
			continue
		}
		if cur == nil {
			continue
		}
		if logLineRE.MatchString(line) {
			cur = nil
			continue
		}
		stream = cur.read(line, stream)
	}
	return out
}

// read takes one line of a failure's text, in the stream it is in, and
// says which stream the next line is in: "" before `--- stdout`.
func (f *buildFailure) read(line, stream string) string {
	t := strings.TrimSpace(line)
	if m := exitRE.FindStringSubmatch(line); m != nil {
		f.exit = m[1]
	}
	if f.inTrace(t) {
		return stream
	}
	switch {
	case t == "--- stdout", t == "--- stderr":
		return strings.TrimPrefix(t, "--- ")
	case strings.EqualFold(t, "stack backtrace:"):
		f.trace = true // a panic's, or anyhow's "Stack backtrace:"
	case stream == "stdout" && t != "":
		f.stdout = append(f.stdout, t)
	case stream == "stderr" && t != "":
		f.stderr = append(f.stderr, t)
	}
	return stream
}

// inTrace says whether a line is part of the backtrace being read: a
// frame, its location, a blank line, or the note that ends a panic's. The
// first line that is none ends it.
func (f *buildFailure) inTrace(t string) bool {
	if !f.trace {
		return false
	}
	if t == "" || frameRE.MatchString(t) || strings.HasPrefix(t, "note: Some details are omitted") {
		return true
	}
	f.trace = false
	return false
}

// maxOutput bounds the lines of each stream a reason quotes: the last
// ones, where a script that panics says why.
const maxOutput = 10

// reason is the failure as a Skip's reason, root taken off its paths.
func (f buildFailure) reason(root string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "the build script of %s failed", f.pkg)
	if f.exit != "" {
		fmt.Fprintf(&b, " (%s)", f.exit)
	}
	b.WriteString(", so the code it writes has no nodes")
	for _, s := range []struct {
		name  string
		lines []string
	}{{"stdout", f.stdout}, {"stderr", f.stderr}} {
		if len(s.lines) == 0 {
			continue
		}
		lines := s.lines[max(0, len(s.lines)-maxOutput):]
		fmt.Fprintf(&b, "; its %s: %s", s.name, strings.Join(lines, " / "))
	}
	r := strings.ReplaceAll(b.String(), root+string(filepath.Separator), "")
	return threadRE.ReplaceAllString(r, "thread '$1'")
}

// firstParagraph is a message up to its first blank line, on one line.
func firstParagraph(s string) string {
	p, _, _ := strings.Cut(s, "\n\n")
	return strings.Join(strings.Fields(p), " ")
}

// A logBuffer keeps the first maxLog bytes a server writes to its
// standard error, safe to write while it is read: build scripts fail
// while the workspace loads, early in the log.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

const maxLog = 4 << 20

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := maxLog - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
