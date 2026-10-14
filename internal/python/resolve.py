# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TrendVidia, LLC.

# Reads a repository's Python files with ast and resolves their imports and
# calls with jedi (#237, ADR 008).
#
# IN is the root, then one job per line: a repo-relative file, a shard and
# a shard count. A shard resolves every shard-count-th call site of the
# file, so one large file can be split across workers; shard 0 also
# reports the file's declarations and imports. A first line "stream" has
# a worker write each job's lines to its standard output as soon as the
# job is done (#269), and OUT is empty; a first line "names" answers every
# file by the warm-up pass below instead of jedi, its call sites by name
# alone (#269), and a line "TABLE" may follow the jobs, then files the
# pass knows of without answering for them. OUT is one line per fact,
# tab-separated, each job's ending with
#   DONE file shard                   the job's lines are all written
#   DECL file path kind line col end-line
#     a class, function, method, module-level variable or class attribute;
#     path is the dotted qualified name: Square.area, main.inner
#   IMPORT file line target-file      an import jedi resolves into the root
#   IMPORT-OUTSIDE file line          one it resolves to the stdlib or a package
#   IMPORT-NONE file line             one it does not resolve
#   CALL file line col caller target-file target-line target-name target-type
#     a call site resolved to a function or class inside the root; caller is
#     the dotted path of the functions and classes holding it, empty at
#     module level. A call through a variable holding functions gives one
#     line per function.
#   REF file line col holder target-file target-line target-name function
#     a function inside the root named as a value, not called: f = g,
#     return g, sorted(xs, key=g); holder as a CALL's caller
#   A read of a property inside the root, r.b64, is a CALL of its function.
#   BASE file line col class target-file target-line target-name
#     a class's base, resolved to a class inside the root; class is the
#     dotted path of the class whose base it is
#   BASE-OUTSIDE file line col, BASE-UNPLACED, BASE-NONE
#     a base resolved outside, inside to no class, or not at all, as for
#     calls below
#   OUTSIDE file line col     a call resolved to the stdlib, a package or a builtin
#   UNPLACED file line col    one resolved inside the root to no function or
#                             class: a parameter, a local value, an instance
#   NONE file line col        one not resolved
#   SKIP file reason          a file not read
#   DEP file target-file      a file inside the root jedi parsed for it
#   DEP-MODULE file name      a module name jedi looked up for it, found or not
#   DEP-NAME file name        a name jedi searched the project's files for
#   DEP-ALL file              a search stopped at jedi's cap on files opened
#     (see Record: what a file's answers depend on, #269)
# A call site is a call whose callee is a name or an attribute — f(), a.f()
# — a decorator naming one, @f, an expression whose value is called:
# make()(x), table[k](x), or a with statement's item, which calls its
# value's __enter__ and __exit__ (__aenter__, __aexit__ for async with).
import ast
import os
import re

import jedi
import parso.cache
from jedi.inference import InferenceState
from jedi.inference import references as jedi_references
from jedi.inference.compiled.subprocess import functions as jedi_functions
from jedi.inference.base_value import ValueSet
from jedi.inference.value import function as jedi_function

# jedi infers a parameter from the function's callers, searching every
# file of the project for them. A call through a parameter is not placed
# (ADR 008), and the search was 40% of graphify's time; callers in the
# same file are still read.
jedi.settings.dynamic_params_for_other_modules = False


# jedi infers a function's result from its return annotation when it has
# one, and only otherwise from its return statements. An annotation naming
# Callable names no function, which hides the closure a factory returns
# (inc = adder(1); inc(x)), so for one the return statements are inferred
# too (#258). This patches jedi 0.20's internals; the fixture's plus_one
# fails the day they change.
CALLABLE = re.compile(r"\bCallable\b")


def returns_callable(funcdef):
    annotation = getattr(funcdef, "annotation", None)
    return annotation is not None and bool(CALLABLE.search(annotation.get_code()))


def skip_callable(annotations):
    def infer_annotations(self):
        if returns_callable(self.tree_node):
            return jedi_function.NO_VALUES
        return annotations(self)
    return infer_annotations


def with_annotation(return_values):
    def get_return_values(self, check_yields=False):
        values = return_values(self, check_yields=check_yields)
        if not check_yields and returns_callable(self.tree_node):
            values |= type(self).annotation_values(self)
        return values
    return get_return_values


for execution in (jedi_function.FunctionExecutionContext, jedi_function.AnonymousFunctionExecution):
    execution.annotation_values = execution.infer_annotations
    execution.infer_annotations = skip_callable(execution.infer_annotations)
jedi_function.BaseFunctionExecutionContext.get_return_values = with_annotation(
    jedi_function.BaseFunctionExecutionContext.get_return_values)


# jedi stops inferring a node past 300 inferences of it (30,000 in the
# builtins module), counting per Script, and that count is the one limit
# its API does not reset at each request. On a Script reused for a file's
# sites a popular node, typeshed's list or dict, passes the cap, and every
# later lookup through it answers nothing (#255). Reset it with the
# others, so each request has the budget a fresh Script would give it.
def reset_counts(reset):
    def reset_recursion_limitations(self):
        reset(self)
        self.inferred_element_counts = {}
    return reset_recursion_limitations


