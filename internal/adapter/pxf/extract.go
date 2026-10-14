// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package pxf

import (
	"fmt"
	"github.com/trendvidia/alidade/internal/scope"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trendvidia/protowire-go/encoding/pxf"

	"github.com/trendvidia/alidade/internal/graph"
)

// Options controls the .pxf layer.
type Options struct {
	// Skip are the directories left out besides the layer's own
	// (ADR 003, amended for #215): .alidade.pxf's list, vendor and
	// third_party by default. Nil is the default.
	Skip *scope.Dirs
	// Testdata includes .pxf files under testdata directories; out by
	// default, as for the other layers.
	Testdata bool
}

// DefaultOptions returns the layer's defaults.
func DefaultOptions() Options { return Options{} }

// A Report says what Extract found.
type Report struct {
	// Files is how many .pxf files were read; Failed how many the
	// parser rejected (each keeps a node carrying the error).
	Files, Failed int
	// Typed counts documents with a @type; Blocks, Fields and Datasets
	// count emitted nodes.
	Typed, Blocks, Fields, Datasets int
	// Refs counts "${path}" references that name a field of their own
	// document; ContextRefs those naming env or ctx; UnresolvedRefs
	// those naming neither, which are emitted and drop at merge.
	Refs, ContextRefs, UnresolvedRefs int
	// Chains counts chain specifications; Layers the layer files they
	// were found to load; ChainErrors the specifications, or single
	// environments of one, chameleon could not expand.
	Chains, Layers, ChainErrors int
	// Unkeyable counts entries whose key cannot be a path segment.
	Unkeyable int
	// Testdata counts .pxf files skipped under testdata directories.
	Testdata int
	// Skipped are the files, references and layer paths left out because
	// no ID can name them, or the files that could not be read (#92).
	Skipped []graph.Skip
	Elapsed time.Duration
	// SchemaLinked counts the fields and blocks [LinkSchema] joined to
	// the proto field they set, SchemaUnresolved the ones under a known
	// type whose path the schema does not have, and SchemaInferred the
	// layers whose type came from their chain (#190).
	SchemaLinked, SchemaUnresolved, SchemaInferred int
}

// SkipDirs are the directories the layer never descends into, by name.
// The build reads them too: a file under one is not in the layer's
// signature, and nothing else is left out of it (#93).
var SkipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".tmp": true}

// Extract parses every .pxf file under root and adds the layer to b.
func Extract(b *graph.Builder, root string, opts Options) (*Report, error) {
	start := time.Now()
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("pxf: %w", err)
	}
	m := b.Minter()
	files, testdata, skipped, err := discover(abs, !opts.Testdata, opts.Skip, m)
	if err != nil {
		return nil, fmt.Errorf("pxf: discover under %s: %w", abs, err)
	}
	x := &extractor{b: b, m: m, root: abs, report: &Report{Files: len(files), Testdata: testdata, Skipped: skipped}}
	for _, f := range files {
		if err := x.file(f); err != nil {
			return nil, err
		}
	}
	x.report.Elapsed = time.Since(start)
	return x.report, nil
}

// discover lists every .pxf file under root, sorted, and counts those
// skipped under testdata. An entry the walk cannot open is recorded and
// passed over.
func discover(root string, skipTestdata bool, skip *scope.Dirs, m *graph.Minter) (files []string, testdata int, skipped []graph.Skip, err error) {
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			skipped = append(skipped, m.Skip(path, 0, err))
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if SkipDirs[d.Name()] || skip.SkipsDir(root, path) {
				return filepath.SkipDir
			}
			if skipTestdata && d.Name() == "testdata" {
				testdata += countPXF(path)
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".pxf") {
			files = append(files, path)
		}
		return nil
	})
	slices.Sort(files)
	return files, testdata, skipped, err
}

func countPXF(dir string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".pxf") {
			n++
		}
		return nil
	})
	return n
}

type extractor struct {
	b      *graph.Builder
	m      *graph.Minter
	root   string
	report *Report
	// rel is the file being read, for [extractor.skip].
	rel string
}

// skip records that something at line of the current file was left out.
func (x *extractor) skip(line int, err error) {
	x.report.Skipped = append(x.report.Skipped, x.m.Skip(x.rel, line, err))
}

