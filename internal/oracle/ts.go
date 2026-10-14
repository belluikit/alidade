// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	tsadapter "github.com/trendvidia/alidade/internal/adapter/ts"
	"github.com/trendvidia/alidade/internal/graph"
)

// The frontend oracle has two halves (#168).
//
// Imports: TypeScript's own module resolution, ts.resolveModuleName with
// each file's tsconfig.json, from the typescript package the repository
// installs. The frontend layer resolves with esbuild's resolver; the two
// share no code. Three kinds of import TypeScript's resolver does not
// answer on its own are answered from TypeScript and Node too (#171):
//
//   - A file with an extension TypeScript does not know, a .svelte
//     component above all: TypeScript resolves "./Row.svelte" to
//     "./Row.d.svelte.ts" under allowArbitraryExtensions, so the script's
//     host reports that declaration file present wherever the file it
//     declares is, and the answer is read back as the file itself.
//   - A Node builtin (node:fs): module.isBuiltin, Node's own list.
//   - A module the program declares ambiently ($app/state, astro:content):
//     the type checker's ambient modules, for the tsconfig the file has.
//
// The second and third are external by construction: what they check is
// that the graph points the import at a package and not at a file of the
// tree. Anything else TypeScript leaves unresolved is reported and not
// compared.
//
// Generated exports: the descriptor protoc-gen-es embeds in every _pb.ts
// (fileDesc("…base64…")) and the index each export takes into it
// (messageDesc(file, 2, 0)), which is the generator's own record of which
// proto declaration each export is, where the layer reads the names.

// TS is the oracle's view of a repository's frontend files.
type TS struct {
	// Imports are [from, to] graph IDs TypeScript resolved: a file of the
	// tree, or an external package node by package name.
	Imports [][2]graph.ID
	// Files are the files whose imports were read; Unresolved the
	// specifiers TypeScript left unresolved, as "file: spec".
	Files      map[graph.ID]bool
	Unresolved []string
	// Named counts the imports of a module named rather than a file, by
	// kind: "builtin" (node:fs) and "ambient" ($app/state).
	Named map[string]int
	// OutOfScope are imports TypeScript resolved to a file of the tree the
	// layer does not read — a .json, a declaration file — which ADR 007's
	// node types leave out, as "file -> path".
	OutOfScope []string
	// Generates are [proto, ts export] graph IDs from the _pb.ts files.
	Generates [][2]graph.ID
	PBFiles   int
	// NoTypeScript names a tree with frontend files and no typescript
	// package to resolve them with: its imports are not compared.
	NoTypeScript bool
	// protos are the tree's .proto files, absolute, for the file-level
	// generates edge.
	protos []string
}