InferenceState.reset_recursion_limitations = reset_counts(InferenceState.reset_recursion_limitations)


# jedi holds the values an expression may have in a ValueSet, a frozenset
# of objects hashed by identity, so it tries them in the order of their
# memory addresses. Where a limit cuts inference short, that order picks
# what is found, and two builds of one tree differed (#267: 8 of
# openpilot's call edges). The ValueSet keeps the order values were added
# in instead; equality and hashing are a frozenset's still.
class Ordered(frozenset):
    __slots__ = ("order",)

    def __new__(cls, iterable=()):
        order = tuple(dict.fromkeys(iterable))
        self = super().__new__(cls, order)
        self.order = order
        return self

    def __iter__(self):
        return iter(self.order)

    def __or__(self, other):
        return Ordered(self.order + tuple(other))

    def __and__(self, other):
        return Ordered(v for v in self.order if v in other)

    def __sub__(self, other):
        return Ordered(v for v in self.order if v not in other)


def ordered_init(self, iterable):
    iterable = list(iterable)
    self._set = Ordered(iterable)
    for value in iterable:
        assert not isinstance(value, ValueSet)


def ordered_from_frozen_set(cls, values):
    self = cls.__new__(cls)
    self._set = values if isinstance(values, Ordered) else Ordered(values)
    return self


def ordered_from_sets(cls, sets):
    values = {}
    for s in sets:
        for v in (s._set if isinstance(s, ValueSet) else s):
            values[v] = None
    return cls._from_frozen_set(Ordered(values))


ValueSet.__init__ = ordered_init
ValueSet._from_frozen_set = classmethod(ordered_from_frozen_set)
ValueSet.from_sets = classmethod(ordered_from_sets)


# parso keeps the trees it parses in memory and, once it holds 600, drops
# every one not used in the last 10 minutes. A tree's last use starts at
# its file's modification time, so a file not edited in 10 minutes is
# dropped at the next parse, even the one a Script is reading: jedi then
# raises KeyError on each of its lookups, and every call in the file is
# lost (#272: 2 of openpilot's files, by how many files their worker had
# parsed before). A worker keeps every tree instead.
parso.cache._CACHED_SIZE_TRIGGER = float("inf")


# parso also keeps each tree it parses as a pickle in jedi's cache
# directory, shared by every process: a worker writes one in place while
# another may be reading it, and the reader's half-written pickle raises
# from inside the import that wanted the module, which then resolves to
# nothing. After an edit to tinygrad's helpers.py, the files importing it
# lost those imports in some builds and not others (#279: 3 of 144
# concurrent reads of two of them). A worker keeps its trees in memory
# only.
parso.cache._load_from_file_system = lambda *args, **kwargs: None
parso.cache._save_to_file_system = lambda *args, **kwargs: None
parso.cache._remove_cache_and_update_lock = lambda *args, **kwargs: None


# What a job's answers depend on besides its own file (#269), so a build
# after an edit can keep the answers of every file none of it changed.
# jedi reaches beyond a file three ways, and each is recorded while the
# job runs:
#   - it parses a file: an import followed, a base class's module, a
#     caller found by the search below;
#   - it looks a module name up on its sys.path, found or not: a file
#     added under that name changes what the lookup finds;
#   - it searches the project's files for a name, to infer a parameter
#     from the calls passing it: a file that comes to contain the name
#     can change the answer. jedi opens at most 2,000 files per search,
#     in the order its walk finds them; a search that reached that cap
#     depends on which files exist at all.
class Record:
    def __init__(self, root):
        self.root, self.files, self.modules, self.names, self.all = root, set(), set(), set(), False


RECORD = None  # the running job's Record


def recording_parse(parse):
    def parse_and_get_code(self, *args, **kwargs):
        parsed = parse(self, *args, **kwargs)
        if RECORD is not None:
            # Only a file that was there: jedi also tries stub paths that
            # are not.
            path = kwargs.get("path")
            if path is None and kwargs.get("file_io") is not None:
                path = kwargs["file_io"].path
            if path is not None:
                RECORD.files.add(os.path.normpath(str(path)))
        return parsed
    return parse_and_get_code


def recording_lookup(get_module_info):
    def lookup(*args, **kwargs):
        if RECORD is not None and kwargs.get("string"):
            RECORD.modules.add(kwargs["string"])
        return get_module_info(*args, **kwargs)
    return lookup


def recording_search(search):
    def search_in_file_ios(inference_state, file_io_iterator, name, limit_reduction=1, complete=False):
        record = RECORD
        if record is None:
            return search(inference_state, file_io_iterator, name, limit_reduction, complete)
        record.names.add(name)
        cap = jedi_references._OPENED_FILE_LIMIT / limit_reduction

        def counted():
            for n, file_io in enumerate(file_io_iterator, 1):
                if n >= cap:
                    record.all = True
                yield file_io
        return search(inference_state, counted(), name, limit_reduction, complete)
    return search_in_file_ios


InferenceState.parse_and_get_code = recording_parse(InferenceState.parse_and_get_code)
jedi_functions.get_module_info = recording_lookup(jedi_functions.get_module_info)
jedi_references.search_in_file_ios = recording_search(jedi_references.search_in_file_ios)


