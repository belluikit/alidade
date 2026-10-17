// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	mcpsdk "github.com/mark3labs/mcp-go/mcp"
)

// Parameters shared by the traversal tools.
func withBudget() mcpsdk.ToolOption {
	return mcpsdk.WithNumber("max_tokens",
		mcpsdk.Description("Result budget in estimated tokens; a cut result says so."),
		mcpsdk.DefaultNumber(defaultBudget), mcpsdk.Min(minBudget), mcpsdk.Max(maxBudget))
}

func withFilter() []mcpsdk.ToolOption {
	return []mcpsdk.ToolOption{
		mcpsdk.WithArray("edge_kinds", mcpsdk.WithStringItems(),
			mcpsdk.Description("Only these edge kinds: calls, implements, references, contains, imports, generates, documents, queries.")),
		mcpsdk.WithBoolean("exclude_inferred", mcpsdk.DefaultBool(false),
			mcpsdk.Description("Leave out INFERRED (heuristic) edges.")),
		withExcludeTests(),
		withExcludeGenerated(),
	}
}

func withExcludeGenerated() mcpsdk.ToolOption {
	return mcpsdk.WithBoolean("exclude_generated", mcpsdk.DefaultBool(false),
		mcpsdk.Description("Leave generated code out; a traversal still crosses it, showing such a node as id and kind."))
}

func withExcludeTests() mcpsdk.ToolOption {
	return mcpsdk.WithBoolean("exclude_tests", mcpsdk.DefaultBool(false),
		mcpsdk.Description("Leave out _test.go declarations and their edges: the production graph."))
}

// withRoot is the graph tools' root: another worktree of the repository,
// whose graph is laid over the served checkout's (#25).
func withRoot() mcpsdk.ToolOption {
	return mcpsdk.WithString("root",
		mcpsdk.Description("Another git worktree of this repository, by its top directory: answer from its graph, built over this checkout's on first use (what its files share with this one is reused). The answer carries root."))
}

func withDirection(def direction) mcpsdk.ToolOption {
	return mcpsdk.WithString("direction", mcpsdk.Enum("out", "in", "both"), mcpsdk.DefaultString(string(def)),
		mcpsdk.Description("out: what it calls, contains, references. in: its callers, implementers, referrers."))
}

func (s *Server) registerTools() {
	add := s.addTool

	add(mcpsdk.NewTool("graph_stats",
		mcpsdk.WithDescription("What the graph holds by layer and derivation, build time, and what the build skipped or could not compile."),
		withRoot(),
		mcpsdk.WithReadOnlyHintAnnotation(true),
	), s.handleStats)

	add(mcpsdk.NewTool("find_nodes",
		mcpsdk.WithDescription("Find nodes by name or ID. Exact name matches rank first, then prefixes, then substrings. Use it to get an ID for the traversal tools. It matches names, not file contents."),
		mcpsdk.WithString("query", mcpsdk.Required(), mcpsdk.Description("A name (Total, Workspace.ReferencesFor), a path fragment, or a whole ID.")),
		mcpsdk.WithString("kind", mcpsdk.Description("Only this kind: func, method, type, message, rpc, table, query, section, ...")),
		mcpsdk.WithString("scheme", mcpsdk.Enum("go", "proto", "pxf", "sql", "md", "goasm", "ts", "py"), mcpsdk.Description("Only nodes of this layer.")),
		mcpsdk.WithNumber("limit", mcpsdk.DefaultNumber(defaultLimit), mcpsdk.Min(1), mcpsdk.Max(maxLimit)),
		withExcludeTests(),
		withExcludeGenerated(),
		withBudget(),
		withRoot(),
		mcpsdk.WithReadOnlyHintAnnotation(true),
	), s.handleFind)

	add(mcpsdk.NewTool("get_node",
		mcpsdk.WithDescription("One node: kind, name, position, attributes, and how many edges of each kind leave and arrive."),
		mcpsdk.WithString("id", mcpsdk.Required(), mcpsdk.Description("A node ID from find_nodes.")),
		withRoot(),
		mcpsdk.WithReadOnlyHintAnnotation(true),
	), s.handleGet)

	add(mcpsdk.NewTool("neighbors",
		append([]mcpsdk.ToolOption{
			mcpsdk.WithDescription("The nodes within depth hops of a node and the edges between them, nearest first, cut to max_tokens."),
			mcpsdk.WithString("id", mcpsdk.Required(), mcpsdk.Description("A node ID from find_nodes.")),
			withDirection(dirBoth),
			mcpsdk.WithNumber("depth", mcpsdk.DefaultNumber(defaultDepth), mcpsdk.Min(1), mcpsdk.Max(maxDepth)),
			withBudget(),
			withRoot(),
			mcpsdk.WithReadOnlyHintAnnotation(true),
		}, withFilter()...)...,
	), s.handleNeighbors)

	add(mcpsdk.NewTool("shortest_path",
		append([]mcpsdk.ToolOption{
			mcpsdk.WithDescription("The fewest edges from one node to another, as the chain of nodes and edges. Empty edges with found=false means no path within max_depth."),
			mcpsdk.WithString("from", mcpsdk.Required()),
			mcpsdk.WithString("to", mcpsdk.Required()),
			withDirection(dirOut),
			mcpsdk.WithNumber("max_depth", mcpsdk.DefaultNumber(maxPathDepth), mcpsdk.Min(1), mcpsdk.Max(maxPathDepth)),
			withBudget(),
			withRoot(),
			mcpsdk.WithReadOnlyHintAnnotation(true),
		}, withFilter()...)...,
	), s.handlePath)

	add(mcpsdk.NewTool("subgraph_for_question",
		append([]mcpsdk.ToolOption{
			mcpsdk.WithDescription("The neighbourhood of the declarations a question names, across layers: the best-matching seeds, then their relationship edges (not containment, unless edge_kinds asks), cut to max_tokens. Only the choice of seeds is heuristic."),
			mcpsdk.WithString("question", mcpsdk.Required(), mcpsdk.Description("A question naming declarations, e.g. \"where is WorkspaceConfig used\" or \"what calls Total\".")),
			mcpsdk.WithNumber("seeds", mcpsdk.DefaultNumber(5), mcpsdk.Min(1), mcpsdk.Max(50), mcpsdk.Description("How many best-matching nodes to expand from.")),
			mcpsdk.WithNumber("depth", mcpsdk.DefaultNumber(questionDepth), mcpsdk.Min(1), mcpsdk.Max(maxDepth),
				mcpsdk.Description("Hops from each seed; table -> generated code -> caller is 3.")),
			withBudget(),
			withRoot(),
			mcpsdk.WithReadOnlyHintAnnotation(true),
		}, withFilter()...)...,
	), s.handleQuestion)

	add(mcpsdk.NewTool("graph_issues",
		mcpsdk.WithDescription("Edges the graph dropped because an end names no node, folded by cause, and conflicts where two layers disagreed about one node."),
		mcpsdk.WithString("kind", mcpsdk.Enum("dropped", "conflicts", "both"), mcpsdk.DefaultString("both")),
		mcpsdk.WithString("prefix", mcpsdk.Description("Only items whose ID starts with this, e.g. sql://.")),
		withBudget(),
		withRoot(),
		mcpsdk.WithReadOnlyHintAnnotation(true),
	), s.handleIssues)

	add(mcpsdk.NewTool("rebuild",
		mcpsdk.WithDescription("Build the graph again from the working tree now. Rarely needed: every tool already rebuilds the changed layers first when files changed since the last build, and says so in rebuilt."),
		withRoot(),
	), s.handleRebuild)
}