// tsScript resolves every import of the files on stdin and writes one
// "file\tspec\tresolved\tkind" line per import: kind is "ext" for a file
// of a dependency, "builtin" or "ambient" for a module named, not a file.
const tsScript = `
const path = require('path'), fs = require('fs');
const ts = require(process.argv[2]);
const files = fs.readFileSync(0, 'utf8').split('\n').filter(Boolean);
const cache = new Map();
const host = { ...ts.sys, onUnRecoverableConfigFileDiagnostic: () => {} };
const fallback = { allowJs: true, module: ts.ModuleKind.ESNext, moduleResolution: ts.ModuleResolutionKind.Bundler };
// configOf is the nearest tsconfig.json above a file, or null.
function configOf(file) {
  for (let d = path.dirname(file); ; ) {
    const c = path.join(d, 'tsconfig.json');
    if (fs.existsSync(c)) return c;
    const up = path.dirname(d);
    if (up === d) return null;
    d = up;
  }
}
function options(file) {
  const c = configOf(file);
  if (!c) return fallback;
  if (!cache.has(c)) {
    const p = ts.getParsedCommandLineOfConfigFile(c, {}, host);
    cache.set(c, p ? p.options : fallback);
  }
  return cache.get(c);
}
// declared is the file a declaration "X.d.ext.ts" stands for, "X.ext",
// when that file is on disk and the declaration is not.
function declared(f) {
  const m = /^(.*)\.d(\.[^./\\]+)\.ts$/.exec(f);
  return m && !ts.sys.fileExists(f) && ts.sys.fileExists(m[1] + m[2]) ? m[1] + m[2] : '';
}
const arbitrary = { ...ts.sys, fileExists: f => ts.sys.fileExists(f) || declared(f) !== '' };
// ambient is the set of modules the file's program declares by name.
const ambients = new Map();
function ambient(file, o) {
  const c = configOf(file) || '';
  if (!ambients.has(c)) {
    const p = c ? ts.getParsedCommandLineOfConfigFile(c, {}, host) : null;
    const prog = ts.createProgram({ rootNames: p ? p.fileNames : [], options: { ...o, noEmit: true } });
    ambients.set(c, new Set(prog.getTypeChecker().getAmbientModules().map(m => m.getName().replace(/^"|"$/g, '')).filter(n => !n.includes('*'))));
  }
  return ambients.get(c);
}
function resolve(spec, f, o) {
  let r = ts.resolveModuleName(spec, f, o, ts.sys).resolvedModule;
  if (r) return [r.resolvedFileName, r.isExternalLibraryImport ? 'ext' : ''];
  r = ts.resolveModuleName(spec, f, { ...o, allowArbitraryExtensions: true }, arbitrary).resolvedModule;
  if (r) return [declared(r.resolvedFileName) || r.resolvedFileName, r.isExternalLibraryImport ? 'ext' : ''];
  if (require('module').isBuiltin(spec)) return [spec, 'builtin'];
  if (ambient(f, o).has(spec)) return [spec, 'ambient'];
  return ['', ''];
}
for (const f of files) {
  let text = fs.readFileSync(f, 'utf8');
  if (f.endsWith('.svelte')) text = [...text.matchAll(/<script[^>]*>([\s\S]*?)<\/script>/g)].map(m => m[1]).join('\n');
  const o = options(f);
  for (const imp of ts.preProcessFile(text, true, true).importedFiles) {
    process.stdout.write([f, imp.fileName, ...resolve(imp.fileName, f, o)].join('\t') + '\n');
  }
}
`

// Frontend runs the oracle over the frontend file nodes of g: the
// imports of each, and every _pb.ts under root. A repository with no
// typescript package installed has no import oracle; node missing from
// PATH is an error.
func Frontend(g *graph.Graph, m *graph.Minter, root string) (*TS, error) {
	t := &TS{Files: map[graph.ID]bool{}, Named: map[string]int{}}
	if err := t.pbFiles(m, root); err != nil {
		return nil, err
	}
	tsPkg := typescriptPackage(root)
	if tsPkg == "" {
		t.NoTypeScript = len(frontendFiles(g, root)) > 0
		return t, nil
	}
	if _, err := exec.LookPath("node"); err != nil {
		return nil, fmt.Errorf("oracle: node is not on PATH: %w", err)
	}
	files := frontendFiles(g, root)
	if len(files) == 0 {
		return t, nil
	}
	return t, t.imports(m, root, tsPkg, files)
}

// typescriptPackage is the typescript package the repository installs,
// or the one ALIDADE_TYPESCRIPT names, or "".
func typescriptPackage(root string) string {
	for _, p := range []string{filepath.Join(root, "node_modules", "typescript"), os.Getenv("ALIDADE_TYPESCRIPT")} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(p, "package.json")); err == nil {
			return p
		}
	}
	return ""
}

// frontendFiles are the absolute paths of g's frontend file nodes.
func frontendFiles(g *graph.Graph, root string) []string {
	var out []string
	for n := range g.Nodes() {
		if (n.Kind == graph.KindModule || n.Kind == graph.KindComponent) && strings.HasPrefix(string(n.ID), "ts://") && n.Pos.File != "" {
			out = append(out, filepath.Join(root, n.Pos.File))
		}
	}
	return out
}