def dep_lines(record, rel):
    """A job's DEP lines: its Record, the file itself left out."""
    out = []
    for path in sorted(record.files):
        dep = path[len(record.root) + 1:] if path.startswith(record.root + os.sep) else None
        if dep is None:
            real = os.path.realpath(path)
            if real.startswith(record.root + os.sep):
                dep = real[len(record.root) + 1:]
        if dep is not None and dep.replace(os.sep, "/") != rel:
            out.append("DEP\t%s\t%s" % (rel, dep.replace(os.sep, "/")))
    out.extend("DEP-MODULE\t%s\t%s" % (rel, m) for m in sorted(record.modules))
    out.extend("DEP-NAME\t%s\t%s" % (rel, n) for n in sorted(record.names))
    if record.all:
        out.append("DEP-ALL\t%s" % rel)
    return out


class Walk(ast.NodeVisitor):
    """The declarations and call sites of one module, each with the
    qualified name of the scope holding it."""

    def __init__(self):
        self.scope, self.decls, self.sites, self.refs, self.bases = [], [], [], [], []
        # The ids of name and attribute nodes that are not values: a
        # callee, the object of an attribute, a decorator.
        self.not_value = set()
        # The ids of nodes that are the object of an attribute: r.b64 in
        # r.b64.decode() is read, and a property's read is a call.
        self.attr_base = set()
        # The names each enclosing scope binds as values, innermost last
        # (see local_values); empty for a class body.
        self.bound = []

    def visit_Module(self, node):
        self.bound.append(local_values(node, node.body))
        self.generic_visit(node)
        self.bound.pop()

    def visit_FunctionDef(self, node):
        self.declare(node, "method" if self.in_class() else "function")

    visit_AsyncFunctionDef = visit_FunctionDef

    def visit_ClassDef(self, node):
        self.declare(node, "class")

    def declare(self, node, kind):
        # A decorator, a default or a base is evaluated in the enclosing
        # scope, so its calls are the enclosing scope's. A decorator named
        # bare, @reclaiming, is itself a call of what it names, made when
        # the definition runs; @wraps(fn) is a call expression already.
        for d in node.decorator_list:
            if isinstance(d, (ast.Name, ast.Attribute)):
                self.sites.append((d.end_lineno, d.end_col_offset - 1, ".".join(self.names())))
                self.not_value.add(id(d))
            self.visit(d)
        if isinstance(node, ast.ClassDef):
            for b in node.bases:
                # Generic[T] is the class Generic; any other expression is
                # a value jedi does not place, and counts as unplaced.
                e = b.value if isinstance(b, ast.Subscript) else b
                place = (e.end_lineno, e.end_col_offset - 1) if isinstance(e, (ast.Name, ast.Attribute)) else (b.lineno, -1)
                self.bases.append(place + (".".join(self.names() + [node.name]),))
                self.not_value.add(id(e))
            for b in node.bases + node.keywords:
                self.visit(b)
        else:
            for d in node.args.defaults + [d for d in node.args.kw_defaults if d]:
                self.visit(d)
        self.decls.append((self.names() + [node.name], kind, node))
        self.scope.append(node)
        self.bound.append(set() if isinstance(node, ast.ClassDef) else local_values(node.args, node.body))
        for stmt in node.body:
            self.visit(stmt)
        self.bound.pop()
        self.scope.pop()

    def in_class(self):
        return bool(self.scope) and isinstance(self.scope[-1], ast.ClassDef)

    def names(self):
        return [s.name for s in self.scope]

    def visit_Assign(self, node):
        self.variables(node.targets, node)
        self.generic_visit(node)

    def visit_AnnAssign(self, node):
        self.variables([node.target], node)
        self.generic_visit(node)

    def variables(self, targets, node):
        # A module's variables and a class body's attributes are
        # declarations; a function's locals are not.
        if self.scope and not self.in_class():
            return
        kind = "field" if self.scope else "variable"
        for t in targets:
            for n in ast.walk(t):
                if isinstance(n, ast.Name):
                    self.decls.append((self.names() + [n.id], kind, n, node))

    def visit_With(self, node):
        enter, leave = ("__aenter__", "__aexit__") if isinstance(node, ast.AsyncWith) else ("__enter__", "__exit__")
        for item in node.items:
            e = item.context_expr
            names = [(n.end_lineno, n.end_col_offset - 1) for n in names_in(e)]
            self.sites.append((e.end_lineno, e.end_col_offset, ".".join(self.names()), names, (enter, leave)))
        self.generic_visit(node)

    visit_AsyncWith = visit_With

    def visit_Name(self, node):
        if isinstance(node, ast.Name) and self.bound and node.id in self.bound[-1]:
            return  # a parameter or a local value: goto names that, not a function
        if isinstance(node.ctx, ast.Load) and id(node) not in self.not_value:
            self.refs.append((node.end_lineno, node.end_col_offset - 1, ".".join(self.names()), False))

    def visit_Attribute(self, node):
        self.not_value.add(id(node.value))
        self.attr_base.add(id(node.value))
        if id(node) in self.attr_base and isinstance(node.ctx, ast.Load):
            # Not a value named, but read: only a property is looked for.
            self.refs.append((node.end_lineno, node.end_col_offset - 1, ".".join(self.names()), True))
        else:
            self.visit_Name(node)
        self.visit(node.value)

    def visit_Call(self, node):
        f = node.func
        self.not_value.add(id(f))
        if isinstance(f, (ast.Name, ast.Attribute)):
            self.sites.append((f.end_lineno, f.end_col_offset - 1, ".".join(self.names())))
        elif not isinstance(f, ast.Lambda):
            # The callee is what the expression evaluates to, asked just
            # past its end. The names it is built from are kept, so one
            # that is a parameter can leave the call unplaced.
            names = [(n.end_lineno, n.end_col_offset - 1) for n in names_in(f)]
            self.sites.append((f.end_lineno, f.end_col_offset, ".".join(self.names()), names))
        self.generic_visit(node)

    def generic_visit(self, node):
        for child in ast.iter_child_nodes(node):
            self.visit(child)


