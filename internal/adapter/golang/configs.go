// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import (
	"bufio"
	"fmt"
	"go/build"
	"go/build/constraint"
	"maps"
	"math/bits"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// A config is one GOOS/GOARCH pair the packages can be loaded for, and
// the build tags set for the load, sorted and comma-separated, "" for
// none (#48).
type config struct{ goos, goarch, tags string }

// String names the configuration as -go-configs and a node's build
// attribute do: "linux/amd64", or with tags "linux/amd64+gl+mobile".
func (c config) String() string {
	if c.tags != "" {
		return c.goos + "/" + c.goarch + "+" + strings.ReplaceAll(c.tags, ",", "+")
	}
	return c.goos + "/" + c.goarch
}

// env is the load's environment for the configuration's platform.
func (c config) env() []string { return []string{"GOOS=" + c.goos, "GOARCH=" + c.goarch} }

// buildTags are the tags the load sets, nil for none.
func (c config) buildTags() []string {
	if c.tags == "" {
		return nil
	}
	return strings.Split(c.tags, ",")
}

// hasTag reports whether the load sets build tag t. It is asked once a
// tag per file per candidate, so it reads c.tags in place.
func (c config) hasTag(t string) bool {
	for s := range strings.SplitSeq(c.tags, ",") {
		if s == t {
			return true
		}
	}
	return false
}

// isOS reports whether the configuration builds the files of GOOS goos:
// its own, and for android linux's, for ios darwin's and for illumos
// solaris's, as go/build's matchTag has it.
func (c config) isOS(goos string) bool { return goos == c.goos || impliedOS[c.goos] == goos }

// impliedOS are the GOOS values go/build takes another GOOS's files for.
var impliedOS = map[string]string{"android": "linux", "ios": "darwin", "illumos": "solaris"}

// withTags is the configuration with these build tags set, sorted.
func (c config) withTags(tags []string) config {
	tags = slices.Clone(tags)
	slices.Sort(tags)
	c.tags = strings.Join(slices.Compact(tags), ",")
	return c
}

// parseConfig reads "linux/amd64", or "linux/amd64+gl+mobile" for a
// load with build tags (#48).
func parseConfig(s string) (config, error) {
	platform, tags, hasTags := strings.Cut(strings.TrimSpace(s), "+")
	c, err := parsePlatform(platform)
	if err != nil || !hasTags {
		return c, err
	}
	list := strings.Split(tags, "+")
	for _, t := range list {
		if t == "" {
			return config{}, fmt.Errorf("golang: configuration %q has an empty build tag", s)
		}
		if !validTag(t) {
			return config{}, fmt.Errorf("golang: configuration %q: %q is not a build tag", s, t)
		}
	}
	return c.withTags(list), nil
}

