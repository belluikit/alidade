// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"gopkg.in/yaml.v3"

	"github.com/belluikit/alidade/internal/graph"
)

// The proto oracle is protoc, Google's C++ compiler, which shares no code
// with protocompile, the Go compiler the proto layer reads files with
// (#148). Each .proto file is compiled on its own to a descriptor set,
// which says what the file declares, what it imports and what each field
// and rpc refers to. The Go types generated for each message come from
// the .pb.go files themselves: protoc-gen-go writes, in every file it
// generates, a table naming each Go type with its proto full name, which
// the layer derives from protogen's naming rules instead.

// Proto is the oracle's view of a repository's .proto files.
type Proto struct {
	// Decls maps each declaration's graph ID to its node kind.
	Decls map[graph.ID]graph.Kind
	// Imports, Refs and Generates are edges as [from, to] graph IDs: a
	// file importing a file, a field or rpc referring to a message or
	// enum, a message or enum generating a Go type.
	Imports, Refs, Generates [][2]graph.ID
	// Files are the repo-relative paths protoc compiled; Failed the ones
	// it refused, each with its first line of error.
	Files  map[string]bool
	Failed map[string]string
	// GoFiles counts the .pb.go files read; MapEntries the map entry
	// messages protoc declares, which the layer does not make nodes of
	// (a compiler artefact, walk.go), so they are not compared.
	GoFiles, MapEntries int
	// Descriptors are every file descriptor protoc gave, the compiled
	// files and their imports, by file name: the schema the .pxf oracle
	// decodes documents against (#190).
	Descriptors map[string]*descriptorpb.FileDescriptorProto
}

// A typeIndex knows, for every message and enum of a file and its
// imports, the file declaring it, and for a map entry message the value
// field a map field of it refers through to.
type typeIndex struct {
	file     map[string]string // ".pkg.T" -> declaring file's import path
	mapValue map[string]*descriptorpb.FieldDescriptorProto
}

// Protoc compiles every .proto file under root with protoc and reads
// every .pb.go file's type table. protoc missing from PATH is an error.
func Protoc(m *graph.Minter, root string) (*Proto, error) {
	if _, err := exec.LookPath("protoc"); err != nil {
		return nil, fmt.Errorf("oracle: protoc is not on PATH: %w", err)
	}
	p := &Proto{Decls: map[graph.ID]graph.Kind{}, Files: map[string]bool{}, Failed: map[string]string{},
		Descriptors: map[string]*descriptorpb.FileDescriptorProto{}}
	roots := includeRoots(root)
	protos, pbgo := sourceFiles(root)
	scratch, err := os.MkdirTemp("", "alidade-oracle-protoc-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	for _, f := range protos {
		rel, _ := filepath.Rel(root, f)
		fileRoots := forFile(root, roots, f)
		fd, idx, err := compile(fileRoots, f, filepath.Join(scratch, "set.binpb"), p.Descriptors)
		if err != nil {
			p.Failed[filepath.ToSlash(rel)] = err.Error()
			continue
		}
		p.Files[filepath.ToSlash(rel)] = true
		p.file(m, fileRoots, f, fd, idx)
	}
	for _, f := range pbgo {
		p.GoFiles++
		p.goTypes(m, root, f)
	}
	return p, nil
}

// skipDir is what neither layer reads: the proto layer's SkipDirs, and
// testdata, which it leaves out by default.
func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".tmp", "testdata":
		return true
	}
	return false
}

// sourceFiles lists the .proto and .pb.go files under root.
func sourceFiles(root string) (protos, pbgo []string) {
	skip := skipOf(root)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != root && skip.SkipsDir(root, p) {
			return filepath.SkipDir // the build leaves it out too
		}
		if d.IsDir() {
			if p != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case strings.HasSuffix(p, ".proto"):
			protos = append(protos, p)
		case strings.HasSuffix(p, ".pb.go"):
			pbgo = append(pbgo, p)
		}
		return nil
	})
	return protos, pbgo
}

