// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// lookPath and executable are indirections for tests.
var (
	lookPath   = exec.LookPath
	executable = os.Executable
	runCommand = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
)

// setup registers alidade with Claude Code through `claude mcp add`, or
// prints the configuration any MCP client takes. The server is registered
// as `alidade serve -root .`: Claude Code starts a stdio server in the
// project directory, so one user-scope registration serves every
// repository (measured on 2026-09-10, see #38).
//
// A schema root (#47) is a property of one repository, so -schema-root
// implies project scope — the registration lands in that repository's
// .mcp.json — and is refused with user scope, which would push the same
// schema root on every repository. The path is passed as given: a
// relative one resolves from the repository when the server starts
// there, which is what a committed .mcp.json wants (#53).
func setup(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("alidade setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	scope := fs.String("scope", "", "user or project")
	print := fs.Bool("print", false, "print the configuration instead of registering")
	enforceOn := fs.Bool("enforce", false, "also add alidade's grep hook to ~/.claude/settings.json")
	enforceOff := fs.Bool("no-enforce", false, "remove alidade's grep hook from ~/.claude/settings.json, and nothing else")
	var schemaRoots []string
	fs.Func("schema-root", "a repository whose .proto files this one's generated code comes from (repeatable; implies -scope project)", func(v string) error {
		schemaRoots = append(schemaRoots, v)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		// "alidade setup project" would otherwise register at user scope
		// (#100).
		fmt.Fprintf(stderr, "alidade setup: unexpected argument %q; the scope is given with -scope\n", fs.Arg(0))
		return 2
	}
	if code, ok := checkScope(scope, schemaRoots, stderr); !ok {
		return code
	}
	exe := serverPath()
	if code, done := setupEnforce(*enforceOn, *enforceOff, *print, exe, stdout, stderr); done {
		return code
	}
	serve := serveArgs(schemaRoots)
	claude, err := lookPath("claude")
	if *print || err != nil {
		if err != nil && !*print {
			fmt.Fprintln(stderr, "alidade setup: claude is not on PATH; add this to your MCP client's configuration:")
		}
		fmt.Fprintln(stdout, clientConfig(exe, serve))
		return 0
	}
	if code := register(claude, *scope, exe, serve, stdout, stderr); code != 0 || !*enforceOn {
		return code
	}
	return enforce(true, exe, stdout, stderr)
}

// checkScope settles -scope: user unless a schema root asks for project,
// and only those two.
func checkScope(scope *string, schemaRoots []string, stderr io.Writer) (int, bool) {
	switch {
	case *scope == "" && len(schemaRoots) > 0:
		*scope = "project"
	case *scope == "":
		*scope = "user"
	case *scope != "user" && *scope != "project":
		fmt.Fprintf(stderr, "alidade setup: -scope must be user or project, not %q\n", *scope)
		return 2, false
	case *scope == "user" && len(schemaRoots) > 0:
		fmt.Fprintln(stderr, "alidade setup: -schema-root needs -scope project: a user-scope registration serves every repository, and a schema root belongs to one")
		return 2, false
	}
	return 0, true
}

// setupEnforce handles the flags that finish setup without registering:
// -no-enforce, and -enforce with -print (#208).
func setupEnforce(on, off, print bool, exe string, stdout, stderr io.Writer) (int, bool) {
	switch {
	case on && off:
		fmt.Fprintln(stderr, "alidade setup: -enforce and -no-enforce contradict each other")
		return 2, true
	case off:
		return enforce(false, exe, stdout, stderr), true
	case on && print:
		b, _ := json.MarshalIndent(map[string]any{"hooks": map[string]any{"PreToolUse": hookEntries(exe)}}, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return 0, true
	}
	return 0, false
}

// register makes the registration so: one already in this scope is
// replaced, so an upgrade or a changed schema root is one command (#65).
// A remove that finds nothing is not an error.
func register(claude, scope, exe string, serve []string, stdout, stderr io.Writer) int {
	verb := "registered"
	removed := false
	if _, err := runCommand(claude, "mcp", "remove", "alidade", "-s", scope); err == nil {
		verb, removed = "re-registered", true
	}
	cmd := append([]string{"mcp", "add", "--scope", scope, "alidade", "--", exe}, serve...)
	out, err := runCommand(claude, cmd...)
	if err != nil {
		fmt.Fprintf(stderr, "alidade setup: claude mcp add failed: %v\n%s", err, out)
		if removed {
			// The registration this replaced is gone; say so, rather
			// than leave the caller believing the old one still serves
			// (#100).
			fmt.Fprintf(stderr, "alidade setup: the previous alidade registration (%s scope) was removed first, so none is registered now; fix the error above and run setup again\n", scope)
		}
		return 1
	}
	fmt.Fprintf(stdout, "%s alidade with Claude Code (%s scope) as %s %s\n%s", verb, scope, exe, strings.Join(serve, " "), out)
	return 0
}

// serveArgs is the server's command line after the executable.
func serveArgs(schemaRoots []string) []string {
	args := []string{"serve", "-root", "."}
	for _, r := range schemaRoots {
		args = append(args, "-schema-root", r)
	}
	return args
}

// serverPath is the path to register: alidade as found on PATH, which is
// the stable symlink under a package manager, else this executable.
func serverPath() string {
	if p, err := lookPath("alidade"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	if p, err := executable(); err == nil {
		return p
	}
	return "alidade"
}

// clientConfig is the JSON any MCP client takes for this server.
func clientConfig(exe string, args []string) string {
	cfg := map[string]any{"mcpServers": map[string]any{"alidade": map[string]any{
		"command": exe, "args": args,
	}}}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return string(b)
}
