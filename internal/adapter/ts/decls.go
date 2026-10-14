// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package ts

import (
	"slices"

	tsgo "github.com/microsoft/typescript-go/shim"

	"github.com/belluikit/alidade/internal/graph"
)

// declPath is a declaration's path when the layer gives it a node — its
// top-level name, and for a member the name of the declaration holding
// it — or nil. One function decides both what is a node and what a call
// lands on, so the two cannot disagree.
func declPath(n *tsgo.Node) []string {
	if top := topLevelName(n); top != "" {
		return []string{top}
	}
	holder := memberHolder(n)
	if holder == nil || !memberDeclaration(n) {
		return nil
	}
	outer := topLevelName(holder)
	name := identifierName(n.Name())
	if outer == "" || name == "" {
		return nil
	}
	return []string{outer, name}
}

// topLevelName is the name of a declaration made at module level: a
// function, class, interface, type alias or enum, or one variable of a
// variable statement. A default export without a name is "default".
func topLevelName(n *tsgo.Node) string {
	switch n.Kind {
	case tsgo.KindFunctionDeclaration, tsgo.KindClassDeclaration, tsgo.KindInterfaceDeclaration,
		tsgo.KindTypeAliasDeclaration, tsgo.KindEnumDeclaration:
		if !atModuleLevel(n) {
			return ""
		}
		if n.Name() == nil && tsgo.IsDefaultExport(n) {
			return "default"
		}
		return identifierName(n.Name())
	case tsgo.KindVariableDeclaration:
		list := n.Parent
		if list == nil || list.Parent == nil || list.Parent.Kind != tsgo.KindVariableStatement || !atModuleLevel(list.Parent) {
			return ""
		}
		return identifierName(n.Name())
	}
	return ""
}

func atModuleLevel(n *tsgo.Node) bool {
	return n.Parent != nil && n.Parent.Kind == tsgo.KindSourceFile
}

// memberHolder is the top-level declaration whose body holds n directly:
// a class or interface, the type alias of a type literal, or the
// variable an object literal initializes.
func memberHolder(n *tsgo.Node) *tsgo.Node {
	body := n.Parent
	if body == nil {
		return nil
	}
	switch body.Kind {
	case tsgo.KindClassDeclaration, tsgo.KindInterfaceDeclaration:
		return body
	case tsgo.KindTypeLiteral:
		if body.Parent != nil && body.Parent.Kind == tsgo.KindTypeAliasDeclaration {
			return body.Parent
		}
	case tsgo.KindObjectLiteralExpression:
		if v := body.Parent; v != nil && v.Kind == tsgo.KindVariableDeclaration && tsgo.SkipParentheses(v.Initializer()) == body {
			return v
		}
	}
	return nil
}

// memberDeclaration reports whether a member of a holder is one the
// layer gives a node: any named member of a class, interface or type
// literal, and a function-valued property of an object literal — its
// data properties are values, not declarations anyone calls.
func memberDeclaration(n *tsgo.Node) bool {
	switch n.Kind {
	case tsgo.KindMethodDeclaration, tsgo.KindGetAccessor, tsgo.KindSetAccessor:
		return true
	case tsgo.KindPropertyDeclaration, tsgo.KindPropertySignature, tsgo.KindMethodSignature:
		return n.Parent.Kind != tsgo.KindObjectLiteralExpression
	case tsgo.KindPropertyAssignment:
		return functionValue(n.Initializer())
	}
	return false
}

// identifierName is a declaration name the graph can key: an identifier,
// or a string or numeric literal; a computed name is none.
func identifierName(name *tsgo.Node) string {
	if name == nil {
		return ""
	}
	switch name.Kind {
	case tsgo.KindIdentifier, tsgo.KindPrivateIdentifier, tsgo.KindStringLiteral, tsgo.KindNumericLiteral:
		return name.Text()
	}
	return ""
}

// functionValue reports whether an expression is a function: an arrow or
// function expression, through parentheses and type assertions.
func functionValue(e *tsgo.Node) bool {
	e = unwrap(e)
	return e != nil && (e.Kind == tsgo.KindArrowFunction || e.Kind == tsgo.KindFunctionExpression)
}

// unwrap is an expression without the parentheses, as and satisfies
// around it.
func unwrap(e *tsgo.Node) *tsgo.Node {
	for e != nil && (e.Kind == tsgo.KindParenthesizedExpression || e.Kind == tsgo.KindAsExpression || e.Kind == tsgo.KindSatisfiesExpression) {
		e = e.Expression()
	}
	return e
}

