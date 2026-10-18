// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import (
	"os"
	"path/filepath"
	"slices"
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
	linux, amd := config{"linux", "amd64", ""}, config{"linux", "arm64", ""}
	win, mac := config{"windows", "arm64", ""}, config{"darwin", "arm64", ""}
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
		{"both_amd64.go", "//go:build !windows\n\npackage p\n", map[config]bool{linux: true, {"windows", "amd64", ""}: false, amd: false}},
		{"late.go", "package p\n\n//go:build windows\n", map[config]bool{linux: true}},
		// Every GOARCH go/build knows ends a file name as a constraint.
		{"x_armbe.go", "package p\n", map[config]bool{linux: false, amd: false}},
		{"x_mips64p32le.go", "package p\n", map[config]bool{linux: false}},
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

var host = config{"darwin", "arm64", ""}

func TestChooseConfigsHostOnly(t *testing.T) {
	_, files := constraintFiles(t)
	configs, covered, unread, err := chooseConfigs("", host, files, false)
	if err != nil || len(configs) != 0 || len(covered) != 0 || len(unread) != 0 {
		t.Errorf("mode \"\": configs %v covered %v err %v", configs, covered, err)
	}
	if _, _, _, err := chooseConfigs("linux", host, files, false); err == nil || !strings.Contains(err.Error(), "GOOS/GOARCH") {
		t.Errorf("malformed list: err = %v", err)
	}
}

// TestChooseConfigsAuto: linux/amd64 covers a and b (two files), then a
// windows configuration covers c; d and e no candidate satisfies.
func TestChooseConfigsAuto(t *testing.T) {
	dir, files := constraintFiles(t)
	configs, covered, _, err := chooseConfigs("auto", host, files, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 2 || configs[0] != (config{"linux", "amd64", ""}) || configs[1].goos != "windows" {
		t.Errorf("auto configs = %v", configs)
	}
	if len(covered) != 3 || covered[filepath.Join(dir, "b.go")] != (config{"linux", "amd64", ""}) || covered[filepath.Join(dir, "c.go")].goos != "windows" {
		t.Errorf("auto covered = %v", covered)
	}
}

func TestChooseConfigsExplicit(t *testing.T) {
	_, files := constraintFiles(t)
	configs, covered, _, err := chooseConfigs("windows/arm64, darwin/arm64", host, files, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs[0] != (config{"windows", "arm64", ""}) || len(covered) != 1 {
		t.Errorf("explicit: configs %v covered %v (the host is skipped, not loaded twice)", configs, covered)
	}
	if got := configNames(host, configs); strings.Join(got, ",") != "darwin/arm64,windows/arm64" {
		t.Errorf("configNames = %v", got)
	}
}

// TestChooseConfigsGaining: under Options.Only a worktree's packages are
// loaded under the base's configurations, and only those that bring in
// a file of theirs (#35): here linux/arm64 brings in none that
// linux/amd64 did not.
func TestChooseConfigsGaining(t *testing.T) {
	_, files := constraintFiles(t)
	configs, _, _, err := chooseConfigs("linux/amd64,linux/arm64,windows/arm64", host, files, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := configNames(host, configs); strings.Join(got, ",") != "darwin/arm64,linux/amd64,windows/arm64" {
		t.Errorf("gaining: %v", got)
	}
	if all, _, _, _ := chooseConfigs("linux/amd64,linux/arm64,windows/arm64", host, files, false); len(all) != 3 {
		t.Errorf("not gaining: %v, want every one listed", all)
	}
}

// TestConfigTags pins #48's configuration syntax: build tags follow the
// platform, joined by "+", sorted, and name the configuration as written
// back; an empty tag is an error.
func TestConfigTags(t *testing.T) {
	c, err := parseConfig(" linux/amd64+mobile+gl ")
	if err != nil || c != (config{"linux", "amd64", "gl,mobile"}) || c.String() != "linux/amd64+gl+mobile" {
		t.Errorf("parseConfig = %+v %q, %v", c, c.String(), err)
	}
	if !slices.Equal(c.buildTags(), []string{"gl", "mobile"}) || (config{"linux", "amd64", ""}).buildTags() != nil {
		t.Errorf("buildTags = %q", c.buildTags())
	}
	for _, bad := range []string{"linux/amd64+", "linux/amd64+gl++x", "linux+gl"} {
		if _, err := parseConfig(bad); err == nil {
			t.Errorf("parseConfig(%q) took it", bad)
		}
	}
}

// TestAutoChoosesTagsAndMorePlatforms pins #48 (the owner's decision):
// auto's candidates are the platforms, now with js/wasm, wasip1/wasm,
// android/arm64 and ios/arm64, and each platform with every build tag the
// excluded files name set at once. A file a tag brings in, a file only the
// browser builds, and one the tag pass reads beside its !tag variant are
// covered; release tags, unix and the platform words are no tags.
func TestAutoChoosesTagsAndMorePlatforms(t *testing.T) {
	dir := t.TempDir()
	var files []string
	for name, body := range map[string]string{
		"gl.go":      "//go:build gl && unix && go1.21 && !amd64.v3 && !arm64be\n\npackage p\n",
		"mobile.go":  "//go:build mobile || ios\n\npackage p\n",
		"browser.go": "//go:build js && wasm\n\npackage p\n",
		"nine.go":    "//go:build plan9\n\npackage p\n",
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	cv := readConstraints(files)
	if got := tagsOf(cv); !slices.Equal(got, []string{"gl", "mobile"}) {
		t.Errorf("tagsOf = %q, want gl and mobile", got)
	}
	configs, covered, _, err := chooseConfigs("auto", host, files, false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range configs {
		names = append(names, c.String())
	}
	if len(covered) != 3 || covered[filepath.Join(dir, "nine.go")] != (config{}) {
		t.Errorf("covered %v by %q: want all but nine.go", covered, names)
	}
	if !slices.ContainsFunc(configs, func(c config) bool { return c.tags == "gl,mobile" }) ||
		!slices.ContainsFunc(configs, func(c config) bool { return c.goos == "js" || c.goos == "ios" }) {
		t.Errorf("configs %q: want a configuration with gl and mobile set, and the browser's or iOS's", names)
	}
}