// validTag reports whether t is a word a //go:build line can name:
// letters, digits, '_' and '.', as go/build/constraint reads one.
func validTag(t string) bool {
	for _, r := range t {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func parsePlatform(s string) (config, error) {
	goos, goarch, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok || goos == "" || goarch == "" || strings.Contains(goarch, "/") {
		return config{}, fmt.Errorf("golang: configuration %q is not GOOS/GOARCH", s)
	}
	return config{goos, goarch, ""}, nil
}

// platforms are the GOOS/GOARCH pairs "auto" chooses among, beyond the
// host's: the platforms the repositories here build for, the desktop
// ones and, since #48, the browser's, WASI's and the mobile ones. A file
// whose constraint none of them satisfies, with or without the build
// tags found, stays excluded and is reported so.
var platforms = []config{
	{"linux", "amd64", ""}, {"linux", "arm64", ""},
	{"darwin", "amd64", ""}, {"darwin", "arm64", ""},
	{"windows", "amd64", ""}, {"windows", "arm64", ""},
	{"js", "wasm", ""}, {"wasip1", "wasm", ""},
	{"android", "arm64", ""}, {"ios", "arm64", ""},
}

// candidates are the configurations "auto" chooses among: each platform,
// each platform with every build tag the excluded files' constraints
// name set at once (#48, the owner's decision), and each platform with
// the fewest of an excluded file's own tags it needs (#51, the owner's
// decision). One load with all the tags reads, on belluikit/bellui, every
// declaration a load per tag does, in 11 loads against 29: a file a tag
// excludes (//go:build !gl) is read by the load without tags already, and
// each further load adds only what the ones before it did not. A file
// needing one tag set and another unset (//go:build wayland && !x11) is
// read by neither; its own candidate reads it, and on bellui reads 9
// such files in 13 loads against 11.
func candidates(cv *coverage) []config {
	tags := tagsOf(cv.cons)
	out := slices.Clone(platforms)
	if len(tags) > 0 {
		for _, p := range platforms {
			out = append(out, p.withTags(tags))
		}
	}
	seen := map[config]bool{}
	for _, f := range slices.Sorted(maps.Keys(cv.cons)) {
		for _, p := range platforms {
			if own, ok := cv.cons[f].fewestTags(p); ok && len(own) > 0 && !seen[p.withTags(own)] {
				seen[p.withTags(own)] = true
				out = append(out, p.withTags(own))
			}
		}
	}
	return out
}

// maxOwnTags bounds the tags of one file fewestTags tries each subset of:
// 2^8 evaluations a platform. A file naming more is tried with all of
// them set only.
const maxOwnTags = 8

// fewestTags is the smallest set of the build tags the file's constraint
// names under which the platform includes it, and whether there is one;
// of sets as small, the first in the order of the sorted tags (#51).
func (fc fileConstraint) fewestTags(p config) ([]string, bool) {
	tags := tagsOf(map[string]fileConstraint{"": fc})
	if len(tags) > maxOwnTags {
		if fc.satisfied(p.withTags(tags)) {
			return tags, true
		}
		return nil, false
	}
	var best []string
	found := false
	for mask := range 1 << len(tags) {
		if found && bits.OnesCount(uint(mask)) >= len(best) {
			continue
		}
		var set []string
		for i, t := range tags {
			if mask&(1<<i) != 0 {
				set = append(set, t)
			}
		}
		if fc.satisfied(p.withTags(set)) {
			best, found = set, true
		}
	}
	return best, found
}

// tagsOf are the build tags the files' constraints name, sorted: every
// one but a GOOS, a GOARCH, a release tag, and those a load sets or never
// can — unix, gc, cgo, gccgo, ignore. Both what auto loads and the reason
// a skipped directory gives are read from it.
func tagsOf(cons map[string]fileConstraint) []string {
	seen := map[string]bool{}
	for _, fc := range cons {
		if fc.expr != nil {
			collectTags(fc.expr, seen)
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

func collectTags(e constraint.Expr, seen map[string]bool) {
	switch e := e.(type) {
	case *constraint.TagExpr:
		t := e.Tag
		if t == "boringcrypto" {
			t = "goexperiment.boringcrypto" // go/build's old name for it
		}
		if !knownOS[t] && !knownArch[t] && !nonTags[t] && !releaseRE.MatchString(t) && !archLevel(t) {
			seen[t] = true
		}
	case *constraint.NotExpr:
		collectTags(e.X, seen)
	case *constraint.AndExpr:
		collectTags(e.X, seen)
		collectTags(e.Y, seen)
	case *constraint.OrExpr:
		collectTags(e.X, seen)
		collectTags(e.Y, seen)
	}
}

// archLevel reports whether a constraint word is a GOARCH's
// microarchitecture level — amd64.v3, arm64.v8.1 — which the go command
// sets from GOAMD64 and its kin, not from -tags.
func archLevel(t string) bool {
	arch, _, ok := strings.Cut(t, ".")
	return ok && knownArch[arch]
}

// nonTags are the constraint words that are not tags a load can set.
var nonTags = map[string]bool{"unix": true, "gc": true, "gccgo": true, "cgo": true, "ignore": true}

var unixOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true, "hurd": true,
	"illumos": true, "ios": true, "linux": true, "netbsd": true, "openbsd": true, "solaris": true,
}

var knownOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true, "hurd": true,
	"illumos": true, "ios": true, "js": true, "linux": true, "nacl": true, "netbsd": true, "openbsd": true,
	"plan9": true, "solaris": true, "wasip1": true, "windows": true, "zos": true,
}

// knownArch are the GOARCH values go/build knows, as its internal/syslist
// lists them: one ending a file name constrains it, and none is a build
// tag a load can set.
var knownArch = map[string]bool{
	"386": true, "amd64": true, "amd64p32": true, "arm": true, "armbe": true, "arm64": true, "arm64be": true,
	"loong64": true, "mips": true, "mipsle": true, "mips64": true, "mips64le": true, "mips64p32": true,
	"mips64p32le": true, "ppc": true, "ppc64": true, "ppc64le": true, "riscv": true, "riscv64": true,
	"s390": true, "s390x": true, "sparc": true, "sparc64": true, "wasm": true,
}

// fileConstraint is what decides whether one file is in a configuration:
// its "//go:build" expression, if any, and the GOOS/GOARCH its name
// carries, if any — the two mechanisms go/build applies.
type fileConstraint struct {
	expr         constraint.Expr // nil when the file has no //go:build line
	goos, goarch string          // from the file name, "" when absent
	// release are the go1.N tags of the toolchain that loads the file's
	// module (#112).
	release map[string]bool
}

// toolchains caches the "go1.N" release tags of the toolchain that loads
// each module, by module root. The tags of the toolchain alidade was built
// with (build.Default.ReleaseTags) are not those: packages.Load shells out
// to the host's go command, which may be older than the toolchain that
// built the binary (a binary older than the go command is refused by
// caliper before any load), and a module's toolchain line can switch it.
// A binary built with go1.27 on a go1.26 host read a //go:build go1.27
// file as one the host loads, and never looked for it elsewhere (#112).
var toolchains = struct {
	sync.Mutex
	roots map[string]string          // directory -> its module root
	tags  map[string]map[string]bool // module root -> release tags
}{roots: map[string]string{}, tags: map[string]map[string]bool{}}

// goVersion reports the version of the go command that loads in dir, as
// `go env GOVERSION` there says: "go1.26.1", "go1.27rc1", "devel
// go1.28-…". A variable, so a test can be the toolchain.
var goVersion = func(dir string) (string, error) {
	cmd := exec.Command("go", "env", "GOVERSION")
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

var minorRE = regexp.MustCompile(`go1\.(\d+)`)

// releaseRE is a release tag, go1.N, and only that: a custom tag such as
// cargo1.5 is not one.
var releaseRE = regexp.MustCompile(`^go1\.\d+$`)

// releaseTagsFor returns the release tags true for the toolchain loading
// the module dir is in: go1.1 through go1.N for its go1.N. When the go
// command cannot say, the tags of the toolchain alidade was built with
// stand in; a load would not get far either.
func releaseTagsFor(dir string) map[string]bool {
	toolchains.Lock()
	defer toolchains.Unlock()
	root := moduleRootLocked(dir)
	if tags, ok := toolchains.tags[root]; ok {
		return tags
	}
	tags := map[string]bool{}
	v, err := goVersion(root)
	m := minorRE.FindStringSubmatch(v)
	if err != nil || m == nil {
		for _, t := range build.Default.ReleaseTags {
			tags[t] = true
		}
	} else {
		n, _ := strconv.Atoi(m[1])
		for i := 1; i <= n; i++ {
			tags["go1."+strconv.Itoa(i)] = true
		}
	}
	toolchains.tags[root] = tags
	return tags
}

// moduleRootLocked returns the nearest directory at or above dir holding
// a go.mod, or dir itself when there is none.
func moduleRootLocked(dir string) string {
	if root, ok := toolchains.roots[dir]; ok {
		return root
	}
	root := dir
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			root = d
			break
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	toolchains.roots[dir] = root
	return root
}

// satisfied reports whether the configuration includes the file. Tags
// true for a configuration are its GOOS and GOARCH, the GOOS it implies
// (linux for android, darwin for ios), "unix" for a Unix GOOS, "gc", the
// build tags it sets (#48), and a "go1.N" release tag the loading
// toolchain has reached (#95, #112); every other tag — a custom one the
// configuration does not set, "cgo", another platform — is false, which
// is how the load treats them here. A file name's GOOS is matched the
// same way, as go/build does.
func (fc fileConstraint) satisfied(c config) bool {
	if fc.goos != "" && !c.isOS(fc.goos) || fc.goarch != "" && fc.goarch != c.goarch {
		return false
	}
	if fc.expr == nil {
		return true
	}
	return fc.expr.Eval(func(tag string) bool {
		switch {
		case tag == c.goarch || tag == "gc" || c.isOS(tag):
			return true
		case tag == "unix":
			return unixOS[c.goos]
		case tag == "boringcrypto":
			tag = "goexperiment.boringcrypto"
		}
		return c.hasTag(tag) || fc.release[tag]
	})
}

// readConstraint reads a file's constraint: the first "//go:build" line
// before the package clause, and the name's suffixes.
func readConstraint(path string) (fileConstraint, error) {
	fc := nameConstraint(filepath.Base(path))
	fc.release = releaseTagsFor(filepath.Dir(path))
	f, err := os.Open(path)
	if err != nil {
		return fc, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if constraint.IsGoBuild(line) {
			expr, err := constraint.Parse(line)
			if err != nil {
				return fc, fmt.Errorf("%s: %w", path, err)
			}
			fc.expr = expr
			return fc, nil
		}
		if strings.HasPrefix(line, "package ") {
			break
		}
	}
	return fc, sc.Err()
}

// nameConstraint reads the GOOS and GOARCH a file name ends in:
// x_linux.go, x_amd64.go, x_linux_amd64.go, with a _test dropped first.
// The first segment is never a constraint (linux.go is unconstrained),
// as in go/build.
func nameConstraint(base string) fileConstraint {
	name := strings.TrimSuffix(strings.TrimSuffix(base, ".go"), "_test")
	parts := strings.Split(name, "_")
	var fc fileConstraint
	if len(parts) < 2 {
		return fc
	}
	last := parts[len(parts)-1]
	switch {
	case len(parts) >= 3 && knownOS[parts[len(parts)-2]] && knownArch[last]:
		fc.goos, fc.goarch = parts[len(parts)-2], last
	case knownOS[last]:
		fc.goos = last
	case knownArch[last]:
		fc.goarch = last
	}
	return fc
}

// A coverage tracks which excluded files a chosen configuration brings
// in, and which files it could not read a constraint from at all.
type coverage struct {
	cons    map[string]fileConstraint
	covered map[string]config
	unread  map[string]error
}

// readConstraints reads each file's constraint. A file it cannot open, or
// whose "//go:build" line will not parse, is left out and recorded in
// unread rather than failing: these paths come from a directory walk, not
// from the load, so one unreadable file must cost one fact and never the
// whole layer (#92); the build reports each one skipped.
func readConstraints(files []string) *coverage {
	cv := &coverage{cons: map[string]fileConstraint{}, covered: map[string]config{}, unread: map[string]error{}}
	for _, f := range files {
		fc, err := readConstraint(f)
		if err != nil {
			cv.unread[f] = err
			continue
		}
		cv.cons[f] = fc
	}
	return cv
}

// gain counts the files c would bring in that nothing has yet.
func (cv *coverage) gain(c config) int {
	n := 0
	for f, fc := range cv.cons {
		if _, done := cv.covered[f]; !done && fc.satisfied(c) {
			n++
		}
	}
	return n
}

// take records c as the configuration covering every file it satisfies.
func (cv *coverage) take(c config) {
	for f, fc := range cv.cons {
		if _, done := cv.covered[f]; !done && fc.satisfied(c) {
			cv.covered[f] = c
		}
	}
}

// chooseConfigs decides which configurations beyond the host's to load,
// and which of the host's excluded files each brings in. mode is "" for
// none, "auto" to pick from candidates greedily until no candidate
// covers a further file, or a comma-separated list of GOOS/GOARCH, each
// with build tags if any (linux/amd64+gl), to take as given; with
// gaining, a listed one that brings in no further file is left out (#35). covered maps a file to the configuration that includes
// it, and unread the files no constraint could be read from, each with
// why. Only a malformed mode is an error.
func chooseConfigs(mode string, host config, excluded []string, gaining bool) (configs []config, covered map[string]config, unread map[string]error, err error) {
	if mode == "" || len(excluded) == 0 && mode == "auto" {
		return nil, map[string]config{}, map[string]error{}, nil
	}
	cv := readConstraints(excluded)
	if mode == "auto" {
		configs = autoConfigs(cv, host)
	} else if configs, err = listedConfigs(cv, host, mode, gaining); err != nil {
		return nil, nil, nil, err
	}
	return configs, cv.covered, cv.unread, nil
}

// autoConfigs is the greedy cover: the candidate bringing in the most
// uncovered files, repeated until none brings in any.
func autoConfigs(cv *coverage, host config) []config {
	cands := candidates(cv)
	var configs []config
	chosen := map[config]bool{host: true}
	for {
		best, bestN := config{}, 0
		for _, c := range cands {
			if n := cv.gain(c); !chosen[c] && n > bestN {
				best, bestN = c, n
			}
		}
		if bestN == 0 {
			return configs
		}
		chosen[best] = true
		configs = append(configs, best)
		cv.take(best)
	}
}

// listedConfigs takes the configurations as named, the host's skipped,
// and with gaining those that bring in no file the earlier ones did not.
func listedConfigs(cv *coverage, host config, list string, gaining bool) ([]config, error) {
	var configs []config
	for _, s := range strings.Split(list, ",") {
		c, err := parseConfig(s)
		if err != nil {
			return nil, err
		}
		if c != host && (!gaining || cv.gain(c) > 0) {
			configs = append(configs, c)
			cv.take(c)
		}
	}
	return configs, nil
}

// configNames renders configurations for a report, host first.
func configNames(host config, extra []config) []string {
	out := []string{host.String()}
	for _, c := range extra {
		out = append(out, c.String())
	}
	return out
}

// SameAutoChoice reports whether -go-configs auto chooses, from a
// worktree's host-excluded files relative to its root, the configurations
// base was built under, in the same order (#39). The order is the choice
// too: a file two configurations include is credited to the one chosen
// first. base's own files are not read again: what its graph was built
// under is what it recorded then, whatever its checkout holds now. tests
// says which of base's loads to compare with: under ExactTests the load
// with tests chose its own configurations, and the load without them
// chose from the same files less their _test.go files (#43 asks for each
// as that load is made).
func SameAutoChoice(base *Report, tests bool, root string, files []string) bool {
	host := hostConfig()
	var abs []string
	for _, f := range files {
		if tests || !strings.HasSuffix(f, "_test.go") {
			abs = append(abs, filepath.Join(root, filepath.FromSlash(f)))
		}
	}
	want := base.Configs
	if tests && base.ExactTests {
		want = base.testConfigs
	}
	return slices.Equal(configNames(host, autoConfigs(readConstraints(abs), host)), want)
}

// relativeTo names each file by its path relative to root, slash
// separated; one outside the root is left out.
func relativeTo(root string, files []string) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		if rel, err := filepath.Rel(root, f); err == nil && filepath.IsLocal(rel) {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out
}
