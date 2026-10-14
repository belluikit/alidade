// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package proto

import (
	"strings"

	"github.com/trendvidia/protocompile/protoutil"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// A goIdent is the Go type protoc-gen-go emits for a message or enum.
type goIdent struct {
	importPath string
	name       string
}

// goImportPath returns the import path a file's go_package names, or "".
func goImportPath(fd protoreflect.FileDescriptor) protogen.GoImportPath {
	gp := goPackageOption(fd)
	// "import/path;name" — the name after the semicolon is the package
	// name, not part of the path.
	for i := range gp {
		if gp[i] == ';' {
			return protogen.GoImportPath(gp[:i])
		}
	}
	return protogen.GoImportPath(gp)
}

// goNames asks protogen — the library protoc-gen-go is built on — what Go
// type it would name each message and enum of the file, so the generates
// edge follows the generator's own rule (nested Outer_Inner, CamelCase,
// keyword avoidance) rather than a copy of it. importPathOf says what Go
// import path a file has, from its go_package or from buf's managed mode;
// a file with none has no Go types and gets an empty map.
func goNames(fd protoreflect.FileDescriptor, importPathOf func(protoreflect.FileDescriptor) string) (map[protoreflect.FullName]goIdent, error) {
	names := make(map[protoreflect.FullName]goIdent)
	if importPathOf(fd) == "" {
		return names, nil
	}
	req := genRequest(fd, importPathOf)
	plugin, err := protogen.Options{}.New(req)
	if err != nil {
		return names, err
	}
	for _, f := range plugin.Files {
		if !f.Generate {
			continue
		}
		var walk func(ms []*protogen.Message, es []*protogen.Enum)
		walk = func(ms []*protogen.Message, es []*protogen.Enum) {
			for _, e := range es {
				names[e.Desc.FullName()] = goIdent{string(e.GoIdent.GoImportPath), e.GoIdent.GoName}
			}
			for _, m := range ms {
				if m.Desc.IsMapEntry() {
					continue
				}
				names[m.Desc.FullName()] = goIdent{string(m.GoIdent.GoImportPath), m.GoIdent.GoName}
				walk(m.Messages, m.Enums)
			}
		}
		walk(f.Messages, f.Enums)
	}
	return names, nil
}

// descriptorProto is a file's descriptor as protoc would hand it to a
// plugin. A file protocompile compiled carries the proto it parsed,
// edition and all, which protoutil returns once the linker's wrapper is
// taken off; protodesc's conversion of the linked file reports
// EDITION_UNKNOWN for an editions file, which protogen refuses, so such
// a file named no Go types (#97).
func descriptorProto(f protoreflect.FileDescriptor) *descriptorpb.FileDescriptorProto {
	if u, ok := f.(interface {
		Unwrap() protoreflect.FileDescriptor
	}); ok {
		f = u.Unwrap()
	}
	return protoutil.ProtoFromFileDescriptor(f)
}

// genRequest builds the CodeGeneratorRequest protoc would send for fd:
// the file, its transitive imports, and an import path for every file
// that does not carry one.
func genRequest(fd protoreflect.FileDescriptor, importPathOf func(protoreflect.FileDescriptor) string) *pluginpb.CodeGeneratorRequest {
	req := &pluginpb.CodeGeneratorRequest{FileToGenerate: []string{fd.Path()}}
	var files []protoreflect.FileDescriptor
	seen := make(map[string]bool)
	var add func(f protoreflect.FileDescriptor)
	add = func(f protoreflect.FileDescriptor) {
		if seen[f.Path()] {
			return
		}
		seen[f.Path()] = true
		imps := f.Imports()
		for i := range imps.Len() {
			add(imps.Get(i).FileDescriptor)
		}
		files = append(files, f)
		req.ProtoFile = append(req.ProtoFile, descriptorProto(f))
	}
	add(fd)
	// The plugin needs an import path for every file, ours and its
	// dependencies'. An M parameter is how protoc passes one, and it wins
	// over a written go_package: managed mode's answer where there is one
	// — which replaces a written option (bufgen.go) — and a placeholder
	// that cannot collide with a real package for a file with neither. A file that does name its package ("path;name") settles
	// the name for every file sharing that path, since the plugin
	// refuses two names for one path.
	pkgNames := make(map[string]string)
	for _, f := range files {
		if gp := goPackageOption(f); strings.Contains(gp, ";") {
			i := strings.Index(gp, ";")
			pkgNames[gp[:i]] = gp[i+1:]
		}
	}
	for _, f := range files {
		p := importPathOf(f)
		if written := goPackageOption(f); written != "" && (p == "" || string(goImportPath(f)) == p) {
			continue // the file's own option is the answer
		}
		if p == "" {
			p = "unknown.invalid/" + f.Path()
		}
		if name := pkgNames[p]; name != "" {
			p += ";" + name
		}
		req.Parameter = appendParam(req.Parameter, "M"+f.Path()+"="+p)
	}
	return req
}

// goPackageOption returns a file's go_package option as written, or "".
func goPackageOption(f protoreflect.FileDescriptor) string {
	opts, _ := f.Options().(*descriptorpb.FileOptions)
	return opts.GetGoPackage()
}

func appendParam(p *string, kv string) *string {
	s := kv
	if p != nil && *p != "" {
		s = *p + "," + kv
	}
	return &s
}