def local_values(args, body):
    """The names a scope binds only as values: its parameters and the
    targets it assigns, not a name it also binds by def, class or import.
    goto on such a name gives the parameter or the assignment, never a
    function, so a reference through it is not looked up. Lambdas and
    nested functions and classes are their own scopes."""
    values, named = set(), set()
    if isinstance(args, ast.arguments):
        values |= {a.arg for a in ast.walk(args) if isinstance(a, ast.arg)} | {"self", "cls"}
    todo = list(body)
    while todo:
        n = todo.pop()
        if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            named.add(n.name)
            continue
        if isinstance(n, ast.Lambda):
            continue
        if isinstance(n, (ast.Import, ast.ImportFrom)):
            named |= {(a.asname or a.name).split(".")[0] for a in n.names}
        elif isinstance(n, (ast.Global, ast.Nonlocal)):
            named |= set(n.names)
        elif isinstance(n, ast.Name) and isinstance(n.ctx, (ast.Store, ast.Del)):
            values.add(n.id)
        todo.extend(ast.iter_child_nodes(n))
    return values - named


def names_in(expr):
    """The names an expression's value depends on: each ast.Name in it
    that is not the object of an attribute (self in self.table[k] is how
    the table is reached, not what is called)."""
    bases = {id(n.value) for n in ast.walk(expr) if isinstance(n, ast.Attribute)}
    return [n for n in ast.walk(expr) if isinstance(n, ast.Name) and id(n) not in bases]


def decl_lines(walk, rel):
    out = []
    for entry in walk.decls:
        path, kind, node = entry[0], entry[1], entry[2]
        end = (entry[3] if len(entry) > 3 else node).end_lineno
        out.append("DECL\t%s\t%s\t%s\t%d\t%d\t%d" % (rel, ".".join(path), kind, node.lineno, node.col_offset + 1, end))
    return out


FROM = re.compile(rb"from\s+(\.*)([\w.]*)")


def import_sites(tree, src_lines):
    """(line, col) of the last character of each imported module's name:
    jedi's goto there is the module. Columns count UTF-8 bytes, as ast's
    do."""
    out = []
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for a in node.names:
                out.append((a.lineno, a.col_offset + len(a.name.encode("utf-8")) - 1))
        elif isinstance(node, ast.ImportFrom):
            m = FROM.match(src_lines[node.lineno - 1].encode("utf-8"), node.col_offset)
            if m and m.group(2):
                out.append((node.lineno, m.end(2) - 1))
            else:  # from . import x: each name is a module, or a name in the package
                for a in node.names:
                    if a.name != "*":
                        out.append((a.lineno, a.col_offset + len(a.name.encode("utf-8")) - 1))
    return out


def inside(d, root):
    p = d.module_path
    return p is not None and os.path.realpath(str(p)).startswith(root + os.sep)


def rel_of(d, root):
    return os.path.relpath(os.path.realpath(str(d.module_path)), root)


# The functions each dataclass field annotated Callable is given, by the
# field's (file, line, column); see field_functions.
FIELDS = {}