func (t *TS) imports(m *graph.Minter, root, tsPkg string, files []string) error {
	script := filepath.Join(os.TempDir(), "alidade-oracle-resolve.cjs")
	if err := os.WriteFile(script, []byte(tsScript), 0o644); err != nil {
		return err
	}
	defer os.Remove(script)
	cmd := exec.Command("node", script, tsPkg)
	cmd.Stdin = strings.NewReader(strings.Join(files, "\n"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("oracle: typescript resolution: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) == 4 {
			t.line(m, root, f[0], f[1], f[2], f[3])
		}
	}
	for _, f := range files {
		if id, err := m.TSID(f, ""); err == nil {
			t.Files[id] = true
		}
	}
	return sc.Err()
}

// line adds one resolved import: to the file of the tree TypeScript
// resolved it to, or — resolved into a dependency, a Node builtin or an
// ambient module — to the package it names.
func (t *TS) line(m *graph.Minter, root, file, spec, resolved, kind string) {
	from, err := m.TSID(file, "")
	if err != nil {
		return
	}
	if resolved == "" {
		rel, _ := filepath.Rel(root, file)
		t.Unresolved = append(t.Unresolved, filepath.ToSlash(rel)+": "+spec)
		return
	}
	var to graph.ID
	switch {
	case kind == "builtin" || kind == "ambient":
		t.Named[kind]++
		to, err = m.TSPackageID(packageName(spec))
	case kind == "ext" || strings.Contains(resolved, string(filepath.Separator)+"node_modules"+string(filepath.Separator)):
		to, err = m.TSPackageID(packageName(spec))
	case !tsadapter.Extensions[filepath.Ext(resolved)] || tsadapter.Declaration(resolved):
		rel, _ := filepath.Rel(root, file)
		target, _ := filepath.Rel(root, resolved)
		t.OutOfScope = append(t.OutOfScope, filepath.ToSlash(rel)+" -> "+filepath.ToSlash(target))
		return
	default:
		to, err = m.TSID(resolved, "")
	}
	if err == nil {
		t.Imports = append(t.Imports, [2]graph.ID{from, to})
	}
}

// packageName is the package an import specifier names: "@scope/name"
// for a scoped one, the first segment otherwise.
func packageName(spec string) string {
	segs := strings.Split(spec, "/")
	if strings.HasPrefix(spec, "@") && len(segs) > 1 {
		return segs[0] + "/" + segs[1]
	}
	return segs[0]
}

var (
	fileDescRE = regexp.MustCompile(`fileDesc\(\s*"([A-Za-z0-9+/=]+)"`)
	// descCallRE is the descriptor call an export protoc-gen-es writes
	// for a declaration is initialised with: messageDesc(file_x, 2, 0).
	// The export is the nearest "export const" before it: a service's
	// type parameter carries the rpcs' doc comments, which may hold "=".
	descCallRE    = regexp.MustCompile(`(messageDesc|enumDesc|serviceDesc|extDesc)\(\s*\w+((?:\s*,\s*\d+)+)\s*\)`)
	exportConstRE = regexp.MustCompile(`export const (\w+)\b`)
	// messageTypeRE is the type alias of a message: export type Name = Message<"pkg.Name">
	messageTypeRE = regexp.MustCompile(`export type (\w+) = Message<"([\w.]+)">`)
	// enumRE is an enum's TypeScript enum: export enum Name {
	enumRE = regexp.MustCompile(`export enum (\w+) \{`)
)

// pbFiles reads every _pb.ts under root.
func (t *TS) pbFiles(m *graph.Minter, root string) error {
	var pb []string
	skip := skipOf(root)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != root && skip.SkipsDir(root, p) {
			return filepath.SkipDir // the build leaves it out too
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".tmp", ".svelte-kit", "dist", "build", ".astro", "out", "testdata":
				if p != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		switch {
		case strings.HasSuffix(p, "_pb.ts"):
			pb = append(pb, p)
		case strings.HasSuffix(p, ".proto"):
			t.protos = append(t.protos, p)
		}
		return nil
	})
	for _, p := range pb {
		t.PBFiles++
		t.pbFile(m, p)
	}
	return err
}

