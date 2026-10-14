// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/trendvidia/alidade/internal/adapter/ts/svelte"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/trendvidia/alidade/internal/graph"
)

// The frontend calls oracle (#215): the repository's own typescript
// package — TypeScript 5 or 6 under Node, where the layer runs TypeScript
// 7's Go compiler in process — resolving every call, new and JSX element
// of the files the layer reads, and a second implementation of the
// layer's declaration rules (ADR 007, amended for #215), in JavaScript,
// naming the declaration holding each call and the one it reaches. Each
// file is checked under the nearest tsconfig.json above it, where the
// layer takes the deepest project that includes it: the two rules agree
// on a tree whose projects do not overlap, and the comparison says where
// they do not.

// TSCalls is the oracle's view of the frontend calls.
type TSCalls struct {
	// Edges are [caller, callee] graph IDs: the declaration holding a
	// call, or its file at module level, and the declaration of the tree
	// the call resolves to.
	Edges [][2]graph.ID
	// Files are the files walked; Sites the calls, news and JSX elements
	// in them, of which Resolved reached a declaration anywhere and
	// InTree one the edges name.
	Files                   map[graph.ID]bool
	Sites, Resolved, InTree int
}

// tsCallsScript reads "file\tgenerated" lines on stdin and writes a
// "CALL\tcaller file\tcaller path\tcallee file\tcallee path" line per
// call reaching a declaration of those files, and a final
// "SITES\tsites\tresolved\tin tree" line.
const tsCallsScript = `
const path = require('path'), fs = require('fs');
const ts = require(process.argv[2]);
const [bundlePath, shimDir] = process.argv.slice(3);
// own: every file the layer reads, its kind: '' a script, 'gen' a
// generated one, 'comp' a Svelte component.
const kinds = new Map(fs.readFileSync(0, 'utf8').split('\n').filter(Boolean).map(l => l.split('\t')));
const own = new Map([...kinds].filter(([, k]) => k !== 'comp').map(([f, k]) => [f, k === 'gen']));
// Components (#221): translated by svelte2tsx — the bundle the layer
// runs in V8, run here in Node — and read as Card.svelte.tsx.
const virtual = new Map();
if (bundlePath) {
  new Function('process', 'require', 'module', fs.readFileSync(bundlePath, 'utf8'))(undefined, undefined, undefined);
  for (const [f, k] of kinds) {
    if (k === 'comp') virtual.set(f + '.tsx', JSON.parse(globalThis.convert(fs.readFileSync(f, 'utf8'), f)).code);
  }
}
const shims = shimDir ? fs.readdirSync(shimDir).map(n => path.join(shimDir, n)) : [];
const host = { ...ts.sys, onUnRecoverableConfigFileDiagnostic: () => {} };
const fallback = { allowJs: true, jsx: ts.JsxEmit.ReactJSX, module: ts.ModuleKind.ESNext, moduleResolution: ts.ModuleResolutionKind.Bundler, target: ts.ScriptTarget.ES2022 };
function configOf(file) {
  for (let d = path.dirname(file); ; ) {
    for (const n of ['tsconfig.json', 'jsconfig.json']) { const c = path.join(d, n); if (fs.existsSync(c)) return c; }
    const up = path.dirname(d);
    if (up === d) return '';
    d = up;
  }
}
// The layer's declaration rules, written again.
const K = ts.SyntaxKind;
function valid(name) { return name !== '' && !/[#/.\s]/.test(name) && !/[\u0000-\u001f]/.test(name); }
function nameText(n) {
  if (!n) return '';
  switch (n.kind) { case K.Identifier: case K.PrivateIdentifier: case K.StringLiteral: case K.NumericLiteral: return n.text; }
  return '';
}
function isDefault(n) { return (ts.getCombinedModifierFlags(n) & ts.ModifierFlags.Default) !== 0; }
function topLevelName(n) {
  switch (n.kind) {
    case K.FunctionDeclaration: case K.ClassDeclaration: case K.InterfaceDeclaration: case K.TypeAliasDeclaration: case K.EnumDeclaration:
      if (!n.parent || n.parent.kind !== K.SourceFile) return '';
      if (!n.name && isDefault(n)) return 'default';
      return nameText(n.name);
    case K.VariableDeclaration: {
      const list = n.parent, stmt = list && list.parent;
      if (!stmt || stmt.kind !== K.VariableStatement || !stmt.parent || stmt.parent.kind !== K.SourceFile) return '';
      return nameText(n.name);
    }
  }
  return '';
}
function functionValue(e) {
  while (e) {
    switch (e.kind) {
      case K.ArrowFunction: case K.FunctionExpression: return true;
      case K.ParenthesizedExpression: case K.AsExpression: case K.SatisfiesExpression: e = e.expression; break;
      default: return false;
    }
  }
  return false;
}
function holderOf(n) {
  const body = n.parent;
  if (!body) return null;
  switch (body.kind) {
    case K.ClassDeclaration: case K.InterfaceDeclaration: return body;
    case K.TypeLiteral: return body.parent && body.parent.kind === K.TypeAliasDeclaration ? body.parent : null;
    case K.ObjectLiteralExpression: {
      const v = body.parent;
      return v && v.kind === K.VariableDeclaration && ts.skipParentheses(v.initializer) === body ? v : null;
    }
  }
  return null;
}
function isMember(n) {
  switch (n.kind) {
    case K.MethodDeclaration: case K.GetAccessor: case K.SetAccessor: return true;
    case K.PropertyDeclaration: case K.PropertySignature: case K.MethodSignature: return n.parent.kind !== K.ObjectLiteralExpression;
    case K.PropertyAssignment: return functionValue(n.initializer);
  }
  return false;
}
function declPath(n) {
  const top = topLevelName(n);
  if (top) return valid(top) ? [top] : null;
  const h = holderOf(n);
  if (!h || !isMember(n)) return null;
  const outer = topLevelName(h), name = nameText(n.name);
  return outer && name && valid(outer) && valid(name) ? [outer, name] : null;
}
// The rules for a translation, written again: svelte2tsx's names are not
// declarations, the instance script's are (the statements of $$render),
// its default export is the component, a component used in markup is a
// new of const $$_x = __sveltets_2_ensureComponent(C), and the calls it
// writes for itself are no call sites.
function synthesized(name) { return name.startsWith('$$') || name.startsWith('__sveltets') || name.endsWith('__SvelteComponent_'); }
function renderLevelName(n) {
  let stmt = n;
  switch (n.kind) {
    case K.FunctionDeclaration: case K.ClassDeclaration: case K.InterfaceDeclaration: case K.TypeAliasDeclaration: case K.EnumDeclaration: break;
    case K.VariableDeclaration:
      if (!n.parent || !n.parent.parent || n.parent.parent.kind !== K.VariableStatement) return '';
      stmt = n.parent.parent; break;
    default: return '';
  }
  const body = stmt.parent, fn = body && body.parent;
  if (!body || body.kind !== K.Block || !fn || fn.kind !== K.FunctionDeclaration || !fn.parent || fn.parent.kind !== K.SourceFile || nameText(fn.name) !== '$$render') return '';
  return nameText(n.name);
}
function compDeclPath(n) {
  const dp = declPath(n);
  if (dp) return synthesized(dp[0]) ? null : dp;
  const name = renderLevelName(n);
  return name && valid(name) && !synthesized(name) ? [name] : null;
}
// Nested named functions (#235), written again: a function declaration,
// a variable holding a function or useCallback's call on one, an object literal's named method or function property,
// inside a declaration with a path, is that path, the names of the
// objects holding it, and its own name.
function unwrap(e) {
  while (e && (e.kind === K.ParenthesizedExpression || e.kind === K.AsExpression || e.kind === K.SatisfiesExpression)) e = e.expression;
  return e;
}
function wrapped(e) {
  if (!e || e.kind !== K.CallExpression || !e.arguments.length || !functionValue(e.arguments[0])) return false;
  const c = e.expression.kind === K.PropertyAccessExpression ? e.expression.name : e.expression;
  return c.kind === K.Identifier && c.text === 'useCallback';
}
function nestedName(n) {
  const inObject = n.parent && n.parent.kind === K.ObjectLiteralExpression;
  switch (n.kind) {
    case K.FunctionDeclaration: return nameText(n.name);
    case K.VariableDeclaration:
      return (functionValue(n.initializer) || wrapped(n.initializer)) && n.name.kind === K.Identifier ? n.name.text : '';
    case K.MethodDeclaration: case K.GetAccessor: case K.SetAccessor: return inObject ? nameText(n.name) : '';
    case K.PropertyAssignment: return inObject && functionValue(n.initializer) ? nameText(n.name) : '';
  }
  return '';
}
function objectHolder(n) {
  if (n.kind !== K.VariableDeclaration && !(n.kind === K.PropertyAssignment && n.parent.kind === K.ObjectLiteralExpression)) return '';
  const v = unwrap(n.initializer);
  return v && v.kind === K.ObjectLiteralExpression ? nameText(n.name) : '';
}
function fullPath(n, base) {
  const p = base(n);
  if (p) return p;
  const name = nestedName(n);
  if (!name || !valid(name) || synthesized(name)) return null;
  const path = [name];
  for (let a = n.parent; a && a.kind !== K.SourceFile; a = a.parent) {
    const ap = fullPath(a, base);
    if (ap) return [...ap, ...path];
    const h = objectHolder(a);
    if (h) path.unshift(h);
  }
  return null;
}
function isComponentExport(d) {
  return d.kind === K.VariableDeclaration && d.parent && d.parent.parent && d.parent.parent.parent && d.parent.parent.parent.kind === K.SourceFile && nameText(d.name).endsWith('__SvelteComponent_');
}
function renderedOf(sym) {
  const d = sym && sym.declarations && sym.declarations[0];
  if (!d || d.kind !== K.VariableDeclaration || !nameText(d.name).startsWith('$$_')) return null;
  const i = d.initializer;
  if (!i || i.kind !== K.CallExpression || i.arguments.length !== 1 || i.expression.kind !== K.Identifier || i.expression.text !== '__sveltets_2_ensureComponent') return null;
  return i.arguments[0];
}
function synthesizedCall(n) {
  const e = n.kind === K.CallExpression || n.kind === K.NewExpression ? n.expression : null;
  if (!e) return false;
  if (e.kind === K.Identifier) return e.text.startsWith('__sveltets') || e.text === '$$render';
  return e.kind === K.PropertyAccessExpression && e.expression.kind === K.Identifier && e.expression.text === 'svelteHTML';
}
function callee(n) {
  let e = null;
  switch (n.kind) {
    case K.CallExpression: case K.NewExpression: e = n.expression; break;
    case K.JsxOpeningElement: case K.JsxSelfClosingElement: e = n.tagName; break;
    default: return null;
  }
  return e && e.kind === K.PropertyAccessExpression ? e.name : e;
}
const parsed = new Map();
function parse(c) {
  if (!parsed.has(c)) parsed.set(c, c ? ts.getParsedCommandLineOfConfigFile(c, {}, host) : null);
  return parsed.get(c);
}
// projectOf is the nearest tsconfig above a file, or for a solution
// config (no files, only references) the referenced one that includes it.
function projectOf(file) {
  const c = configOf(file), p = parse(c);
  if (!p || p.fileNames.length > 0 || !p.projectReferences) return c;
  for (const r of p.projectReferences) {
    const rc = ts.resolveProjectReferencePath(r), rp = parse(rc);
    if (rp && rp.fileNames.includes(file)) return rc;
  }
  return c;
}
let sites = 0, resolved = 0, inTree = 0;
const groups = new Map();
const files = [...own.keys(), ...virtual.keys()];
for (const f of files) { const c = projectOf(virtual.has(f) ? f.slice(0, -4) : f); if (!groups.has(c)) groups.set(c, []); groups.get(c).push(f); }
// target is the [file, path] a declaration is, for a CALL line, or null.
function target(d) {
  const df = d.getSourceFile().fileName;
  if (virtual.has(df)) {
    if (isComponentExport(d)) return [df.slice(0, -4), ''];
    const tp = fullPath(d, compDeclPath);
    return tp ? [df.slice(0, -4), tp.join('.')] : null;
  }
  if (!own.has(df)) return null;
  const tp = fullPath(d, declPath);
  return !tp || (own.get(df) && tp.length > 1) ? null : [df, tp.join('.')];
}
// Dispatch through interfaces (#229), written again: a class whose
// implements clause names an interface implements every member of it and
// of the interfaces it extends, with its own member or an inherited one;
// a call to an interface member also reaches each implementation.
const impls = new Map(); // "file\tpath" of an interface member -> Set of "file\tpath"
const ifaceCalls = [];   // [caller file, owner path, "file\tpath" of the member]
const key = t => t[0] + '\t' + t[1];
for (const [c, group] of groups) {
  const p = parse(c);
  const options = { ...(p ? p.options : fallback), noEmit: true };
  const host = ts.createCompilerHost(options);
  const { fileExists, readFile, getSourceFile } = host;
  host.fileExists = f => virtual.has(f) || fileExists.call(host, f);
  host.readFile = f => virtual.has(f) ? virtual.get(f) : readFile.call(host, f);
  host.getSourceFile = (f, lang, ...rest) => virtual.has(f) ? ts.createSourceFile(f, virtual.get(f), lang, true, ts.ScriptKind.TSX) : getSourceFile.call(host, f, lang, ...rest);
  const extra = group.some(f => virtual.has(f)) ? shims : [];
  const prog = ts.createProgram({ rootNames: [...new Set([...(p ? p.fileNames : []), ...group, ...extra])], options, host });
  const checker = prog.getTypeChecker();
  const resolve = e => { let s = checker.getSymbolAtLocation(e); if (s && s.flags & ts.SymbolFlags.Alias) s = checker.getAliasedSymbol(s); return s; };
  for (const f of group) {
    if (own.get(f)) continue;
    const comp = virtual.has(f), caller = comp ? f.slice(0, -4) : f;
    const sf = prog.getSourceFile(f);
    if (!sf) continue;
    const visit = (n, owner) => {
      const dp = fullPath(n, comp ? compDeclPath : declPath);
      if (dp) owner = dp.join('.');
      if (dp && n.kind === K.ClassDeclaration && n.heritageClauses) {
        const csym = checker.getSymbolAtLocation(n.name);
        const inst = csym && checker.getDeclaredTypeOfSymbol(csym);
        const seen = new Set();
        const walkIface = t => {
          if (!t || !t.symbol || seen.has(t.symbol)) return;
          seen.add(t.symbol);
          for (const m of checker.getPropertiesOfType(t)) {
            const mt = (m.declarations || []).map(target).find(Boolean);
            const own = inst && checker.getPropertyOfType(inst, m.name);
            const it = own && (own.declarations || []).map(target).find(Boolean);
            if (!mt || !it || key(mt) === key(it)) continue;
            if (!impls.has(key(mt))) impls.set(key(mt), new Set());
            impls.get(key(mt)).add(key(it));
          }
          for (const b of checker.getBaseTypes(t) || []) walkIface(b);
        };
        for (const h of n.heritageClauses) {
          if (h.token !== K.ImplementsKeyword) continue;
          for (const tn of h.types) {
            const s = resolve(tn.expression);
            if (s) walkIface(checker.getDeclaredTypeOfSymbol(s));
          }
        }
      }
      const t = callee(n);
      if (t && !(comp && synthesizedCall(n))) {
        sites++;
        let sym = resolve(t);
        const r = renderedOf(sym);
        if (r) sym = resolve(r);
        if (sym && sym.declarations && sym.declarations.length) {
          resolved++;
          for (const d of sym.declarations) {
            const tg = target(d);
            if (!tg) continue;
            inTree++;
            process.stdout.write(['CALL', caller, owner, tg[0], tg[1]].join('\t') + '\n');
            if (d.parent && (d.parent.kind === K.InterfaceDeclaration || d.parent.kind === K.TypeLiteral)) ifaceCalls.push([caller, owner, key(tg)]);
            break;
          }
        }
      }
      ts.forEachChild(n, c => visit(c, owner));
    };
    ts.forEachChild(sf, c => visit(c, ''));
  }
}
for (const [caller, owner, member] of ifaceCalls) {
  for (const impl of impls.get(member) || []) process.stdout.write(['CALL', caller, owner, ...impl.split('\t')].join('\t') + '\n');
}
process.stdout.write(['SITES', sites, resolved, inTree].join('\t') + '\n');
`

