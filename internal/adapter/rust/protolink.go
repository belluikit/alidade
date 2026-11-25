// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/belluikit/alidade/internal/graph"
)

// protoPackage is the .proto package prost-build wrote a file for: it
// names each file after its package, `my.package.rs`, and the file of the
// .proto files with none `_.rs` (ADR 009).
func protoPackage(out string) string {
	pkg := strings.TrimSuffix(filepath.Base(out), ".rs")
	if pkg == "_" {
		return ""
	}
	return pkg
}

func filepathBase(p string) string { return filepath.Base(p) }

// nestedProto is the .proto path of the message whose nested types a
// module holds: prost-build writes them in a module named after it in
// snake case, beside its struct (`my_message` beside `MyMessage`). nil
// for any other item, and for a module beside no such struct.
func nestedProto(sy symbol, siblings []symbol, proto []string) []string {
	if proto == nil || symbolKinds[sy.Kind] != symbolKinds[2] {
		return nil
	}
	for _, s := range siblings {
		if s.Kind == 23 && snake(s.Name) == sy.Name {
			return append(slices.Clone(proto), s.Name)
		}
	}
	return nil
}

// snake is a struct's name as prost-build names the module of its nested
// types: an underscore before each capital that follows a lower-case
// letter or a digit, all lower case. prost-build's own names are already
// upper camel case, so no run of capitals reaches here.
func snake(name string) string {
	var b strings.Builder
	prev := rune(0)
	for _, r := range name {
		if unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
		prev = r
	}
	return b.String()
}

var messageNameRE = regexp.MustCompile(`message_name\s*=\s*"([^"]*)"`)

// protoLink joins a struct prost-build wrote for a .proto message, or an
// enum for a .proto enum, to it: a generates edge from the message's node
// (ADR 009), DECLARED, as for protoc-gen-go and protoc-gen-es. The
// generator's derive says which items are which (::prost::Message,
// ::prost::Enumeration; a oneof's enum is neither). The full name is the
// file's package, then the messages holding it, then its name. Where the
// generated code also states names (prost-reflect's message_name, prost's
// impl Name), one of them must be that one, or the struct is reported and
// gets no edge: the two disagree, and neither is guessed.
func (x *extractor) protoLink(g *genFile, sy symbol, siblings []symbol, d *def, proto []string) error {
	if proto == nil {
		return nil
	}
	attrs := attributes(g.lines, int(sy.Range.Start.Line), int(sy.SelectionRange.Start.Line))
	isMessage := sy.Kind == 23 && bytes.Contains(attrs, []byte("::prost::Message"))
	isEnum := sy.Kind == 10 && bytes.Contains(attrs, []byte("::prost::Enumeration"))
	if !isMessage && !isEnum {
		return nil
	}
	full := strings.Join(append(slices.Clone(proto), sy.Name), ".")
	if g.pkg != "" {
		full = g.pkg + "." + full
	}
	stated := statedNames(g, attrs, sy.Name, siblings)
	if len(stated) > 0 && !slices.Contains(stated, full) {
		x.report.GeneratesConflicting++
		x.skip(g.s.dc.abs, int(g.s.at.line)+1, fmt.Errorf("%s states the .proto names %s, and prost-build's naming gives %s: it has no generates edge",
			d.id, strings.Join(stated, ", "), full))
		return nil
	}
	from, err := x.m.ProtoID(full)
	if err != nil {
		return nil
	}
	x.report.Generates++
	return x.b.AddEdge(graph.Edge{From: from, To: d.id, Kind: graph.KindGenerates, Derivation: graph.Declared,
		Attrs: map[string]string{"via": "prost"}})
}

// attributes are an item's lines from its start, its attributes
// included (prost's derive can span lines), to its name, joined.
func attributes(lines [][]byte, from, name int) []byte {
	var out [][]byte
	for i := max(from, 0); i <= name && i < len(lines); i++ {
		out = append(out, bytes.TrimSpace(lines[i]))
	}
	return bytes.Join(out, []byte("\n"))
}

// statedNames are the full .proto names a generated item states for
// itself: prost-reflect's #[prost_reflect(message_name = "test.Scalars")],
// each of them (a build can stack two), and prost's impl ::prost::Name,
// PACKAGE then NAME.
func statedNames(g *genFile, attrs []byte, name string, siblings []symbol) []string {
	var out []string
	for _, m := range messageNameRE.FindAllSubmatch(attrs, -1) {
		out = append(out, string(m[1]))
	}
	for _, s := range siblings {
		if s.Kind != lspImpl {
			continue
		}
		if ty, trait := implName(s.Name); ty != name || trait != "Name" {
			continue
		}
		var pkg, short string
		for _, c := range s.Children {
			switch c.Name {
			case "NAME":
				short = literalOn(g.lines, int(c.SelectionRange.Start.Line))
			case "PACKAGE":
				pkg = literalOn(g.lines, int(c.SelectionRange.Start.Line))
			}
		}
		if short != "" {
			out = append(out, strings.TrimPrefix(pkg+"."+short, "."))
		}
	}
	return out
}

// literalOn is the first string literal on a line, without its quotes.
func literalOn(lines [][]byte, at int) string {
	if at < 0 || at >= len(lines) {
		return ""
	}
	_, rest, ok := bytes.Cut(lines[at], []byte(`"`))
	if !ok {
		return ""
	}
	lit, _, _ := bytes.Cut(rest, []byte(`"`))
	return string(lit)
}
