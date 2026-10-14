// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/trendvidia/protowire-go/encoding/pxf"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/alidade/internal/graph"
)

// The PXF schema oracle (#190): each document that declares its @type,
// decoded by protowire-go — the format's decoder, not the layer's walk
// of block paths — against the descriptors protoc compiled, and the set
// of proto fields the decoder says it sets. The graph's side is the set
// of proto fields the document's entries reference.

// SchemaFields decodes every document of types — file to message — and
// returns, by file, the full names of the proto fields it sets, the
// fields a default inside a list element may be (the decoder records it
// without its place), and the documents the decoder or schema refused.
func SchemaFields(root string, types map[string]string, protos *Proto) (sets, unplaced map[string]map[string]bool, failed []string) {
	sets, unplaced = map[string]map[string]bool{}, map[string]map[string]bool{}
	if protos == nil || len(protos.Descriptors) == 0 {
		return sets, unplaced, nil
	}
	for file, typ := range types {
		md, err := messageDescriptor(protos.Descriptors, typ)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			continue
		}
		// A sops-encrypted layer carries sops's own top-level block,
		// which sops strips before chameleon decodes the layer.
		opts := pxf.UnmarshalOptions{SkipValidate: true, DiscardUnknown: strings.HasSuffix(file, ".sops.pxf")}
		msg, res, err := opts.UnmarshalFullDescriptor(src, md)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: decode: %v", file, strings.SplitN(err.Error(), "\n", 2)[0]))
			continue
		}
		set := map[string]bool{}
		setFields(msg, set)
		msgs := reachable(md)
		un := map[string]bool{}
		for _, path := range res.PresentFields() {
			presentField(msgs, strings.Split(path, "."), set, un)
		}
		sets[file], unplaced[file] = set, un
	}
	return sets, unplaced, failed
}

// CensusTypes gives each layer of a judged chain (the chain census) that
// declares no @type the one type the chain's other layers declare, by
// the spec reader — the oracle's own inference, from the census rather
// than from the graph's chain edges.
func CensusTypes(s *Sample, declared map[string]string) map[string]string {
	out := map[string]string{}
	chains := map[graph.ID][]string{}
	for _, l := range s.Edges {
		chains[l.From] = append(chains[l.From], strings.TrimPrefix(string(l.To), "pxf://"))
	}
	for _, layers := range chains {
		types := map[string]bool{}
		for _, f := range layers {
			if t := declared[f]; t != "" {
				types[t] = true
			}
		}
		if len(types) != 1 {
			continue
		}
		for t := range types {
			for _, f := range layers {
				if declared[f] == "" {
					out[f] = t
				}
			}
		}
	}
	return out
}

// messageDescriptor builds the message's descriptor from its file and
// that file's imports.
func messageDescriptor(all map[string]*descriptorpb.FileDescriptorProto, typ string) (protoreflect.MessageDescriptor, error) {
	var home string
	for name, fd := range all {
		if declares(fd, typ) {
			home = name
			break
		}
	}
	if home == "" {
		return nil, fmt.Errorf("no compiled file declares %s", typ)
	}
	set := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	var add func(name string)
	add = func(name string) {
		if seen[name] || all[name] == nil {
			return
		}
		seen[name] = true
		for _, dep := range all[name].Dependency {
			add(dep)
		}
		set.File = append(set.File, all[name])
	}
	add(home)
	files, err := protodesc.NewFiles(set)
	if err != nil {
		return nil, err
	}
	d, err := files.FindDescriptorByName(protoreflect.FullName(typ))
	if err != nil {
		return nil, err
	}
	md, ok := d.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, fmt.Errorf("%s is not a message", typ)
	}
	return md, nil
}

func declares(fd *descriptorpb.FileDescriptorProto, typ string) bool {
	pkg := fd.GetPackage()
	var walk func(scope string, msgs []*descriptorpb.DescriptorProto) bool
	walk = func(scope string, msgs []*descriptorpb.DescriptorProto) bool {
		for _, m := range msgs {
			name := m.GetName()
			if scope != "" {
				name = scope + "." + name
			}
			if name == typ || walk(name, m.NestedType) {
				return true
			}
		}
		return false
	}
	return walk(pkg, fd.MessageType)
}

// setFields records every field a decoded message holds a value for,
// through nested messages, lists and maps.
func setFields(m protoreflect.Message, set map[string]bool) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		set[string(fd.FullName())] = true
		switch {
		case fd.IsMap():
			if fd.MapValue().Message() != nil && !wellKnown(fd.MapValue().Message()) {
				v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
					setFields(mv.Message(), set)
					return true
				})
			}
		case fd.IsList():
			if fd.Message() != nil && !wellKnown(fd.Message()) {
				for i := range v.List().Len() {
					setFields(v.List().Get(i).Message(), set)
				}
			}
		case fd.Message() != nil && !wellKnown(fd.Message()):
			setFields(v.Message(), set)
		}
		return true
	})
}