// FrontendCalls runs the calls oracle over the frontend files of g the
// type checker reads — every script file, and every component when the
// build translates them (#221) — with the
// repository's typescript package. A tree without one returns nil.
func FrontendCalls(g *graph.Graph, m *graph.Minter, root string) (*TSCalls, error) {
	tsPkg := typescriptPackage(root)
	if tsPkg == "" {
		return nil, nil
	}
	var in strings.Builder
	n := 0
	for node := range g.Nodes() {
		comp := node.Kind == graph.KindComponent && svelte.Available()
		if node.Kind != graph.KindModule && !comp || !strings.HasPrefix(string(node.ID), "ts://") || node.Pos.File == "" {
			continue
		}
		gen := ""
		switch {
		case comp:
			gen = "comp"
		case node.Generated:
			gen = "gen"
		}
		fmt.Fprintf(&in, "%s\t%s\n", filepath.Join(root, graph.FilePath(node.Pos.File)), gen)
		n++
	}
	if n == 0 {
		return nil, nil
	}
	script := filepath.Join(os.TempDir(), "alidade-oracle-calls.cjs")
	if err := os.WriteFile(script, []byte(tsCallsScript), 0o644); err != nil {
		return nil, err
	}
	defer os.Remove(script)
	args := []string{script, tsPkg}
	if svelte.Available() {
		bundle, shims, cleanup, err := svelteFiles()
		if err != nil {
			return nil, err
		}
		defer cleanup()
		args = append(args, bundle, shims)
	}
	cmd := exec.Command("node", args...)
	cmd.Stdin = strings.NewReader(in.String())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("oracle: typescript calls: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return readTSCalls(m, out)
}

func readTSCalls(m *graph.Minter, out []byte) (*TSCalls, error) {
	c := &TSCalls{Files: map[graph.ID]bool{}}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		switch {
		case len(f) == 4 && f[0] == "SITES":
			c.Sites, _ = strconv.Atoi(f[1])
			c.Resolved, _ = strconv.Atoi(f[2])
			c.InTree, _ = strconv.Atoi(f[3])
		case len(f) == 5 && f[0] == "CALL":
			from, err := declID(m, f[1], f[2])
			if err != nil {
				continue
			}
			to, err := declID(m, f[3], f[4])
			if err != nil {
				continue
			}
			file, _ := m.TSID(f[1], "")
			c.Files[file] = true
			c.Edges = append(c.Edges, [2]graph.ID{from, to})
		}
	}
	return c, sc.Err()
}

