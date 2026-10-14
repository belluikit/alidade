// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package tsgo is alidade's door into typescript-go, TypeScript 7's
// compiler (ADR 007, amended for #215). typescript-go keeps every
// package under internal/, which Go lets only a package under
// github.com/microsoft/typescript-go import; this module lives at
// github.com/microsoft/typescript-go/shim for that reason alone, the
// pattern oxc's tsgolint uses. alidade's go.mod replaces both modules
// with the directories under third_party.
//
// It re-declares what the frontend layer uses and nothing else: type
// aliases, which carry their methods, the syntax kinds and flags
// (consts_generated.go, by gen.go), and the functions the layer calls.
// What it names is the whole surface a typescript-go release can break.
package tsgo

import (
	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/compiler"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/scanner"
	"github.com/microsoft/typescript-go/internal/tsoptions"
	"github.com/microsoft/typescript-go/internal/vfs"
	"github.com/microsoft/typescript-go/internal/vfs/cachedvfs"
	"github.com/microsoft/typescript-go/internal/vfs/osvfs"
)

// The syntax tree and the checker.
type (
	Node        = ast.Node
	SourceFile  = ast.SourceFile
	Symbol      = ast.Symbol
	Kind        = ast.Kind
	SymbolFlags = ast.SymbolFlags
	Checker     = checker.Checker
	Type        = checker.Type
	Program     = compiler.Program
	Diagnostic  = ast.Diagnostic
	FS          = vfs.FS
)

// GetSourceFileOfNode is the file a node is in.
func GetSourceFileOfNode(n *Node) *SourceFile { return ast.GetSourceFileOfNode(n) }

// ImplementsTypes are the types a class's implements clause names.
func ImplementsTypes(class *Node) []*Node { return ast.GetImplementsTypeNodes(class) }

// ExtendsType is the type a class's extends clause names, or nil.
func ExtendsType(class *Node) *Node { return ast.GetClassExtendsHeritageElement(class) }

// IsVarConst reports whether a variable declaration is a const.
func IsVarConst(n *Node) bool { return ast.IsVarConst(n) }

// IsDefaultExport reports whether a declaration is its module's export
// default.
func IsDefaultExport(n *Node) bool { return ast.HasSyntacticModifier(n, ast.ModifierFlagsDefault) }

// SkipParentheses is the expression inside any parentheses.
func SkipParentheses(n *Node) *Node { return ast.SkipParentheses(n) }

// SkipTrivia is the position of the first token at or after pos.
func SkipTrivia(text string, pos int) int { return scanner.SkipTrivia(text, pos) }

// LineAndByte is the 0-based line of a position, and its byte offset in
// that line.
func LineAndByte(f *SourceFile, pos int) (line, byteOffset int) {
	return scanner.GetECMALineAndByteOffsetOfPosition(f, pos)
}

// FileSystem is the operating system's, with TypeScript's own lib.*.d.ts
// served from the binary: without them no member of a built-in type
// resolves.
func FileSystem() FS { return bundled.WrapFS(cachedvfs.From(osvfs.FS())) }

// A Host is a compiler host over a file system.
type Host = compiler.CompilerHost

// NewHost is a compiler host for programs under dir.
func NewHost(dir string, fs FS) Host {
	return compiler.NewCompilerHost(dir, fs, bundled.LibPath(), nil, nil)
}

// A Config is a parsed tsconfig.
type Config = tsoptions.ParsedCommandLine

// ParseConfig reads a tsconfig.json, its extends chain included.
func ParseConfig(path string, host Host) (*Config, []*Diagnostic) {
	return tsoptions.GetParsedCommandLineOfConfigFile(path, &core.CompilerOptions{}, nil, host, nil)
}

// InferredConfig is the configuration of files no tsconfig includes:
// the options TypeScript's editor support gives such a file, JSX and
// JavaScript allowed, modules resolved as a bundler does.
func InferredConfig(files []string) *Config {
	return &tsoptions.ParsedCommandLine{ParsedConfig: &core.ParsedOptions{
		CompilerOptions: &core.CompilerOptions{
			AllowJs:                    core.TSTrue,
			Module:                     core.ModuleKindESNext,
			ModuleResolution:           core.ModuleResolutionKindBundler,
			Target:                     core.ScriptTargetES2022,
			Jsx:                        core.JsxEmitReactJSX,
			AllowImportingTsExtensions: core.TSTrue,
			ESModuleInterop:            core.TSTrue,
			AllowNonTsExtensions:       core.TSTrue,
			ResolveJsonModule:          core.TSTrue,
		},
		FileNames: files,
	}}
}

// NewProgram builds a program: its files parsed and bound, checked on
// demand. A config's project references are read from their sources.
func NewProgram(config *Config, host Host) *Program {
	p := compiler.NewProgram(compiler.ProgramOptions{
		Config:                      config,
		Host:                        host,
		UseSourceOfProjectReference: true,
		SingleThreaded:              core.TSFalse,
	})
	if p != nil {
		p.BindSourceFiles()
	}
	return p
}

// ConfigFiles are the files a parsed config includes.
func ConfigFiles(c *Config) []string { return c.FileNames() }

// AddFiles adds root files to a parsed config before its program is
// built: the translations of the Svelte components in its directory.
func AddFiles(c *Config, files []string) {
	c.ParsedConfig.FileNames = append(c.ParsedConfig.FileNames, files...)
}