def field_functions(d, root):
    """The functions inside the root a field of a dataclass annotated
    Callable is given where the class is built (#258): by keyword,
    Handler(run=f), or by position, Handler("x", f). Its annotation names
    no function, and the values are passed elsewhere in the program, so
    jedi's inference of the field gives none. Only a dataclass: its
    __init__ takes the fields in order, and one with bases is followed by
    keyword only, since an inherited field comes first."""
    key = (str(d.module_path), d.line, d.column)
    if key in FIELDS:
        return FIELDS[key]
    FIELDS[key] = found = []
    try:
        stmt = d._name.tree_name.get_definition()
        cls = stmt.parent.parent.parent
    except Exception:
        return found
    if (cls is None or cls.type != "classdef" or stmt.type != "expr_stmt"
            or stmt.children[1].type != "annassign"
            or not CALLABLE.search(stmt.children[1].children[1].get_code())
            or not any(re.search(r"\bdataclass\b", dec.get_code()) for dec in cls.get_decorators())):
        return found
    scripts = {}

    def values(path, node):
        if path not in scripts:
            scripts[path] = jedi.Script(path=path, project=PROJECT, environment=ENV)
        try:
            return scripts[path].infer(node.end_pos[0], node.end_pos[1] - 1)
        except Exception:
            return []

    given = []
    try:
        for r in scripts.setdefault(str(d.module_path), jedi.Script(path=str(d.module_path), project=PROJECT, environment=ENV)).get_references(d.line, d.column, scope="project"):
            name = r._name.tree_name
            arg = name.parent if name is not None else None
            if arg is not None and arg.type == "argument" and arg.children[0] is name and arg.children[1] == "=":
                given.append((str(r.module_path), arg.children[2]))
        fields = [st.children[0].children[0].value for st in cls.children[-1].children
                  if st.type == "simple_stmt" and st.children[0].type == "expr_stmt"
                  and st.children[0].children[1].type == "annassign" and st.children[0].children[0].type == "name"]
        if cls.get_super_arglist() is None and d.name in fields:
            at = fields.index(d.name)
            for r in scripts[str(d.module_path)].get_references(cls.name.start_pos[0], cls.name.start_pos[1], scope="project"):
                name = r._name.tree_name
                call = name.get_next_sibling() if name is not None else None
                if call is None or call.type != "trailer" or call.children[0] != "(" or call.children[1] == ")":
                    continue
                args = call.children[1]
                args = [a for a in (args.children if args.type == "arglist" else [args]) if a != ","]
                positional = []
                for a in args:
                    if a.type == "argument" and a.children[0] in ("*", "**"):
                        break  # past *xs, a position is not known
                    if not (a.type == "argument" and len(a.children) == 3 and a.children[1] == "="):
                        positional.append(a)
                if at < len(positional):
                    given.append((str(r.module_path), positional[at]))
    except Exception:
        return found
    seen = set()
    for path, node in given:
        for v in values(path, node):
            if inside(v, root) and v.type == "function" and (v.module_path, v.line) not in seen:
                seen.add((v.module_path, v.line))
                found.append(v)
    return found


def decorated(d):
    name = d._name.tree_name if d.type == "function" else None
    return name is not None and name.parent.parent is not None and name.parent.parent.type == "decorated"


def runs(v):
    """The function a decorated function's value calls first: jedi names
    a functools.wraps wrapper after the function it wraps, so look under
    its wrappers for the function itself."""
    value = getattr(v._name, "_value", None)
    while hasattr(value, "_wrapped_value"):
        value = value._wrapped_value
    node = getattr(value, "tree_node", None)
    if node is None or node.type != "funcdef":
        return v
    return Found(value.get_root_context().py__file__(), node.name.start_pos[0], node.name.value)


def resolve_call(script, line, col, root):
    """The functions and classes inside the root a callee names, or why
    there are none: "outside", "unplaced" or "none"."""
    try:
        defs = script.goto(line, col, follow_imports=True)
    except Exception:
        return [], "none"
    if not defs:
        return [], "none"
    found = [d for d in defs if inside(d, root) and d.type in ("function", "class", "property")]
    if not found and any(inside(d, root) and d.type == "statement" for d in defs):
        # A variable holding functions: follow it to them. Not a
        # parameter, whose inference guesses from call sites.
        try:
            found = [d for d in script.infer(line, col) if inside(d, root) and d.type in ("function", "class")]
        except Exception:
            found = []
        if not found:
            found = [f for d in defs if inside(d, root) and d.type == "statement" for f in field_functions(d, root)]
    if any(decorated(d) for d in found):
        # A decorated function runs the decorator's wrapper first, then
        # itself through it, as a parameter: both are targets (#261).
        try:
            seen = {(str(d.module_path), d.line) for d in found}
            for v in script.infer(line, col):
                if v.type == "function" and inside(v, root):
                    v = runs(v)
                    if (str(v.module_path), v.line) not in seen and inside(v, root):
                        seen.add((str(v.module_path), v.line))
                        found.append(v)
        except Exception:
            pass
    if found:
        return found, ""
    if any(inside(d, root) for d in defs):
        return [], "unplaced"
    return [], "outside"


def resolve_value(script, line, col, names, root):
    """resolve_call for a callee that is an expression: the functions and
    classes inside the root its value is. Not guessed when it is built
    from a parameter, whose inference comes from call sites."""
    for nl, nc in names:
        try:
            if any(d.type == "param" for d in script.goto(nl, nc)):
                return [], "unplaced"
        except Exception:
            return [], "none"
    try:
        defs = script.infer(line, col)
    except Exception:
        return [], "none"
    if not defs:
        return [], "none"
    found = [d for d in defs if inside(d, root) and d.type in ("function", "class")]
    if found:
        return found, ""
    if any(inside(d, root) for d in defs):
        return [], "unplaced"
    return [], "outside"


def char_col(lines, line, col):
    """ast's column, a count of UTF-8 bytes, as jedi's, a count of
    characters: they differ after any non-ASCII text on the line."""
    return len(lines[line - 1].encode("utf-8")[:col].decode("utf-8", "ignore"))


