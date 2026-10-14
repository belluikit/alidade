// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileConstraintSatisfied(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	linux, amd := config{"linux", "amd64"}, config{"linux", "arm64"}
	win, mac := config{"windows", "arm64"}, config{"darwin", "arm64"}
	for _, tt := range []struct {
		file string
		body string
		want map[config]bool
	}{
		{"expr.go", "//go:build linux && amd64\n\npackage p\n", map[config]bool{linux: true, amd: false, win: false, mac: false}},
		{"not.go", "//go:build !windows\n\npackage p\n", map[config]bool{linux: true, win: false, mac: true}},
		{"unix.go", "//go:build unix\n\npackage p\n", map[config]bool{linux: true, mac: true, win: false}},
		{"custom.go", "//go:build plugin || purego\n\npackage p\n", map[config]bool{linux: false, mac: false}},
		{"ignore.go", "//go:build ignore\n\npackage p\n", map[config]bool{linux: false}},
		{"x_windows_arm64.go", "package p\n", map[config]bool{win: true, linux: false, mac: false}},
		{"x_arm64.go", "package p\n", map[config]bool{amd: true, mac: true, linux: false}},
		{"x_darwin_test.go", "package p\n", map[config]bool{mac: true, linux: false}},
		{"linux.go", "package p\n", map[config]bool{win: true, linux: true}},
		{"both_amd64.go", "//go:build !windows\n\npackage p\n", map[config]bool{linux: true, {"windows", "amd64"}: false, amd: false}},
		{"late.go", "package p\n\n//go:build windows\n", map[config]bool{linux: true}},
	} {
		fc, err := readConstraint(write(tt.file, tt.body))
		if err != nil {
			t.Fatal(err)
		}
		for c, want := range tt.want {
			if got := fc.satisfied(c); got != want {
				t.Errorf("%s satisfied by %s = %v, want %v", tt.file, c, got, want)
			}
		}
	}
	if _, err := readConstraint(write("bad.go", "//go:build linux &&\n\npackage p\n")); err == nil {
		t.Error("a malformed constraint parsed")
	}
}

func constraintFiles(t *testing.T) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	var files []string
	for name, body := range map[string]string{
		"a_amd64.go": "package p\n",
		"b.go":       "//go:build linux && amd64\n\npackage p\n",
		"c.go":       "//go:build windows\n\npackage p\n",
		"d.go":       "//go:build plan9\n\npackage p\n",
		"e.go":       "//go:build ignore\n\npackage p\n",
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	return dir, files
}

var host = config{"darwin", "arm64"}

func TestChooseConfigsHostOnly(t *testing.T) {
	_, files := constraintFiles(t)
	configs, covered, unread, err := chooseConfigs("", host, files)
	if err != nil || len(configs) != 0 || len(covered) != 0 || len(unread) != 0 {
		t.Errorf("mode \"\": configs %v covered %v err %v", configs, covered, err)
	}
	if _, _, _, err := chooseConfigs("linux", host, files); err == nil || !strings.Contains(err.Error(), "GOOS/GOARCH") {
		t.Errorf("malformed list: err = %v", err)
	}
}

// TestChooseConfigsAuto: linux/amd64 covers a and b (two files), then a
// windows configuration covers c; d and e no candidate satisfies.
func TestChooseConfigsAuto(t *testing.T) {
	dir, files := constraintFiles(t)
	configs, covered, _, err := chooseConfigs("auto", host, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 2 || configs[0] != (config{"linux", "amd64"}) || configs[1].goos != "windows" {
		t.Errorf("auto configs = %v", configs)
	}
	if len(covered) != 3 || covered[filepath.Join(dir, "b.go")] != (config{"linux", "amd64"}) || covered[filepath.Join(dir, "c.go")].goos != "windows" {
		t.Errorf("auto covered = %v", covered)
	}
}

func TestChooseConfigsExplicit(t *testing.T) {
	_, files := constraintFiles(t)
	configs, covered, _, err := chooseConfigs("windows/arm64, darwin/arm64", host, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs[0] != (config{"windows", "arm64"}) || len(covered) != 1 {
		t.Errorf("explicit: configs %v covered %v (the host is skipped, not loaded twice)", configs, covered)
	}
	if got := configNames(host, configs); strings.Join(got, ",") != "darwin/arm64,windows/arm64" {
		t.Errorf("configNames = %v", got)
	}
}