// includeRoots are the directories imports resolve against, as buf
// documents them: each module of a buf.yaml (v2 modules, v1 build.roots,
// else its own directory) and each directory of a buf.work.yaml, then the
// repository root. Innermost first, so protoc maps a file to the module
// it is in.
func includeRoots(root string) []string {
	set := map[string]bool{root: true}
	skip := skipOf(root)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != root && skip.SkipsDir(root, p) {
			return filepath.SkipDir // the build leaves it out too
		}
		if d.IsDir() && p != root && skipDir(d.Name()) {
			return filepath.SkipDir
		}
		if !d.IsDir() && (d.Name() == "buf.yaml" || d.Name() == "buf.work.yaml") {
			for _, r := range bufRoots(p) {
				set[r] = true
			}
		}
		return nil
	})
	roots := make([]string, 0, len(set))
	for r := range set {
		roots = append(roots, r)
	}
	slices.SortFunc(roots, func(a, b string) int { return len(b) - len(a) })
	return roots
}

// importLineRE is an import statement of a .proto file.
var importLineRE = regexp.MustCompile(`(?m)^\s*import\s+(?:public\s+|weak\s+)?"([^"]+)"`)

// forFile is the include roots one file compiles against. A file in a
// buf module has buf's. A file outside every module has no build
// configuration to read, and the layer's documented rule (discover.go)
// is the contract for it: each import makes the nearest ancestor
// directory under which the imported path exists a root.
func forFile(root string, roots []string, file string) []string {
	for _, r := range roots {
		if r != root && strings.HasPrefix(file, r+string(filepath.Separator)) {
			return roots
		}
	}
	src, err := os.ReadFile(file)
	if err != nil {
		return roots
	}
	var inferred []string
	for _, m := range importLineRE.FindAllStringSubmatch(string(src), -1) {
		for dir := filepath.Dir(file); strings.HasPrefix(dir, root); dir = filepath.Dir(dir) {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(m[1]))); err == nil {
				if !slices.Contains(inferred, dir) && !slices.Contains(roots, dir) {
					inferred = append(inferred, dir)
				}
				break
			}
			if dir == root {
				break
			}
		}
	}
	return append(inferred, roots...)
}

// bufRoots reads one buf.yaml or buf.work.yaml.
func bufRoots(file string) []string {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var doc struct {
		Modules     []struct{ Path string } `yaml:"modules"`
		Directories []string                `yaml:"directories"`
		Build       struct {
			Roots []string `yaml:"roots"`
		} `yaml:"build"`
	}
	if yaml.Unmarshal(raw, &doc) != nil {
		return nil
	}
	dir := filepath.Dir(file)
	var rels []string
	for _, m := range doc.Modules {
		rels = append(rels, m.Path)
	}
	rels = append(append(rels, doc.Directories...), doc.Build.Roots...)
	if len(rels) == 0 && filepath.Base(file) == "buf.yaml" {
		rels = []string{"."}
	}
	out := make([]string, 0, len(rels))
	for _, r := range rels {
		out = append(out, filepath.Join(dir, r))
	}
	return out
}

