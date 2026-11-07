"""Schema definitions: the type tree, flattened fields, and the parser (SPEC §5)."""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from typing import NoReturn

from .errors import SchemaError

__all__ = [
    "Annotation",
    "Field",
    "Leaf",
    "List",
    "Map",
    "Member",
    "Object",
    "Schema",
    "Type",
    "parse_definition",
]

_KEY = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")
_WORD = re.compile(r"[A-Za-z_][A-Za-z0-9_.]*")
_ANNOTATION = re.compile(r"[A-Za-z_][A-Za-z0-9_.-]*")
_TYPE_NAME = re.compile(r"[A-Za-z_][A-Za-z0-9_.]*")
_VERSION = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")
_TYPE_START = frozenset("|{[_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz")


@dataclass(frozen=True)
class Annotation:
    """``@name`` or ``@name(text)``. ``text`` is None when there are no parentheses."""

    name: str
    text: str | None = None

    def __str__(self) -> str:
        return f"@{self.name}" if self.text is None else f"@{self.name}({self.text})"


@dataclass(eq=False, kw_only=True)
class Type:
    """A node of a schema's type tree. Suffixes (``?`` and annotations) apply to the node."""

    optional: bool = False
    annotations: list[Annotation] = field(default_factory=list)


@dataclass(eq=False, kw_only=True)
class Leaf(Type):
    """``str``, ``num`` or ``enum(…)``. Unknown leaves have kind ``str`` and keep their text."""

    kind: str
    words: tuple[str, ...] = ()
    block: bool = False
    unknown: str | None = None


@dataclass(eq=False, kw_only=True)
class Member:
    """One ``key: type`` entry of an object."""

    key: str
    type: Type
    description: str | None = None
    line: int = 0
    pos: int = 0


@dataclass(eq=False, kw_only=True)
class Object(Type):
    members: list[Member]


@dataclass(eq=False, kw_only=True)
class List(Type):
    elem: Type


@dataclass(eq=False, kw_only=True)
class Map(Type):
    value: Type


@dataclass(frozen=True)
class Field:
    """A leaf column of a type.

    ``path`` writes list indexes as ``[]`` and map keys as ``{}``, e.g. ``tags.[]``.
    ``optional`` is true when any type on the way from the root to the leaf is optional.
    ``annotations`` and ``description`` come from the leaf's own field, the nearest
    enclosing object member, with annotations ordered outermost first.
    """

    path: str
    type: str
    enum: tuple[str, ...]
    unknown: str | None
    block: bool
    optional: bool
    annotations: tuple[Annotation, ...]
    description: str | None


@dataclass(eq=False)
class Schema:
    """A type's schema. ``root`` is None when its definition can't be parsed; see ``error``."""

    name: str
    index: int
    version: str
    text: str
    description: str | None
    root: Object | None
    fields: tuple[Field, ...]
    error: SchemaError | None = None

    @property
    def annotations(self) -> tuple[Annotation, ...]:
        return () if self.root is None else tuple(self.root.annotations)

    def field(self, path: str) -> Field:
        for f in self.fields:
            if f.path == path:
                return f
        raise KeyError(path)


def valid_type_name(name: str) -> bool:
    return _TYPE_NAME.fullmatch(name) is not None


def valid_version(version: str) -> bool:
    return _VERSION.fullmatch(version) is not None


def parse_definition(text: str, *, strict: bool = True) -> tuple[Object, str | None]:
    """Parse a definition and return its root object and type description.

    With ``strict``, unknown leaf types are errors (a 1.0 writer's view). Without it,
    they are read as ``str`` (a reader's view, SPEC §9).
    """
    return _Parser(text, strict).parse()


def build_schema(name: str, index: int, version: str, text: str, *, strict: bool = False) -> Schema:
    """Parse ``text`` into a Schema. When not strict, parse failures are kept in ``error``."""
    try:
        m = _VERSION.fullmatch(version)
        if m is None or m.group(1) != "1":
            raise SchemaError(f"unsupported schema version {version}")
        root, description = parse_definition(text, strict=strict)
    except SchemaError as e:
        if strict:
            raise
        return Schema(name, index, version, text, None, None, (), e)
    return Schema(name, index, version, text, description, root, tuple(flatten(root)))