def resolve_with(script, line, col, names, methods, root):
    """The methods inside the root a with item's value enters and exits
    by, its own or inherited, or why there are none. A value built from a
    parameter is not guessed."""
    for nl, nc in names:
        try:
            if any(d.type == "param" for d in script.goto(nl, nc)):
                return [], "unplaced"
        except Exception:
            return [], "none"
    try:
        values = [v for d in script.infer(line, col) for v in d._name.infer()]
    except Exception:
        return [], "none"
    if not values:
        return [], "none"
    found = []
    for v in values:
        for name in methods:
            try:
                ms = v.py__getattribute__(name)
            except Exception:
                continue
            for m in ms:
                f, tn = m.get_root_context().py__file__(), m.tree_node
                if f is not None and tn is not None and tn.type == "funcdef" and os.path.realpath(str(f)).startswith(root + os.sep):
                    found.append(Found(os.path.realpath(str(f)), tn.name.start_pos[0], name))
    if found:
        return found, ""
    return [], "outside"


def kind_of(d):
    """A definition's type as a CALL line says it: a property is its
    function."""
    return "function" if d.type == "property" else d.type


class Found:
    """A definition resolve_with found, shaped as jedi's Name is where the
    resolver reads one."""

    type = "function"

    def __init__(self, path, line, name):
        self.module_path, self.line, self.name = path, line, name


def resolve_ref(script, line, col, root):
    """The functions and properties inside the root a name used as a value
    is: a function is referenced, a property read, which runs it."""
    try:
        defs = script.goto(line, col, follow_imports=True)
    except Exception:
        return []
    return [d for d in defs if inside(d, root) and d.type in ("function", "property")]


def resolve_base(script, line, col, root):
    """The classes inside the root a base names, or why there are none,
    as resolve_call: a base from a parameter is not guessed."""
    if col < 0:
        return [], "unplaced"
    found, why = resolve_call(script, line, col, root)
    classes = [d for d in found if d.type == "class"]
    if found and not classes:
        return [], "unplaced"
    return classes, why


def resolve_site(script, site, root, lines):
    line, col = site[0], char_col(lines, site[0], site[1])
    if len(site) > 4:
        names = [(l, char_col(lines, l, c)) for l, c in site[3]]
        return resolve_with(script, line, col, names, site[4], root)
    if len(site) > 3:
        names = [(l, char_col(lines, l, c)) for l, c in site[3]]
        return resolve_value(script, line, col, names, root)
    return resolve_call(script, line, col, root)


def job(root, rel, shard, shards, project, env, out):
    path = os.path.join(root, rel)
    try:
        with open(path, encoding="utf-8") as fh:
            src = fh.read()
        tree = ast.parse(src, rel)
    except (OSError, SyntaxError, ValueError) as e:
        if shard == 0:
            out.append("SKIP\t%s\t%s" % (rel, type(e).__name__))
        return
    global RECORD
    RECORD = Record(root)
    try:
        resolve_job(root, rel, shard, shards, project, env, out, path, src, tree)
    finally:
        record, RECORD = RECORD, None
    out.extend(dep_lines(record, rel))


def resolve_job(root, rel, shard, shards, project, env, out, path, src, tree):
    walk = Walk()
    walk.visit(tree)
    script = jedi.Script(code=src, path=path, project=project, environment=env)
    lines = src.splitlines()
    if shard == 0:
        out.extend(decl_lines(walk, rel))
        for line, col in import_sites(tree, lines):
            try:
                defs = script.goto(line, char_col(lines, line, col), follow_imports=True)
            except Exception:
                defs = []
            here = [d for d in defs if inside(d, root)]
            if here:
                out.append("IMPORT\t%s\t%d\t%s" % (rel, line, rel_of(here[0], root)))
            elif defs:
                out.append("IMPORT-OUTSIDE\t%s\t%d" % (rel, line))
            else:
                out.append("IMPORT-NONE\t%s\t%d" % (rel, line))
        for line, col, cls in walk.bases:
            c = char_col(lines, line, col) if col >= 0 else col
            found, why = resolve_base(script, line, c, root)
            where = "%s\t%d\t%d" % (rel, line, col + 1)
            for d in found:
                out.append("BASE\t%s\t%s\t%s\t%d\t%s" % (where, cls, rel_of(d, root), d.line or 0, d.name))
            if not found:
                out.append("BASE-%s\t%s" % (why.upper(), where))
    for i, site in enumerate(walk.sites):
        if i % shards != shard:
            continue
        line, col, caller = site[:3]
        where = "%s\t%d\t%d" % (rel, line, col + 1)
        found, why = resolve_site(script, site, root, lines)
        for d in found:
            out.append("CALL\t%s\t%s\t%s\t%d\t%s\t%s" % (where, caller, rel_of(d, root), d.line or 0, d.name, kind_of(d)))
        if not found:
            out.append("%s\t%s" % (why.upper(), where))
    for i, (line, col, holder, property_only) in enumerate(walk.refs):
        if i % shards != shard:
            continue
        for d in resolve_ref(script, line, char_col(lines, line, col), root):
            fact = (rel, line, col + 1, holder, rel_of(d, root), d.line or 0, d.name)
            if d.type == "property":
                out.append("CALL\t%s\t%d\t%d\t%s\t%s\t%d\t%s\tfunction" % fact)
            elif not property_only:
                out.append("REF\t%s\t%d\t%d\t%s\t%s\t%d\t%s\tfunction" % fact)