// declID is the graph ID of a declaration path in a file, or of the file
// for an empty path.
func declID(m *graph.Minter, file, path string) (graph.ID, error) {
	if path == "" {
		return m.TSID(file, "")
	}
	return m.TSDeclID(file, strings.Split(path, ".")...)
}

// A TSCallsResult compares the frontend layer's calls edges with the
// oracle's, both ways: Edges is [distinct oracle edges, of them in the
// graph], Missing the oracle's the graph lacks, Extra the graph's from a
// file the oracle walked that the oracle does not have.
type TSCallsResult struct {
	Files, Sites, Resolved, InTree int
	Edges                          [2]int
	Missing, Extra                 []string
}

// CompareTSCalls measures g's frontend calls edges against the oracle.
func CompareTSCalls(g *graph.Graph, c *TSCalls) *TSCallsResult {
	r := &TSCallsResult{Files: len(c.Files), Sites: c.Sites, Resolved: c.Resolved, InTree: c.InTree}
	want := map[[2]graph.ID]bool{}
	for _, e := range c.Edges {
		want[e] = true
	}
	have := map[[2]graph.ID]bool{}
	for e := range g.Edges() {
		if e.Kind != graph.KindCalls || !strings.HasPrefix(string(e.From), "ts://") {
			continue
		}
		key := [2]graph.ID{e.From, e.To}
		have[key] = true
		file, _, _ := strings.Cut(string(e.From), "#")
		if !want[key] && c.Files[graph.ID(file)] {
			r.Extra = append(r.Extra, string(e.From)+" -> "+string(e.To))
		}
	}
	for e := range want {
		r.Edges[0]++
		if have[e] {
			r.Edges[1]++
		} else {
			r.Missing = append(r.Missing, string(e[0])+" -> "+string(e[1]))
		}
	}
	sortAll(&r.Missing, &r.Extra)
	return r
}