// pbFile maps one _pb.ts's exports to the proto declarations its embedded
// descriptor says they are.
func (t *TS) pbFile(m *graph.Minter, file string) {
	src, err := os.ReadFile(file)
	if err != nil {
		return
	}
	fd := embeddedDescriptor(src)
	if fd == nil {
		return
	}
	// The .proto file the module is generated from: the one in the tree
	// whose path ends with the descriptor's name, when exactly one does.
	if pf := t.protoFile(fd.GetName()); pf != "" {
		from, err1 := m.ProtoFileID(pf)
		to, err2 := m.TSID(file, "")
		if err1 == nil && err2 == nil {
			t.Generates = append(t.Generates, [2]graph.ID{from, to})
		}
	}
	enums := map[string]string{} // schema export -> enum full name
	add := func(export, fq string) {
		from, err1 := m.ProtoID(fq)
		to, err2 := m.TSID(file, export)
		if err1 == nil && err2 == nil {
			t.Generates = append(t.Generates, [2]graph.ID{from, to})
		}
	}
	exports := exportConstRE.FindAllSubmatchIndex(src, -1)
	for _, c := range descCallRE.FindAllSubmatchIndex(src, -1) {
		name := exportBefore(src, exports, c[0])
		kind := string(src[c[2]:c[3]])
		fq := descPath(fd, kind, indices(string(src[c[4]:c[5]])))
		if name == "" || fq == "" {
			continue
		}
		add(name, fq)
		if kind == "enumDesc" {
			enums[name] = fq
		}
	}
	for _, mm := range messageTypeRE.FindAllSubmatch(src, -1) {
		add(string(mm[1]), string(mm[2]))
	}
	for _, mm := range enumRE.FindAllSubmatch(src, -1) {
		if fq := enums[string(mm[1])+"Schema"]; fq != "" {
			add(string(mm[1]), fq)
		}
	}
}

// protoFile is the one .proto file of the tree whose path ends with an
// import path, or "".
func (t *TS) protoFile(imp string) string {
	var found string
	for _, p := range t.protos {
		if p == imp || strings.HasSuffix(p, "/"+imp) {
			if found != "" {
				return ""
			}
			found = p
		}
	}
	return found
}

// exportBefore is the name of the last "export const" before offset at.
func exportBefore(src []byte, exports [][]int, at int) string {
	name := ""
	for _, e := range exports {
		if e[0] > at {
			break
		}
		name = string(src[e[2]:e[3]])
	}
	return name
}

// embeddedDescriptor decodes the file descriptor a _pb.ts embeds.
func embeddedDescriptor(src []byte) *descriptorpb.FileDescriptorProto {
	mm := fileDescRE.FindSubmatch(src)
	if mm == nil {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(string(mm[1]))
	if err != nil {
		return nil
	}
	var fd descriptorpb.FileDescriptorProto
	if proto.Unmarshal(raw, &fd) != nil {
		return nil
	}
	return &fd
}

func indices(s string) []int {
	var out []int
	for _, f := range strings.Split(strings.Trim(s, " ,"), ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// descPath resolves a descriptor index path — messageDesc(file, 2, 0) is
// the first message nested in the third top-level message — to a full
// name, or "" when the path is out of range.
func descPath(fd *descriptorpb.FileDescriptorProto, kind string, idx []int) string {
	if len(idx) == 0 {
		return ""
	}
	scope := fd.GetPackage()
	msgs := fd.MessageType
	var parent *descriptorpb.DescriptorProto
	for _, i := range idx[:len(idx)-1] {
		if i >= len(msgs) {
			return ""
		}
		parent = msgs[i]
		scope = join(scope, parent.GetName())
		msgs = parent.NestedType
	}
	last := idx[len(idx)-1]
	switch kind {
	case "messageDesc":
		if last < len(msgs) {
			return join(scope, msgs[last].GetName())
		}
	case "enumDesc":
		enums := fd.EnumType
		if parent != nil {
			enums = parent.EnumType
		}
		if last < len(enums) {
			return join(scope, enums[last].GetName())
		}
	case "serviceDesc":
		if len(idx) == 1 && last < len(fd.Service) {
			return join(scope, fd.Service[last].GetName())
		}
	case "extDesc":
		exts := fd.Extension
		if parent != nil {
			exts = parent.Extension
		}
		if last < len(exts) {
			return join(scope, exts[last].GetName())
		}
	}
	return ""
}
