// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package proto

import (
	"fmt"
	"strconv"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/belluikit/alidade/internal/graph"
)

// A walker emits the declarations of one compiled file.
type walker struct {
	x     *extractor
	rel   string
	fd    protoreflect.FileDescriptor
	names map[protoreflect.FullName]goIdent
}

func (w *walker) file(fileID graph.ID) error {
	for i := range w.fd.Messages().Len() {
		if err := w.message(fileID, w.fd.Messages().Get(i)); err != nil {
			return err
		}
	}
	for i := range w.fd.Enums().Len() {
		if err := w.enum(fileID, w.fd.Enums().Get(i)); err != nil {
			return err
		}
	}
	for i := range w.fd.Extensions().Len() {
		if err := w.field(fileID, w.fd.Extensions().Get(i)); err != nil {
			return err
		}
	}
	for i := range w.fd.Services().Len() {
		if err := w.service(fileID, w.fd.Services().Get(i)); err != nil {
			return err
		}
	}
	return nil
}

// decl emits a declaration node contained by owner, and returns its ID.
func (w *walker) decl(owner graph.ID, d protoreflect.Descriptor, kind graph.Kind, attrs map[string]string) (graph.ID, error) {
	id, err := w.x.m.ProtoID(string(d.FullName()))
	if err != nil {
		return "", fmt.Errorf("proto: %w", err)
	}
	w.x.report.Decls++
	n := graph.Node{ID: id, Kind: kind, Name: string(d.Name()), Pos: w.position(d), Attrs: attrs}
	if err := w.x.b.AddNode(n); err != nil {
		return "", err
	}
	return id, w.x.b.AddEdge(graph.Edge{From: owner, To: id, Kind: graph.KindContains, Derivation: graph.Extracted})
}

// position locates a declaration from the file's source info.
func (w *walker) position(d protoreflect.Descriptor) graph.Position {
	loc := w.fd.SourceLocations().ByDescriptor(d)
	if loc.Path == nil {
		return graph.Position{File: w.rel}
	}
	return graph.Position{File: w.rel, Line: loc.StartLine + 1, Col: loc.StartColumn + 1}
}

// generates emits the generates edge from a message or enum to the Go type
// protoc-gen-go names for it, when the file has a go_package.
func (w *walker) generates(from graph.ID, name protoreflect.FullName) error {
	gi, ok := w.names[name]
	if !ok || w.x.opts.NoGoLink {
		return nil
	}
	to, err := w.x.m.GoID(gi.importPath, gi.name)
	if err != nil {
		return fmt.Errorf("proto: %w", err)
	}
	w.x.report.Generates++
	return w.x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindGenerates, Derivation: graph.Declared,
		Attrs: map[string]string{"via": "go_package"}})
}

func (w *walker) message(owner graph.ID, md protoreflect.MessageDescriptor) error {
	if md.IsMapEntry() {
		return nil // the synthetic entry message of a map field
	}
	id, err := w.decl(owner, md, graph.KindMessage, nil)
	if err != nil {
		return err
	}
	if err := w.generates(id, md.FullName()); err != nil {
		return err
	}
	for i := range md.Fields().Len() {
		if err := w.field(id, md.Fields().Get(i)); err != nil {
			return err
		}
	}
	for i := range md.Messages().Len() {
		if err := w.message(id, md.Messages().Get(i)); err != nil {
			return err
		}
	}
	for i := range md.Enums().Len() {
		if err := w.enum(id, md.Enums().Get(i)); err != nil {
			return err
		}
	}
	for i := range md.Extensions().Len() {
		if err := w.field(id, md.Extensions().Get(i)); err != nil {
			return err
		}
	}
	return nil
}