def flatten(root: Object) -> list[Field]:
    out: list[Field] = []
    _flatten_object(root, "", False, out)
    return out


def _flatten_object(obj: Object, prefix: str, optional: bool, out: list[Field]) -> None:
    for m in obj.members:
        path = m.key if not prefix else f"{prefix}.{m.key}"
        _flatten_type(m.type, path, optional, (), m.description, out)


def _flatten_type(
    t: Type,
    path: str,
    optional: bool,
    annotations: tuple[Annotation, ...],
    description: str | None,
    out: list[Field],
) -> None:
    optional = optional or t.optional
    annotations = annotations + tuple(t.annotations)
    if isinstance(t, Leaf):
        out.append(
            Field(path, t.kind, t.words, t.unknown, t.block, optional, annotations, description)
        )
    elif isinstance(t, Object):
        _flatten_object(t, path, optional, out)
    elif isinstance(t, List):
        _flatten_type(t.elem, path + ".[]", optional, annotations, description, out)
    elif isinstance(t, Map):
        _flatten_type(t.value, path + ".{}", optional, annotations, description, out)


class _Parser:
    def __init__(self, text: str, strict: bool) -> None:
        self.s = text
        self.i = 0
        self.strict = strict
        # comment start -> (line, alone on its line, text)
        self.comments: dict[int, tuple[int, bool, str]] = {}
        self.members: list[Member] = []

    def fail(self, message: str, pos: int | None = None) -> NoReturn:
        p = self.i if pos is None else pos
        line = self.s.count("\n", 0, p) + 1
        column = p - (self.s.rfind("\n", 0, p) + 1) + 1
        raise SchemaError(message, line, column)

    def peek(self) -> str:
        return self.s[self.i] if self.i < len(self.s) else ""

    def ws(self) -> None:
        s, n = self.s, len(self.s)
        while self.i < n:
            c = s[self.i]
            if c in " \t\n":
                self.i += 1
            elif s.startswith("//", self.i):
                end = s.find("\n", self.i)
                if end < 0:
                    end = n
                if self.i not in self.comments:
                    line_start = s.rfind("\n", 0, self.i) + 1
                    alone = s[line_start : self.i].strip(" \t") == ""
                    line = s.count("\n", 0, self.i) + 1
                    self.comments[self.i] = (line, alone, s[self.i + 2 : end].strip(" \t"))
                self.i = end
            else:
                return

    def expect(self, c: str) -> None:
        self.ws()
        if self.peek() != c:
            self.fail(f"expected {c!r}")
        self.i += 1

    def parse(self) -> tuple[Object, str | None]:
        for i, c in enumerate(self.s):
            if ord(c) > 127:
                self.fail("schemas are ASCII", i)
        self.ws()
        if self.peek() != "{":
            self.fail("expected '{' after the type's comment lines")
        brace = self.i
        root = self.parse_object()
        while True:
            self.ws()
            c = self.peek()
            if c == "@":
                root.annotations.append(self.parse_annotation())
            elif c == "?":
                self.fail("an event's top-level type can't be optional")
            else:
                break
        if self.i != len(self.s):
            self.fail("unexpected text after the definition")
        lines = [text for pos, (_, _, text) in sorted(self.comments.items()) if pos < brace]
        description = "\n".join(lines) if lines else None
        by_line: dict[int, Member] = {}
        for m in self.members:
            prev = by_line.get(m.line)
            if prev is None or m.pos > prev.pos:
                by_line[m.line] = m
        for pos, (line, alone, text) in sorted(self.comments.items()):
            if pos > brace and not alone and line in by_line:
                by_line[line].description = text
        return root, description

    def parse_object(self) -> Object:
        self.expect("{")
        members: list[Member] = []
        keys: set[str] = set()
        while True:
            self.ws()
            if self.peek() == "}":
                self.i += 1
                return Object(members=members)
            m = _KEY.match(self.s, self.i)
            if m is None:
                self.fail("expected a key")
            key, pos = m.group(), self.i
            if key in keys:
                self.fail(f"duplicate key {key!r}")
            keys.add(key)
            self.i = m.end()
            self.expect(":")
            t = self.parse_type()
            member = Member(key=key, type=t, line=self.s.count("\n", 0, pos) + 1, pos=pos)
            members.append(member)
            self.members.append(member)
            self.ws()
            c = self.peek()
            if c == ",":
                self.i += 1
            elif c != "}":
                self.fail("expected ',' or '}'")

    def parse_type(self) -> Type:
        prefixes: list[tuple[str, int]] = []
        base: Type | None = None
        while True:
            self.ws()
            c, pos = self.peek(), self.i
            if c == "|":
                prefixes.append(("|", pos))
                self.i += 1
            elif c == "[":
                self.i += 1
                self.expect("]")
                prefixes.append(("[]", pos))
            elif c == "{":
                self.i += 1
                self.ws()
                if self.peek() != "}":
                    self.i = pos
                    break
                self.i += 1
                after = self.i
                self.ws()
                if self.peek() in _TYPE_START and self.peek() != "":
                    prefixes.append(("{}", pos))
                else:
                    self.i = after
                    base = Object(members=[])
                    break
            else:
                break
        node = base if base is not None else self.parse_base()
        for p, pos in reversed(prefixes):
            if p == "|":
                if not isinstance(node, Leaf) or node.block:
                    self.fail("'|' applies only to a leaf", pos)
                node.block = True
            elif p == "[]":
                node = List(elem=node)
            else:
                node = Map(value=node)
        while True:
            self.ws()
            c = self.peek()
            if c == "?":
                if node.optional:
                    self.fail("repeated '?'")
                node.optional = True
                self.i += 1
            elif c == "@":
                node.annotations.append(self.parse_annotation())
            else:
                return node

    def parse_annotation(self) -> Annotation:
        self.i += 1
        m = _ANNOTATION.match(self.s, self.i)
        if m is None:
            self.fail("expected an annotation name")
        self.i = m.end()
        return Annotation(m.group(), self.parse_parens() if self.peek() == "(" else None)

    def parse_parens(self) -> str:
        start = self.i + 1
        end = self.s.find(")", start)
        if end < 0 or "\n" in self.s[start:end]:
            self.fail("unclosed '(' (text can't contain ')' or a line break)")
        self.i = end + 1
        return self.s[start:end]

    def parse_base(self) -> Type:
        if self.peek() == "{":
            return self.parse_object()
        start = self.i
        m = _WORD.match(self.s, self.i)
        if m is None:
            self.fail("expected a type")
        word = m.group()
        self.i = m.end()
        if word in ("str", "num"):
            if self.peek() == "(":
                self.fail(f"{word} takes no parameters")
            return Leaf(kind=word)
        if word == "enum":
            if self.peek() != "(":
                self.fail("expected '(' after enum")
            self.i += 1
            words: list[str] = []
            while True:
                self.ws()
                if self.peek() == ")":
                    self.i += 1
                    break
                w = _KEY.match(self.s, self.i)
                if w is None:
                    self.fail("expected an enum word")
                if w.group() in words:
                    self.fail(f"duplicate enum word {w.group()!r}")
                words.append(w.group())
                self.i = w.end()
                self.ws()
                c = self.peek()
                if c == ",":
                    self.i += 1
                elif c != ")":
                    self.fail("expected ',' or ')'")
            if not words:
                self.fail("enum needs at least one word", start)
            return Leaf(kind="enum", words=tuple(words))
        if self.peek() == "(":
            self.parse_parens()
        if self.strict:
            self.fail(f"unknown type {word!r}", start)
        return Leaf(kind="str", unknown=self.s[start : self.i])