// file parses one document and emits it. A file no ID can name, or that
// cannot be read, is skipped and recorded (#92).
func (x *extractor) file(path string) error {
	x.rel = path
	id, err := x.m.PXFID(path, "")
	if err != nil {
		x.skip(0, err)
		return nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		x.skip(0, err)
		return nil
	}
	doc, perr := pxf.Parse(src)
	if perr != nil {
		x.report.Failed++
		return x.b.AddNode(graph.Node{ID: id, Kind: graph.KindPXFFile, Name: filepath.Base(path),
			Pos: graph.Position{File: path}, Attrs: map[string]string{"error": firstLine(perr.Error())}})
	}
	return x.document(id, path, doc, src)
}

// document emits a parsed file: its node, its @type reference, its
// entries, its references, and its layers if it is a chain.
func (x *extractor) document(id graph.ID, path string, doc *pxf.Document, src []byte) error {
	attrs := map[string]string{}
	if doc.TypeURL != "" {
		attrs["type"] = doc.TypeURL
		x.report.Typed++
	}
	if err := x.b.AddNode(graph.Node{ID: id, Kind: graph.KindPXFFile, Name: filepath.Base(path), Pos: graph.Position{File: path}, Attrs: attrs}); err != nil {
		return err
	}
	if doc.TypeURL != "" {
		if err := x.typeRef(id, 0, doc.TypeURL, nil); err != nil {
			return err
		}
	}
	d := &document{x: x, rel: path, fileID: id, paths: map[string]graph.ID{}}
	if err := d.directives(doc); err != nil {
		return err
	}
	if err := d.entries(id, "", doc.Entries); err != nil {
		return err
	}
	if err := d.refs(); err != nil {
		return err
	}
	if doc.TypeURL == chainType {
		return x.chain(id, path, src)
	}
	return nil
}

// typeRef emits the references edge from a node to the .proto message a
// type name denotes; a name no ID can carry is skipped and recorded at
// line, 0 for the file's own @type.
func (x *extractor) typeRef(from graph.ID, line int, typeName string, attrs map[string]string) error {
	to, err := x.m.ProtoID(typeName)
	if err != nil {
		x.skip(line, err)
		return nil
	}
	return x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindReferences, Derivation: graph.Extracted, Attrs: attrs})
}

// A document walks one parsed file.
type document struct {
	x      *extractor
	rel    string
	fileID graph.ID
	// paths maps every block path of the document to its node, for
	// resolving "${path}" references once the walk is done.
	paths map[string]graph.ID
	// pending are the references found during the walk, and
	// contextRefs the env/ctx ones, noted on their fields at the end.
	pending     []pendingRef
	contextRefs []contextRef
}

type pendingRef struct {
	from graph.ID
	path string
	// line is where the "${path}" was written, so a path no ID can
	// carry is recorded at the reference and not at the whole file.
	line int
}

// directives emits a node per directive and dataset, each referencing
// the type it names.
func (d *document) directives(doc *pxf.Document) error {
	count, seen := map[string]int{}, map[string]int{}
	for _, dir := range doc.Directives {
		count[dir.Name]++
	}
	for _, dir := range doc.Directives {
		attrs := map[string]string{"directive": dir.Name}
		if len(dir.Prefixes) > 0 {
			attrs["prefixes"] = strings.Join(dir.Prefixes, " ")
		}
		label, typ := directiveType(dir)
		if label != "" {
			attrs["label"] = label
		}
		// A second directive of a name is keyed by an ordinal (ADR 002
		// rule 9), as a second dataset is (#97).
		name := "@" + dir.Name + ordinal(seen[dir.Name], count[dir.Name])
		seen[dir.Name]++
		if err := d.typedBlock(name, dir.Pos, attrs, typ); err != nil {
			return err
		}
	}
	for i, p := range doc.Protos {
		// An embedded schema: visible, with its shape and the type a
		// named one declares (#97). It declares rather than references,
		// so it is no edge.
		attrs := map[string]string{"proto": p.Shape.String()}
		if p.TypeName != "" {
			attrs["declares"] = p.TypeName
		}
		if err := d.typedBlock("@proto"+ordinal(i, len(doc.Protos)), p.Pos, attrs, ""); err != nil {
			return err
		}
	}
	for i, ds := range doc.Datasets {
		d.x.report.Datasets++
		attrs := map[string]string{"dataset": "true", "rows": strconv.Itoa(len(ds.Rows)), "columns": strings.Join(ds.Columns, " ")}
		if err := d.typedBlock("@dataset"+ordinal(i, len(doc.Datasets)), ds.Pos, attrs, ds.Type); err != nil {
			return err
		}
	}
	return nil
}