func (w *walker) field(owner graph.ID, fd protoreflect.FieldDescriptor) error {
	attrs := map[string]string{"number": strconv.Itoa(int(fd.Number())), "type": fd.Kind().String()}
	if fd.Cardinality() == protoreflect.Repeated {
		attrs["repeated"] = "true"
	}
	if fd.IsMap() {
		attrs["map"] = "true"
	}
	if o := fd.ContainingOneof(); o != nil && !o.IsSynthetic() {
		attrs["oneof"] = string(o.Name())
	}
	if fd.IsExtension() {
		attrs["extendee"] = string(fd.ContainingMessage().FullName())
	}
	id, err := w.decl(owner, fd, graph.KindField, attrs)
	if err != nil {
		return err
	}
	if fd.IsExtension() {
		// The message the extension extends is a type it names (#97).
		m := fd.ContainingMessage()
		if err := w.referenceAs(id, m.FullName(), m.ParentFile(), map[string]string{"extends": "true"}); err != nil {
			return err
		}
	}
	// A map field's value type is what it references; the key is scalar.
	target := fd
	if fd.IsMap() {
		target = fd.MapValue()
	}
	var typeName protoreflect.FullName
	var typeFile protoreflect.FileDescriptor
	switch {
	case target.Message() != nil:
		typeName, typeFile = target.Message().FullName(), target.Message().ParentFile()
	case target.Enum() != nil:
		typeName, typeFile = target.Enum().FullName(), target.Enum().ParentFile()
	default:
		return nil
	}
	return w.reference(id, typeName, typeFile)
}

// reference emits a references edge to a declaration by full name, or —
// when that declaration lives in a file outside the repository — to that
// file's external node, since no node exists for the declaration itself.
func (w *walker) reference(from graph.ID, name protoreflect.FullName, file protoreflect.FileDescriptor) error {
	return w.referenceAs(from, name, file, nil)
}

// referenceAs is reference with attributes of the edge's own.
func (w *walker) referenceAs(from graph.ID, name protoreflect.FullName, file protoreflect.FileDescriptor, attrs map[string]string) error {
	e := graph.Edge{From: from, Kind: graph.KindReferences, Derivation: graph.Resolved, Attrs: attrs}
	var err error
	if file != nil && locate(w.x.cur, file.Path()) == "" {
		e.To, err = w.x.fileNode(file.Path())
		if e.Attrs == nil {
			e.Attrs = map[string]string{}
		}
		e.Attrs["boundary"], e.Attrs["type"] = "file", string(name)
	} else {
		e.To, err = w.x.m.ProtoID(string(name))
	}
	if err != nil {
		w.x.skip(0, err)
		return nil
	}
	return w.x.b.AddEdge(e)
}

func (w *walker) enum(owner graph.ID, ed protoreflect.EnumDescriptor) error {
	id, err := w.decl(owner, ed, graph.KindEnum, nil)
	if err != nil {
		return err
	}
	if err := w.generates(id, ed.FullName()); err != nil {
		return err
	}
	for i := range ed.Values().Len() {
		v := ed.Values().Get(i)
		if _, err := w.decl(id, v, graph.KindEnumValue, map[string]string{"number": strconv.Itoa(int(v.Number()))}); err != nil {
			return err
		}
	}
	return nil
}

func (w *walker) service(owner graph.ID, sd protoreflect.ServiceDescriptor) error {
	id, err := w.decl(owner, sd, graph.KindService, nil)
	if err != nil {
		return err
	}
	for i := range sd.Methods().Len() {
		m := sd.Methods().Get(i)
		attrs := map[string]string{}
		if m.IsStreamingClient() {
			attrs["client_streaming"] = "true"
		}
		if m.IsStreamingServer() {
			attrs["server_streaming"] = "true"
		}
		mid, err := w.decl(id, m, graph.KindRPC, attrs)
		if err != nil {
			return err
		}
		for _, io := range []protoreflect.MessageDescriptor{m.Input(), m.Output()} {
			if err := w.reference(mid, io.FullName(), io.ParentFile()); err != nil {
				return err
			}
		}
	}
	return nil
}