# The warm-up pass (#269): every call site resolved by name alone, with
# ast and no jedi, in seconds where jedi takes minutes, for the graph to
# answer from while jedi runs. A callee is placed when it is a function or
# class defined in the file or an enclosing function, a name imported from
# a module the root holds (followed through re-exports), an attribute of
# such a module or of a class it names, or self.m() and cls.m() through
# the enclosing class and its bases. A module name is the file whose
# dotted path ends with it, the one nearest the importer when several do.
# Anything else is unresolved until jedi answers. Measured on openpilot
# (#269): 69% of the sites jedi places, at most 0.2% of them elsewhere.
def dotted(rel):
    d = rel[:-3].replace("/", ".")
    return d[:-9] if d.endswith(".__init__") else d


class Names:
    """The warm-up pass's table of the root's modules. A module is parsed
    when it is first asked about, so answering a few files reads only the
    modules they reach (#269: after an edit, only the files rerun need
    answers)."""

    def __init__(self, root, rels):
        self.root, self.trees, self.defs, self.classes, self.imports = root, {}, {}, {}, {}
        self.by_dotted = {}
        for rel in rels:
            parts = dotted(rel).split(".")
            for i in range(len(parts)):
                self.by_dotted.setdefault(".".join(parts[i:]), []).append(rel)

    def load(self, rel):
        """Parses a module once; False when it does not parse."""
        if rel in self.trees:
            return self.trees[rel] is not None
        try:
            with open(os.path.join(self.root, rel), encoding="utf-8") as fh:
                tree = ast.parse(fh.read(), rel)
        except (OSError, SyntaxError, ValueError):
            tree = None
        self.trees[rel] = tree
        if tree is not None:
            defs, classes, imports = {}, {}, {}
            for n in tree.body:
                if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)):
                    defs[n.name] = (n.lineno, "function")
                elif isinstance(n, ast.ClassDef):
                    defs[n.name] = (n.lineno, "class")
                    classes[n.name] = (n, {m.name: m.lineno for m in n.body if isinstance(m, (ast.FunctionDef, ast.AsyncFunctionDef))})
            for n in ast.walk(tree):
                if isinstance(n, ast.Import):
                    for a in n.names:
                        imports[a.asname or a.name.split(".")[0]] = ("mod", a.name if a.asname else a.name.split(".")[0])
                elif isinstance(n, ast.ImportFrom):
                    for a in n.names:
                        if a.name != "*":
                            imports[a.asname or a.name] = ("from", n.level, n.module, a.name)
            self.defs[rel], self.classes[rel], self.imports[rel] = defs, classes, imports
        return tree is not None

    def module(self, name, importer):
        found = [r for r in self.by_dotted.get(name, []) if self.load(r)]
        if len(found) == 1:
            return found[0]
        here = os.path.dirname(importer)
        best = [r for r in found if dotted(r) == name] or [r for r in found if os.path.dirname(r) == here or r.startswith(here + "/")]
        return best[0] if len(best) == 1 else None

    def relative(self, importer, level, name):
        pkg = os.path.dirname(importer).split("/")
        if level > 1:
            pkg = pkg[:len(pkg) - (level - 1)]
        base = ".".join(p for p in pkg if p)
        full = ".".join(p for p in (base, name) if p)
        return next((r for r in self.by_dotted.get(full, []) if dotted(r) == full and self.load(r)), None)

    def target(self, importer, level, name):
        return self.relative(importer, level, name) if level else self.module(name, importer) if name else None

    def symbol(self, rel, name, depth=0):
        """(file, line, name, type) of a module's top-level name, through
        its imports, or ("module", file) for a submodule."""
        if depth > 4 or not self.load(rel):
            return None
        if name in self.defs[rel]:
            line, kind = self.defs[rel][name]
            return (rel, line, name, kind)
        imp = self.imports[rel].get(name)
        return self.imported(rel, imp, depth + 1) if imp else None

    def imported(self, rel, imp, depth=0):
        if imp[0] == "mod":
            f = self.module(imp[1], rel)
            return ("module", f) if f else None
        _, level, module, name = imp
        m = self.target(rel, level, module)
        if m:
            s = self.symbol(m, name, depth)
            if s:
                return s
        sub = self.target(rel, level, ".".join(p for p in (module, name) if p))
        return ("module", sub) if sub else None

    def method(self, rel, cls, name, depth=0):
        if depth > 5 or not self.load(rel) or cls not in self.classes[rel]:
            return None
        node, methods = self.classes[rel][cls]
        if name in methods:
            return (rel, methods[name], name, "function")
        for b in node.bases:
            s = None
            if isinstance(b, ast.Name):
                s = self.symbol(rel, b.id)
            elif isinstance(b, ast.Attribute) and isinstance(b.value, ast.Name):
                m = self.symbol(rel, b.value.id)
                s = self.symbol(m[1], b.attr) if m and m[0] == "module" else None
            if s and s[0] != "module" and s[3] == "class":
                found = self.method(s[0], s[2], name, depth + 1)
                if found:
                    return found
        return None

    def callee(self, rel, f, cls, local):
        """The definition a callee, a name or an attribute, names."""
        if isinstance(f, ast.Name):
            if f.id in local:
                line, kind = local[f.id]
                return (rel, line, f.id, kind)
            s = self.symbol(rel, f.id)
            return s if s and s[0] != "module" else None
        v = f.value
        if isinstance(v, ast.Name) and v.id in ("self", "cls") and cls:
            return self.method(rel, cls, f.attr)
        if isinstance(v, ast.Name) and v.id in self.imports[rel]:
            s = self.imported(rel, self.imports[rel][v.id])
            if s and s[0] == "module":
                t = self.symbol(s[1], f.attr)
                return t if t and t[0] != "module" else None
            if s and s[3] == "class":
                return self.method(s[0], s[2], f.attr)
            return None
        if isinstance(v, ast.Name) and self.defs[rel].get(v.id, (0, ""))[1] == "class":
            return self.method(rel, v.id, f.attr)
        if isinstance(v, ast.Attribute) and isinstance(v.value, ast.Name) and v.value.id in self.imports[rel]:
            s = self.imported(rel, self.imports[rel][v.value.id])
            sub = self.module(dotted(s[1]) + "." + v.attr, rel) if s and s[0] == "module" else None
            t = self.symbol(sub, f.attr) if sub else None
            return t if t and t[0] != "module" else None
        return None

    def calls(self, rel):
        """Each callee's definition, by the callee's (end line, end column)."""
        tree, found = self.trees[rel], {}
        decorators = {id(d) for n in ast.walk(tree) if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef))
                      for d in n.decorator_list if isinstance(d, (ast.Name, ast.Attribute))}

        def visit(node, cls, local):
            for child in ast.iter_child_nodes(node):
                if isinstance(child, ast.ClassDef):
                    visit(child, child.name, local)
                    continue
                if isinstance(child, (ast.FunctionDef, ast.AsyncFunctionDef)):
                    inner = dict(local)
                    for n in ast.walk(child):
                        if n is not child and isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
                            inner[n.name] = (n.lineno, "class" if isinstance(n, ast.ClassDef) else "function")
                    visit(child, cls, inner)
                    continue
                f = child if id(child) in decorators else child.func if isinstance(child, ast.Call) else None
                if isinstance(f, (ast.Name, ast.Attribute)):
                    t = self.callee(rel, f, cls, local)
                    if t:
                        found[(f.end_lineno, f.end_col_offset)] = t
                visit(child, cls, local)
        visit(tree, None, dict(self.defs[rel]))
        return found

    def lines(self, root, rel, out):
        """A file's resolver lines, as job() writes them, from names alone."""
        if not self.load(rel):
            out.append("SKIP\t%s\tSyntaxError" % rel)
            return
        tree = self.trees[rel]
        walk = Walk()
        walk.visit(tree)
        out.extend(decl_lines(walk, rel))
        for n in ast.walk(tree):
            targets = []
            if isinstance(n, ast.Import):
                targets = [self.module(a.name, rel) for a in n.names]
            elif isinstance(n, ast.ImportFrom):
                m = self.target(rel, n.level, n.module)
                targets = [m] if m else [self.target(rel, n.level, ".".join(p for p in (n.module, a.name) if p)) for a in n.names]
            for t in targets:
                out.append("IMPORT\t%s\t%d\t%s" % (rel, n.lineno, t) if t else "IMPORT-NONE\t%s\t%d" % (rel, n.lineno))
        found = self.calls(rel)
        for site in walk.sites:
            where = "%s\t%d\t%d" % (rel, site[0], site[1] + 1)
            t = found.get((site[0], site[1] + 1)) if len(site) == 3 else None
            if t:
                out.append("CALL\t%s\t%s\t%s\t%d\t%s\t%s" % (where, site[2], t[0], t[1], t[2], t[3]))
            else:
                out.append("NONE\t%s" % where)