// compile runs protoc on one file and returns its descriptor, with an
// index of the types it and its imports declare. protoc writes a file's
// imports before it, so the file is the set's last.
func compile(roots []string, file, out string, all map[string]*descriptorpb.FileDescriptorProto) (*descriptorpb.FileDescriptorProto, *typeIndex, error) {
	args := make([]string, 0, len(roots)+3)
	for _, r := range roots {
		args = append(args, "-I"+r)
	}
	args = append(args, "--include_imports", "--descriptor_set_out="+out, file)
	if msg, err := exec.Command("protoc", args...).CombinedOutput(); err != nil {
		line, _, _ := strings.Cut(strings.TrimSpace(string(msg)), "\n")
		return nil, nil, fmt.Errorf("protoc: %s", line)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, nil, err
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(b, &set); err != nil || len(set.File) == 0 {
		return nil, nil, fmt.Errorf("protoc: unreadable descriptor set: %v", err)
	}
	idx := &typeIndex{file: map[string]string{}, mapValue: map[string]*descriptorpb.FieldDescriptorProto{}}
	for _, fd := range set.File {
		idx.add(fd.GetName(), "."+fd.GetPackage(), fd.MessageType, fd.EnumType)
		if _, seen := all[fd.GetName()]; !seen {
			all[fd.GetName()] = fd
		}
	}
	return set.File[len(set.File)-1], idx, nil
}

func (idx *typeIndex) add(file, scope string, msgs []*descriptorpb.DescriptorProto, enums []*descriptorpb.EnumDescriptorProto) {
	if scope == "." {
		scope = ""
	}
	for _, e := range enums {
		idx.file[scope+"."+e.GetName()] = file
	}
	for _, md := range msgs {
		name := scope + "." + md.GetName()
		idx.file[name] = file
		if md.GetOptions().GetMapEntry() && len(md.Field) == 2 {
			idx.mapValue[name] = md.Field[1]
		}
		idx.add(file, name, md.NestedType, md.EnumType)
	}
}

// file adds one compiled file's declarations, imports and references.
func (p *Proto) file(m *graph.Minter, roots []string, path string, fd *descriptorpb.FileDescriptorProto, idx *typeIndex) {
	fileID, err := m.ProtoFileID(path)
	if err != nil {
		return
	}
	for _, dep := range fd.Dependency {
		if to := importID(m, roots, dep); to != "" {
			p.Imports = append(p.Imports, [2]graph.ID{fileID, to})
		}
	}
	scope := fd.GetPackage()
	w := &protoWalk{p: p, m: m, roots: roots, idx: idx}
	for _, md := range fd.MessageType {
		w.message(scope, md)
	}
	for _, ed := range fd.EnumType {
		p.enum(m, scope, ed)
	}
	w.extensions(scope, fd.Extension)
	for _, sd := range fd.Service {
		p.decl(m, join(scope, sd.GetName()), graph.KindService)
		for _, md := range sd.Method {
			rid := p.decl(m, join(scope, sd.GetName(), md.GetName()), graph.KindRPC)
			w.ref(rid, md.GetInputType())
			w.ref(rid, md.GetOutputType())
		}
	}
}

// importID is the node an import lands on: the repository file under the
// first include root that has it, or the import path itself, as the
// layer keys an import nothing in the repository answers.
func importID(m *graph.Minter, roots []string, dep string) graph.ID {
	for _, r := range roots {
		if _, err := os.Stat(filepath.Join(r, dep)); err == nil {
			if id, err := m.ProtoFileID(filepath.Join(r, dep)); err == nil {
				return id
			}
		}
	}
	id, _ := m.ProtoFileID(dep)
	return id
}

// protoWalk walks one file's messages with what resolving a reference
// needs.
type protoWalk struct {
	p     *Proto
	m     *graph.Minter
	roots []string
	idx   *typeIndex
}

func (w *protoWalk) message(scope string, md *descriptorpb.DescriptorProto) {
	if md.GetOptions().GetMapEntry() {
		w.p.MapEntries++
		return
	}
	name := join(scope, md.GetName())
	w.p.decl(w.m, name, graph.KindMessage)
	for _, f := range md.Field {
		fid := w.p.decl(w.m, join(name, f.GetName()), graph.KindField)
		w.fieldRef(fid, f)
	}
	for _, n := range md.NestedType {
		w.message(name, n)
	}
	for _, e := range md.EnumType {
		w.p.enum(w.m, name, e)
	}
	w.extensions(name, md.Extension)
}

// extensions adds extension fields, declared in scope: each refers to
// the message it extends and to its own type.
func (w *protoWalk) extensions(scope string, exts []*descriptorpb.FieldDescriptorProto) {
	for _, f := range exts {
		fid := w.p.decl(w.m, join(scope, f.GetName()), graph.KindField)
		w.ref(fid, f.GetExtendee())
		w.fieldRef(fid, f)
	}
}

// fieldRef adds what a field refers to: its message, group or enum
// type, or for a map field its value's, the key being scalar.
func (w *protoWalk) fieldRef(fid graph.ID, f *descriptorpb.FieldDescriptorProto) {
	if v, ok := w.idx.mapValue[f.GetTypeName()]; ok {
		f = v
	}
	switch f.GetType() {
	case descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, descriptorpb.FieldDescriptorProto_TYPE_ENUM,
		descriptorpb.FieldDescriptorProto_TYPE_GROUP: // a proto2 group is a message field
		w.ref(fid, f.GetTypeName())
	}
}

// ref adds a reference to a type protoc names fully qualified, ".pkg.T":
// to its node when the repository declares it, else to the external
// node of the file that does, as the layer lands it.
func (w *protoWalk) ref(from graph.ID, typeName string) {
	if from == "" {
		return
	}
	to, err := w.m.ProtoID(strings.TrimPrefix(typeName, "."))
	if file := w.idx.file[typeName]; file != "" && !inRepo(w.roots, file) {
		to, err = w.m.ProtoFileID(file)
	}
	if err == nil {
		w.p.Refs = append(w.p.Refs, [2]graph.ID{from, to})
	}
}

// inRepo reports whether an import path names a file under an include
// root.
func inRepo(roots []string, file string) bool {
	for _, r := range roots {
		if _, err := os.Stat(filepath.Join(r, file)); err == nil {
			return true
		}
	}
	return false
}

// enum adds an enum and its values. A value's full name is a sibling of
// its enum's, as protobuf scopes it: pkg.VALUE, not pkg.Enum.VALUE.
func (p *Proto) enum(m *graph.Minter, scope string, ed *descriptorpb.EnumDescriptorProto) {
	p.decl(m, join(scope, ed.GetName()), graph.KindEnum)
	for _, v := range ed.Value {
		p.decl(m, join(scope, v.GetName()), graph.KindEnumValue)
	}
}

func (p *Proto) decl(m *graph.Minter, fq string, kind graph.Kind) graph.ID {
	id, err := m.ProtoID(fq)
	if err != nil {
		return ""
	}
	p.Decls[id] = kind
	return id
}

func join(parts ...string) string {
	var out []string
	for _, s := range parts {
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ".")
}

// goTypeRE is one entry of protoc-gen-go's goTypes table:
// "(*Foo_Bar)(nil), // 3: pkg.Foo.Bar" for a message,
// "(Kind)(0), // 0: pkg.Kind" for an enum. An entry whose Go name is
// qualified (timestamppb.Timestamp) is a type of another package.
var goTypeRE = regexp.MustCompile(`^\s*\(\*?([A-Za-z_][A-Za-z0-9_]*)\)\((?:nil|0)\),\s*//\s*\d+:\s*([A-Za-z0-9_.]+)\s*$`)

// goTypes reads one .pb.go file's goTypes table.
func (p *Proto) goTypes(m *graph.Minter, root, file string) {
	pkg := goImportPath(root, filepath.Dir(file))
	if pkg == "" {
		return
	}
	f, err := os.Open(file)
	if err != nil {
		return
	}
	defer f.Close()
	in := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "var file_") && strings.Contains(line, "_goTypes = []any{"):
			in = true
		case in && strings.TrimSpace(line) == "}":
			in = false
		case in:
			if g := goTypeRE.FindStringSubmatch(line); g != nil {
				from, err1 := m.ProtoID(g[2])
				to, err2 := m.GoID(pkg, g[1])
				if err1 == nil && err2 == nil {
					p.Generates = append(p.Generates, [2]graph.ID{from, to})
				}
			}
		}
	}
}

// goImportPath is a directory's Go import path, from the nearest go.mod
// at or above it within root; "" when there is none.
func goImportPath(root, dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if raw, err := os.ReadFile(filepath.Join(d, "go.mod")); err == nil {
			mod := modulePath(raw)
			rel, _ := filepath.Rel(d, dir)
			if mod == "" {
				return ""
			}
			if rel == "." {
				return mod
			}
			return mod + "/" + filepath.ToSlash(rel)
		}
		if d == root || d == filepath.Dir(d) {
			return ""
		}
	}
}

func modulePath(gomod []byte) string {
	for _, line := range strings.Split(string(gomod), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`)
		}
	}
	return ""
}