func (r *TSCallsResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ts calls %d / %d distinct edges (%d extra) over %d files with calls; TypeScript: %d call sites, %d resolved, %d to a declaration of the tree\n",
		r.Edges[1], r.Edges[0], len(r.Extra), r.Files, r.Sites, r.Resolved, r.InTree)
	for _, l := range []struct {
		name  string
		items []string
	}{{"missing ts call", r.Missing}, {"extra ts call", r.Extra}} {
		for i, s := range l.items {
			if i == 8 {
				fmt.Fprintf(&b, "%s ... and %d more\n", l.name, len(l.items)-8)
				break
			}
			fmt.Fprintf(&b, "%s %s\n", l.name, s)
		}
	}
	return b.String()
}

// svelteFiles writes svelte2tsx's bundle and its shims where Node reads
// them, for the components (#221).
func svelteFiles() (bundle, shims string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "alidade-oracle-svelte")
	if err != nil {
		return "", "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	js, err := svelte.Bundle()
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	bundle = filepath.Join(dir, "svelte2tsx.js")
	shims = filepath.Join(dir, "shims")
	if err = os.WriteFile(bundle, js, 0o644); err == nil {
		err = os.MkdirAll(shims, 0o755)
	}
	for name, body := range svelte.Shims {
		if err == nil {
			err = os.WriteFile(filepath.Join(shims, name), []byte(body), 0o644)
		}
	}
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	return bundle, shims, cleanup, nil
}