// presentField records a path the decoder saw written, default values
// included, which a decoded message does not hold. The decoder records a
// path inside a list element relative to the element, so the path is
// resolved from every message the document's type reaches and recorded
// when exactly one of them has it.
func presentField(msgs []protoreflect.MessageDescriptor, segs []string, set, unplaced map[string]bool) {
	if countResolving(msgs, segs) != 1 {
		// Two messages have the path: the decoder wrote it without its
		// place, a default value inside a list element. Note every
		// field it could be, so the comparison can say why.
		for _, md := range msgs {
			if names, ok := pathFields(md, segs); ok {
				unplaced[names[len(names)-1]] = true
			}
		}
		return
	}
	for _, md := range msgs {
		if names, ok := pathFields(md, segs); ok {
			for _, n := range names {
				set[n] = true
			}
			return
		}
	}
}

func countResolving(msgs []protoreflect.MessageDescriptor, segs []string) int {
	n := 0
	for _, md := range msgs {
		if _, ok := pathFields(md, segs); ok {
			n++
		}
	}
	return n
}

// pathFields resolves a field path from a message, returning each
// field's full name.
func pathFields(md protoreflect.MessageDescriptor, segs []string) ([]string, bool) {
	var out []string
	for _, s := range segs {
		if md == nil || wellKnown(md) {
			return nil, false
		}
		fd := md.Fields().ByName(protoreflect.Name(s))
		if fd == nil {
			return nil, false
		}
		out = append(out, string(fd.FullName()))
		md = fd.Message()
		if fd.IsMap() {
			md = fd.MapValue().Message()
		}
	}
	return out, true
}

// reachable lists a message and every message its fields reach, the
// well-known types left out: PXF writes those as one value.
func reachable(md protoreflect.MessageDescriptor) []protoreflect.MessageDescriptor {
	seen := map[protoreflect.FullName]bool{}
	var out []protoreflect.MessageDescriptor
	var walk func(m protoreflect.MessageDescriptor)
	walk = func(m protoreflect.MessageDescriptor) {
		if m == nil || seen[m.FullName()] || wellKnown(m) {
			return
		}
		seen[m.FullName()] = true
		out = append(out, m)
		fs := m.Fields()
		for i := range fs.Len() {
			fd := fs.Get(i)
			if fd.IsMap() {
				walk(fd.MapValue().Message())
			} else {
				walk(fd.Message())
			}
		}
	}
	walk(md)
	return out
}

// wellKnown is a message PXF writes as one value: a protobuf well-known
// type (7d for a Duration) or protowire's own pxf.Secret.
func wellKnown(m protoreflect.MessageDescriptor) bool {
	name := string(m.FullName())
	return strings.HasPrefix(name, "google.protobuf.") || strings.HasPrefix(name, "pxf.")
}

// A PXFSchemaResult compares, by document, the proto fields the decoder
// says a document sets with the ones its entries reference in the graph.
type PXFSchemaResult struct {
	Files          int
	Fields         [2]int
	Missing, Extra []string
	Failed         []string
	// Unplaced are graph references the decoder confirms only as a
	// default written inside a list element, without its place.
	Unplaced int
}

// ComparePXFSchema measures the graph's .pxf → .proto field references
// for the documents the decoder read.
func ComparePXFSchema(g *graph.Graph, sets, unplaced map[string]map[string]bool, failed []string) *PXFSchemaResult {
	r := &PXFSchemaResult{Files: len(sets), Failed: failed}
	have := map[string]map[string]bool{}
	for e := range g.Edges() {
		body, ok := strings.CutPrefix(string(e.From), "pxf://")
		to, toProto := strings.CutPrefix(string(e.To), "proto://")
		if !ok || !toProto || e.Kind != graph.KindReferences || !strings.Contains(body, "#") {
			continue
		}
		file, _, _ := strings.Cut(body, "#")
		if have[file] == nil {
			have[file] = map[string]bool{}
		}
		have[file][to] = true
	}
	for file, set := range sets {
		for f := range set {
			r.Fields[0]++
			if have[file][f] {
				r.Fields[1]++
			} else {
				r.Missing = append(r.Missing, file+" -> "+f)
			}
		}
		for f := range have[file] {
			switch {
			case set[f]:
			case unplaced[file][f]:
				r.Unplaced++
			default:
				r.Extra = append(r.Extra, file+" -> "+f)
			}
		}
	}
	sortAll(&r.Missing, &r.Extra, &r.Failed)
	return r
}

func (r *PXFSchemaResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "pxf schema fields %d / %d in %d decoded documents, %d extra, %d a default inside a list the decoder does not place, %d not decoded\n",
		r.Fields[1], r.Fields[0], r.Files, len(r.Extra), r.Unplaced, len(r.Failed))
	for _, l := range []struct {
		name  string
		items []string
	}{{"missing schema field", r.Missing}, {"extra schema field", r.Extra}, {"not decoded", r.Failed}} {
		for i, s := range l.items {
			if i == 6 {
				fmt.Fprintf(&b, "%s ... and %d more\n", l.name, len(l.items)-6)
				break
			}
			fmt.Fprintf(&b, "%s %s\n", l.name, s)
		}
	}
	return b.String()
}
