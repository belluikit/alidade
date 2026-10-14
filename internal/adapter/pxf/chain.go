// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package pxf

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trendvidia/chameleon/chain"
	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"

	"github.com/trendvidia/alidade/internal/graph"
)

// chainType is the @type of a chameleon chain specification.
const chainType = "chameleon.v1.ChainSpec"

// chain emits, from a chain specification file, one references edge to
// each layer file it loads, with the layer's id and the environments
// that load it. The expansion is chameleon's own — chain.ParseSpec and
// chain.Expand, once per declared environment — so what the graph says
// a chain loads is what chameleon would load. A literal path is
// EXTRACTED; one expanded from a template is DECLARED, since the
// environments it stands for are the expansion's, not the source's.
//
// A specification chameleon rejects, or an environment it cannot
// expand (a required context key with no default, say), leaves its
// reason on the file node and no layer edges for that environment.
func (x *extractor) chain(from graph.ID, path string, src []byte) error {
	x.report.Chains++
	// Layers are paths of the tree, relative to the chain's directory as
	// the file system names it: an escaped or labelled path is an ID's
	// form, and minting from it would escape it twice (#124).
	rel, err := x.m.Rel(path)
	if err != nil {
		return x.chainError(from, err)
	}
	msg, err := chain.ParseSpec(src)
	if err != nil {
		return x.chainError(from, err)
	}
	spec, ok := msg.(*chameleonv1.ChainSpec)
	if !ok {
		return x.chainError(from, fmt.Errorf("chain: %T is not a ChainSpec", msg))
	}
	loads, errs := expandAll(spec, filepath.ToSlash(filepath.Dir(rel)))
	if len(errs) > 0 {
		if err := x.chainError(from, fmt.Errorf("%s", strings.Join(slices.Compact(errs), "; "))); err != nil {
			return err
		}
	}
	return x.layerEdges(from, spec, loads)
}

// loads is target file -> layer id -> environments that load it.
type loads map[string]map[string][]string

// expandAll expands a chain for every declared environment and gathers
// what each layer entry loads, as repo-relative paths. Environments
// chameleon cannot expand contribute their error and nothing else.
func expandAll(spec *chameleonv1.ChainSpec, dir string) (loads, []string) {
	out := loads{}
	var errs []string
	for _, env := range spec.GetEnvironments() {
		refs, err := chain.Options{AllowUnknownContextKeys: true}.Expand(spec, env.GetName(), nil)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		for _, ref := range refs {
			root, ok := fsRoot(spec, ref.Source)
			if !ok {
				continue // an env or embed source loads no file of the tree
			}
			target := path.Clean(path.Join(dir, root, ref.Path))
			if out[target] == nil {
				out[target] = map[string][]string{}
			}
			out[target][ref.EntryID] = append(out[target][ref.EntryID], env.GetName())
		}
	}
	return out, errs
}

// layerEdges emits one references edge per (layer file, layer entry).
func (x *extractor) layerEdges(from graph.ID, spec *chameleonv1.ChainSpec, ld loads) error {
	templates := map[string]string{}
	for _, l := range spec.GetLayers() {
		templates[l.GetId()] = l.GetPath()
	}
	for _, target := range slices.Sorted(keysOf(ld)) {
		to, err := x.m.PXFID(target, "")
		if err != nil {
			x.skip(0, err)
			continue
		}
		for _, id := range slices.Sorted(keysOf(ld[target])) {
			envs := ld[target][id]
			slices.Sort(envs)
			d := graph.Extracted
			if strings.Contains(templates[id], "${") {
				d = graph.Declared
			}
			x.report.Layers++
			if err := x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindReferences, Derivation: d,
				Attrs: map[string]string{"layer": id, "envs": strings.Join(slices.Compact(envs), " ")}}); err != nil {
				return err
			}
		}
	}
	return nil
}

// fsRoot returns the root directory of a file-system source, relative to
// the chain file, and false for a source that loads no files. A source
// the spec does not declare is the default file-system source.
func fsRoot(spec *chameleonv1.ChainSpec, source string) (string, bool) {
	ref, ok := spec.GetSources()[source]
	if !ok {
		return ".", source == "default"
	}
	if ref.GetKind() != "fs" {
		return "", false
	}
	if root := ref.GetOptions()["root"]; root != "" {
		return root, true
	}
	return ".", true
}

// chainError notes on the chain's file node why chameleon could not
// expand it, in whole or for some environment.
func (x *extractor) chainError(from graph.ID, err error) error {
	x.report.ChainErrors++
	return x.b.AddNode(graph.Node{ID: from, Kind: graph.KindPXFFile, Attrs: map[string]string{"chain_error": firstLine(err.Error())}})
}

func keysOf[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