def write(text):
    """All of text to standard output: one os.write may take part of it."""
    data = text.encode("utf-8")
    while data:
        data = data[os.write(1, data):]


def main(inp):
    lines = inp.split("\n")
    stream, names = lines[0] == "stream", lines[0] == "names"
    if stream or names:
        lines = lines[1:]
    root = os.path.realpath(lines[0])
    if names:
        # The files to answer, then optionally a line "TABLE" and the
        # rest of the tree's files, which the table knows.
        rest = lines[1:]
        others = []
        if "TABLE" in rest:
            i = rest.index("TABLE")
            rest, others = rest[:i], rest[i + 1:]
        rels = [l.split("\t")[0] for l in rest if l]
        table, out = Names(root, rels + [l for l in others if l]), []
        for rel in rels:
            table.lines(root, rel, out)
            out.append("DONE\t%s\t0" % rel)
        return "\n".join(out)
    project = jedi.Project(root)
    # jedi's default environment runs a helper process from sys.executable,
    # which in an embedded interpreter is the host binary: it never answers
    # and jedi waits forever. Inference runs in this interpreter instead.
    env = jedi.InterpreterEnvironment()
    global PROJECT, ENV
    PROJECT, ENV = project, env
    out = []
    for l in lines[1:]:
        if l:
            rel, shard, shards = l.split("\t")
            job(root, rel, int(shard), int(shards), project, env, out)
            out.append("DONE\t%s\t%s" % (rel, shard))
            if stream:
                write("".join(o + "\n" for o in out))
                out = []
    return "\n".join(out)


OUT = main(IN)