// typedBlock emits one directive or dataset under the file, and its
// reference to the type it names when it names one.
func (d *document) typedBlock(name string, pos pxf.Position, attrs map[string]string, typ string) error {
	id, err := d.node(name, graph.KindBlock, pos, attrs)
	if err != nil || id == "" {
		return err
	}
	if err := d.contains(d.fileID, id); err != nil {
		return err
	}
	if typ == "" {
		return nil
	}
	return d.x.typeRef(id, pos.Line, typ, nil)
}

// directiveType reads a directive's prefixes as the protowire spec
// registers them (#97): @entry takes a label, a type, or both — one
// prefix being a type when dotted and a label when bare — and every
// other directive one prefix, its type.
func directiveType(dir pxf.Directive) (label, typ string) {
	if dir.Name != "entry" {
		return "", dir.Type
	}
	switch len(dir.Prefixes) {
	case 1:
		if strings.Contains(dir.Prefixes[0], ".") {
			return "", dir.Prefixes[0]
		}
		return dir.Prefixes[0], ""
	case 2:
		return dir.Prefixes[0], dir.Prefixes[1]
	}
	return "", ""
}

// ordinal distinguishes the second and later of several like-named
// directives; the first keeps the bare name.
func ordinal(i, n int) string {
	if n <= 1 || i == 0 {
		return ""
	}
	return strconv.Itoa(i)
}

// entries emits the entries of a body under owner at path prefix.
func (d *document) entries(owner graph.ID, prefix string, entries []pxf.Entry) error {
	blocks := repeatedBlocks(entries)
	seen := map[string]int{}
	for _, e := range entries {
		key, value, body, ok := split(e)
		if !ok {
			continue
		}
		if !keyable(key) {
			d.x.report.Unkeyable++
			continue
		}
		pos, _ := pxf.EntrySpan(e)
		path := join(prefix, key)
		if n := blocks[key]; n > 1 {
			// A block written more than once is a repeated message:
			// each occurrence is an element of it, keyed as a list's
			// element is (ADR 002 rule 9) — by its id or name, else
			// its index — not merged into one node (#97).
			i := seen[key]
			seen[key]++
			if err := d.repeated(owner, path, i, n, pos, body); err != nil {
				return err
			}
			continue
		}
		if err := d.entry(owner, path, pos, value, body); err != nil {
			return err
		}
	}
	return nil
}

// split reads an entry's key, its value, and a block's body.
func split(e pxf.Entry) (key string, value pxf.Value, body []pxf.Entry, ok bool) {
	switch n := e.(type) {
	case *pxf.Assignment:
		return n.Key, n.Value, nil, true
	case *pxf.MapEntry:
		return n.Key, n.Value, nil, true
	case *pxf.Block:
		return n.Name, nil, n.Entries, true
	}
	return "", nil, nil, false
}

// repeatedBlocks counts, per key, the blocks a body writes under it.
func repeatedBlocks(entries []pxf.Entry) map[string]int {
	n := map[string]int{}
	for _, e := range entries {
		if b, ok := e.(*pxf.Block); ok {
			n[b.Name]++
		}
	}
	return n
}

// repeated emits occurrence i of n of a repeated block: the block's node
// once, at its first occurrence, and this occurrence as its element.
func (d *document) repeated(owner graph.ID, path string, i, n int, pos pxf.Position, body []pxf.Entry) error {
	if i == 0 {
		d.x.report.Blocks++
		id, err := d.node(path, graph.KindBlock, pos, map[string]string{"repeated": strconv.Itoa(n)})
		if err != nil || id == "" {
			return err
		}
		if err := d.contains(owner, id); err != nil {
			return err
		}
	}
	parent, ok := d.paths[path]
	if !ok {
		return nil
	}
	key := elementKey(body)
	if key == "" || !keyable(key) {
		key = strconv.Itoa(i)
	}
	return d.entry(parent, join(path, key), pos, nil, body)
}

