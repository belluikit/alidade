// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The layer reads Rust through the toolchain the user's machine has (ADR
// 009): cargo, which rust-analyzer asks for the workspace and has run the
// build scripts, rust-analyzer itself, and rustc for the standard
// library's source, which rust-analyzer needs to type anything that
// touches std: in the sysroot, or where RUST_SRC_PATH says.
var tools = []struct{ name, fix string }{
	{"cargo", "install Rust with rustup (https://rustup.rs)"},
	{"rustc", "install Rust with rustup (https://rustup.rs)"},
	{"rust-analyzer", "run `rustup component add rust-analyzer`"},
}

// toolchainMissing says what of the toolchain is missing when the layer is
// run in dir, and how to add it, or "" when nothing is. A missing standard
// library source counts: without it rust-analyzer loses the type of a
// quarter of all expressions, silently (measured on protowire-rust, ADR
// 009), so the layer reads nothing rather than read that.
func toolchainMissing(dir string) string {
	for _, t := range tools {
		if _, err := output(dir, t.name, "--version"); err != nil {
			return fmt.Sprintf("%s does not run (%v): %s", t.name, err, t.fix)
		}
	}
	if srcPath() != "" {
		return ""
	}
	sysroot, err := output(dir, "rustc", "--print", "sysroot")
	if err != nil {
		return fmt.Sprintf("rustc --print sysroot fails (%v)", err)
	}
	if !isDir(filepath.Join(sysroot, "lib", "rustlib", "src", "rust", "library")) {
		return fmt.Sprintf("the standard library's source is not in %s, and without it rust-analyzer loses the type of a quarter of all expressions: run `rustup component add rust-src`", sysroot)
	}
	return ""
}

// srcPath is the standard library's source as RUST_SRC_PATH names it, or
// "" when it names none. rust-analyzer takes the variable before the
// sysroot when it is an absolute path to a directory holding core, and
// otherwise passes over it (measured on rust-analyzer 1.100, #117): Nix
// and some distributions' packages put the source there.
func srcPath() string {
	p := os.Getenv("RUST_SRC_PATH")
	if !filepath.IsAbs(p) || !isDir(filepath.Join(p, "core")) {
		return ""
	}
	return p
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// output runs a command in dir and returns its standard output, trimmed;
// an error carries the first line of what it printed to standard error.
func output(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if line := firstLine(stderr.String()); line != "" {
			return "", fmt.Errorf("%w: %s", err, line)
		}
		var nf *exec.Error
		if errors.As(err, &nf) {
			return "", fmt.Errorf("%s is not on the PATH", name)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// lastLines returns the last n lines of s, for an error from a tool whose
// output ends with what went wrong.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