// declKind is the node kind of a declaration declPath names.
func declKind(n *tsgo.Node) graph.Kind {
	switch n.Kind {
	case tsgo.KindFunctionDeclaration:
		return graph.KindFunc
	case tsgo.KindClassDeclaration, tsgo.KindInterfaceDeclaration, tsgo.KindTypeAliasDeclaration:
		return graph.KindType
	case tsgo.KindEnumDeclaration:
		return graph.KindEnum
	case tsgo.KindVariableDeclaration:
		switch {
		case functionValue(n.Initializer()), wrappedFunction(n.Initializer()):
			return graph.KindFunc
		case tsgo.IsVarConst(n):
			return graph.KindConst
		}
		return graph.KindVar
	case tsgo.KindPropertyDeclaration, tsgo.KindPropertySignature:
		if functionValue(n.Initializer()) || n.Type() != nil && n.Type().Kind == tsgo.KindFunctionType {
			return graph.KindMethod
		}
		return graph.KindField
	}
	return graph.KindMethod
}

// fullPath is a declaration's path when it has a node: base's — the
// module-level and member rule, declPath or componentDeclPath — or, for a
// named function nested inside a declaration that has one, that
// declaration's path and its own name (#235, the owner's decision):
// attachSecretEditGuard.protectedAt, Editor.save. A React component's
// handlers and a hook's callbacks are what a reader asks about, and
// without nodes their calls were the enclosing component's. Derived from
// the declaration's place alone, as declPath is, so a call into another
// file lands on the ID that file's walk mints. Two nested functions of
// one name in one declaration are one node.
func fullPath(n *tsgo.Node, base func(*tsgo.Node) []string) []string {
	if p := base(n); p != nil {
		return p
	}
	name := nestedFunctionName(n)
	if name == "" || synthesized(name) {
		return nil
	}
	path := []string{name}
	for a := n.Parent; a != nil && a.Kind != tsgo.KindSourceFile; a = a.Parent {
		if p := fullPath(a, base); p != nil {
			return append(slices.Clone(p), path...)
		}
		if holder := objectHolderName(a); holder != "" {
			path = append([]string{holder}, path...)
		}
	}
	return nil
}

// objectHolderName is the name of a property or variable holding an
// object literal, which a nested function's path goes through:
// api.fs.readDir, not api.readDir, which would merge with
// api.workspace.readDir.
func objectHolderName(n *tsgo.Node) string {
	switch {
	case n.Kind == tsgo.KindPropertyAssignment && n.Parent != nil && n.Parent.Kind == tsgo.KindObjectLiteralExpression:
	case n.Kind == tsgo.KindVariableDeclaration:
	default:
		return ""
	}
	if v := unwrap(n.Initializer()); v == nil || v.Kind != tsgo.KindObjectLiteralExpression {
		return ""
	}
	return identifierName(n.Name())
}

// nestedFunctionName is the name of a named function a body declares: a
// function declaration; a variable holding a function, or React's
// useCallback on one; a named method or function-valued property of an
// object literal. An anonymous callback has none.
func nestedFunctionName(n *tsgo.Node) string {
	switch n.Kind {
	case tsgo.KindFunctionDeclaration:
		return identifierName(n.Name())
	case tsgo.KindVariableDeclaration:
		if functionValue(n.Initializer()) || wrappedFunction(n.Initializer()) {
			if name := n.Name(); name != nil && name.Kind == tsgo.KindIdentifier {
				return name.Text()
			}
		}
	}
	return objectFunctionName(n)
}

// objectFunctionName is the name of an object literal's named method,
// accessor or function-valued property.
func objectFunctionName(n *tsgo.Node) string {
	if n.Parent == nil || n.Parent.Kind != tsgo.KindObjectLiteralExpression {
		return ""
	}
	switch n.Kind {
	case tsgo.KindMethodDeclaration, tsgo.KindGetAccessor, tsgo.KindSetAccessor:
		return identifierName(n.Name())
	case tsgo.KindPropertyAssignment:
		if functionValue(n.Initializer()) {
			return identifierName(n.Name())
		}
	}
	return ""
}

// wrappedFunction reports whether an expression is React's useCallback on
// a function, whose result is that function: useCallback(() => …, deps),
// React.useCallback(…). Not useMemo, whose result is what its function
// returns; not memo or forwardRef, which at module level would turn a
// component's const into a func.
func wrappedFunction(e *tsgo.Node) bool {
	if e == nil || e.Kind != tsgo.KindCallExpression || len(e.Arguments()) == 0 || !functionValue(e.Arguments()[0]) {
		return false
	}
	callee := e.Expression()
	if callee.Kind == tsgo.KindPropertyAccessExpression {
		callee = callee.Name()
	}
	return callee.Kind == tsgo.KindIdentifier && callee.Text() == "useCallback"
}