// entry emits one block or field and whatever is nested in it.
func (d *document) entry(owner graph.ID, path string, pos pxf.Position, value pxf.Value, body []pxf.Entry) error {
	if bv, ok := value.(*pxf.BlockVal); ok {
		body, value = bv.Entries, nil
	}
	if value == nil {
		d.x.report.Blocks++
		id, err := d.node(path, graph.KindBlock, pos, nil)
		if err != nil || id == "" {
			return err
		}
		if err := d.contains(owner, id); err != nil {
			return err
		}
		return d.entries(id, path, body)
	}
	d.x.report.Fields++
	attrs := map[string]string{"kind": kindOf(value)}
	if lv, ok := value.(*pxf.ListVal); ok {
		attrs["len"] = strconv.Itoa(len(lv.Elements))
	}
	id, err := d.node(path, graph.KindField, pos, attrs)
	if err != nil || id == "" {
		return err
	}
	if err := d.contains(owner, id); err != nil {
		return err
	}
	d.scanRefs(id, pos.Line, value)
	if lv, ok := value.(*pxf.ListVal); ok {
		return d.listElements(id, path, lv)
	}
	return nil
}

// listElements emits the block elements of a list, each keyed by its id
// or name when it has one and by its index otherwise.
func (d *document) listElements(owner graph.ID, path string, lv *pxf.ListVal) error {
	for i, el := range lv.Elements {
		bv, ok := el.(*pxf.BlockVal)
		if !ok {
			continue
		}
		key := elementKey(bv.Entries)
		if key == "" || !keyable(key) {
			key = strconv.Itoa(i)
		}
		pos, _ := pxf.ValueSpan(el)
		if err := d.entry(owner, join(path, key), pos, nil, bv.Entries); err != nil {
			return err
		}
	}
	return nil
}

// elementKey returns a list element's id or name string, or "".
func elementKey(entries []pxf.Entry) string {
	for _, want := range []string{"id", "name"} {
		for _, e := range entries {
			if a, ok := e.(*pxf.Assignment); ok && a.Key == want {
				if s, ok := a.Value.(*pxf.StringVal); ok {
					return s.Value
				}
			}
		}
	}
	return ""
}

// node emits one block or field. A path no ID can carry is skipped and
// recorded, and returns "", which the caller takes to skip what it holds.
func (d *document) node(path string, kind graph.Kind, pos pxf.Position, attrs map[string]string) (graph.ID, error) {
	id, err := d.x.m.PXFID(d.rel, path)
	if err != nil {
		d.x.skip(pos.Line, err)
		return "", nil
	}
	name := path
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		name = path[i+1:]
	}
	d.paths[path] = id
	return id, d.x.b.AddNode(graph.Node{ID: id, Kind: kind, Name: name,
		Pos: graph.Position{File: d.rel, Line: pos.Line, Col: pos.Column}, Attrs: attrs})
}

func (d *document) contains(owner, id graph.ID) error {
	return d.x.b.AddEdge(graph.Edge{From: owner, To: id, Kind: graph.KindContains, Derivation: graph.Extracted})
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// keyable reports whether a key can be one segment of a block path: the
// characters ADR 002's grammar reserves, and a dot, which would read as
// two segments, disqualify it.
func keyable(key string) bool {
	return key != "" && !strings.ContainsAny(key, "./#\t\n\r ")
}

// kindOf names a value's kind, never its value.
func kindOf(v pxf.Value) string {
	switch v.(type) {
	case *pxf.StringVal:
		return "string"
	case *pxf.IntVal:
		return "int"
	case *pxf.FloatVal:
		return "float"
	case *pxf.BoolVal:
		return "bool"
	case *pxf.BytesVal:
		return "bytes"
	case *pxf.NullVal:
		return "null"
	case *pxf.IdentVal:
		return "ident"
	case *pxf.TimestampVal:
		return "timestamp"
	case *pxf.DurationVal:
		return "duration"
	case *pxf.ListVal:
		return "list"
	default:
		return "bad"
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
