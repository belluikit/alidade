// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package pxf

import (
	"regexp"
	"strings"

	"github.com/trendvidia/protowire-go/encoding/pxf"

	"github.com/belluikit/alidade/internal/graph"
)

// refRE matches a "${path}" interpolation in a string value.
var refRE = regexp.MustCompile(`\$\{\s*([A-Za-z_][A-Za-z0-9_.]*)\s*\}`)

// scanRefs records the references a field's string values carry, for
// resolution once the whole document is known; line is the field's, for
// a path no ID can carry. Context references (env, ctx.*) name no field
// and are noted on the node instead.
func (d *document) scanRefs(field graph.ID, line int, v pxf.Value) {
	var ctx []string
	for _, s := range stringsIn(v) {
		for _, m := range refRE.FindAllStringSubmatch(s, -1) {
			path := m[1]
			if seg, _, _ := strings.Cut(path, "."); seg == "env" || seg == "ctx" {
				ctx = append(ctx, path)
				continue
			}
			d.pending = append(d.pending, pendingRef{from: field, path: path, line: line})
		}
	}
	if len(ctx) > 0 {
		d.x.report.ContextRefs += len(ctx)
		d.contextRefs = append(d.contextRefs, contextRef{field, ctx})
	}
}

type contextRef struct {
	field graph.ID
	paths []string
}

// stringsIn lists the string values in a value, descending into lists.
func stringsIn(v pxf.Value) []string {
	switch v := v.(type) {
	case *pxf.StringVal:
		return []string{v.Value}
	case *pxf.ListVal:
		var out []string
		for _, el := range v.Elements {
			out = append(out, stringsIn(el)...)
		}
		return out
	}
	return nil
}

// refs emits the edges for the references found during the walk. A
// path that names a field or block of this document is an edge to it; a
// path naming nothing is an edge to where it would be, which drops at
// merge with its reason rather than vanishing.
func (d *document) refs() error {
	for _, cr := range d.contextRefs {
		if err := d.x.b.AddNode(graph.Node{ID: cr.field, Kind: graph.KindField,
			Attrs: map[string]string{"context_refs": strings.Join(cr.paths, " ")}}); err != nil {
			return err
		}
	}
	for _, r := range d.pending {
		to, ok := d.paths[r.path]
		if !ok {
			d.x.report.UnresolvedRefs++
			var err error
			if to, err = d.x.m.PXFID(d.rel, r.path); err != nil {
				d.x.skip(r.line, err)
				continue
			}
		} else {
			d.x.report.Refs++
		}
		if err := d.x.b.AddEdge(graph.Edge{From: r.from, To: to, Kind: graph.KindReferences, Derivation: graph.Extracted,
			Attrs: map[string]string{"ref": r.path}}); err != nil {
			return err
		}
	}
	return nil
}
