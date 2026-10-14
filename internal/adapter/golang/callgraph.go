// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import (
	"github.com/trendvidia/caliper/pkg/analysis"
	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// The calls a declaration makes come from one of two call graphs, chosen
// per call site (#94, the owner's decision, ADR 006). A static call and a
// call through an interface take CHA's answer: the callee the type checker
// names, and every implementation of the method. A call through a function
// value takes VTA's: CHA answers it with every function of the program
// whose signature matches, which is shape matching, while VTA follows the
// function values that can reach the site. VTA is not used everywhere,
// because it also loses true interface edges whose receiver is made in
// dependency code it does not see (walker.Observe → pgconn.PgError.Error
// on drover).

// A callGraph answers a function's outgoing calls, site by site.
type callGraph struct {
	cha *callgraph.Graph
	// dynamic holds VTA's callees of each call through a function value.
	// A site VTA reaches nothing from is absent: no function value of
	// the program flows there, or the one that does was made outside it.
	dynamic map[ssa.CallInstruction][]*ssa.Function
}

// newCallGraph refines ipc's CHA call graph at its function-value call
// sites with VTA, seeded with that graph so the program is not built twice.
func newCallGraph(ipc *analysis.InterProceduralContext) *callGraph {
	g := &callGraph{cha: ipc.CallGraph, dynamic: map[ssa.CallInstruction][]*ssa.Function{}}
	refined := vta.CallGraph(ssautil.AllFunctions(ipc.Program), ipc.CallGraph)
	for _, n := range refined.Nodes {
		for _, e := range n.Out {
			if dynamicSite(e.Site) {
				g.dynamic[e.Site] = append(g.dynamic[e.Site], e.Callee.Func)
			}
		}
	}
	return g
}

// dynamicSite reports whether a call goes through a function value: not a
// static call, and not a call through an interface.
func dynamicSite(site ssa.CallInstruction) bool {
	if site == nil {
		return false
	}
	c := site.Common()
	return c.StaticCallee() == nil && !c.IsInvoke()
}

// A call is one outgoing edge: the site and a function it may call.
type call struct {
	site   ssa.CallInstruction
	callee *ssa.Function
}

// out lists fn's outgoing calls: CHA's at a static or interface site,
// VTA's at a function-value site.
//
// A function the call graph never reached — a generic origin, a method
// nothing calls — has no node in it. Its static calls are still the
// callees its own instructions name (#150); a call through an interface
// is emitted from the instruction too (interfaceCalls), and a call
// through a function value has no callee to name without the graph.
func (g *callGraph) out(fn *ssa.Function) []call {
	node := g.cha.Nodes[fn]
	if node == nil {
		return staticCalls(fn)
	}
	var calls []call
	seen := map[ssa.CallInstruction]bool{}
	for _, e := range node.Out {
		if !dynamicSite(e.Site) {
			calls = append(calls, call{e.Site, e.Callee.Func})
			continue
		}
		if seen[e.Site] {
			continue
		}
		seen[e.Site] = true
		for _, callee := range g.dynamic[e.Site] {
			calls = append(calls, call{e.Site, callee})
		}
	}
	return calls
}

// staticCalls lists the calls fn's instructions name a callee for.
func staticCalls(fn *ssa.Function) []call {
	var calls []call
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if site, ok := instr.(ssa.CallInstruction); ok {
				if callee := site.Common().StaticCallee(); callee != nil {
					calls = append(calls, call{site, callee})
				}
			}
		}
	}
	return calls
}

// callees lists the functions fn may call, each once.
func (g *callGraph) callees(fn *ssa.Function) []*ssa.Function {
	var out []*ssa.Function
	seen := map[*ssa.Function]bool{}
	for _, c := range g.out(fn) {
		if !seen[c.callee] {
			seen[c.callee] = true
			out = append(out, c.callee)
		}
	}
	return out
}
