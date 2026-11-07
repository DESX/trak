from __future__ import annotations

import re

import pytest
from conftest import BUILD

from trak import Annotation, SchemaError
from trak.schema import Leaf, List, Map, Object, build_schema, parse_definition


def fields(text: str, strict: bool = True) -> list[tuple[str, str, bool, bool]]:
    s = build_schema("t", 1, "1.0", text, strict=strict)
    assert s.error is None, s.error
    return [(f.path, f.type, f.block, f.optional) for f in s.fields]


def test_spec_example_schema() -> None:
    s = build_schema("build", 1, "1.0", BUILD, strict=True)
    assert s.description == "One CI build run."
    assert [f.path for f in s.fields] == [
        "user",
        "status",
        "duration",
        "tags.[]",
        "stdout",
        "error",
    ]
    assert s.field("user").description == "who triggered the build"
    assert s.field("status").enum == ("ok", "failed")
    assert s.field("duration").annotations == (Annotation("ms"),)
    assert s.field("stdout").block
    assert s.field("error").optional
    assert s.field("tags.[]").description is None
    with pytest.raises(KeyError):
        s.field("nope")


def test_binding_d1() -> None:
    root, _ = parse_definition("{ a: [] str?, b: [] str @x, c: |str @log, d: {} num? @u }")
    a, b, c, d = (m.type for m in root.members)
    assert isinstance(a, List) and a.optional and not a.elem.optional
    assert isinstance(b, List) and b.annotations == [Annotation("x")]
    assert isinstance(c, Leaf) and c.block and c.annotations == [Annotation("log")]
    assert isinstance(d, Map) and d.optional and d.annotations == [Annotation("u")]


def test_empty_object_and_map_prefix_d6() -> None:
    root, _ = parse_definition("{ a: {}, b: {} str, c: { }?, d: { // x\n } [] num, e: {} }")
    a, b, c, d, e = (m.type for m in root.members)
    assert isinstance(a, Object) and a.members == []
    assert isinstance(b, Map) and isinstance(b.value, Leaf)
    assert isinstance(c, Object) and c.optional
    assert isinstance(d, Map) and isinstance(d.value, List)
    assert isinstance(e, Object)


def test_nested_paths() -> None:
    text = "{ jobs: [] { name: str, env: {} str, log: |str? }, meta: { a: num, b: enum(x,) } }"
    assert fields(text) == [
        ("jobs.[].name", "str", False, False),
        ("jobs.[].env.{}", "str", False, False),
        ("jobs.[].log", "str", True, True),
        ("meta.a", "num", False, False),
        ("meta.b", "enum", False, False),
    ]


def test_optional_inherited() -> None:
    assert fields("{ a: { b: str }? }") == [("a.b", "str", False, True)]


def test_descriptions() -> None:
    text = """// Line one.
// Line two.

{
  a: str, b: str,   // belongs to b
  // a standalone comment is ignored
  c: {              // belongs to c
    d: str,         // belongs to d
  },
}
// trailing comment
"""
    s = build_schema("t", 1, "1.0", text, strict=True)
    assert s.description == "Line one.\nLine two."
    root = s.root
    assert root is not None
    assert [m.description for m in root.members] == [None, "belongs to b", "belongs to c"]
    assert s.field("c.d").description == "belongs to d"


def test_annotations() -> None:
    root, _ = parse_definition("{ a: num @unit(m/s) @x.y-z } @table(wide)")
    assert root.members[0].type.annotations == [Annotation("unit", "m/s"), Annotation("x.y-z")]
    assert root.annotations == [Annotation("table", "wide")]
    assert str(Annotation("unit", "m/s")) == "@unit(m/s)"
    assert str(Annotation("x")) == "@x"


def test_unknown_leaf_lenient() -> None:
    s = build_schema("t", 1, "1.1", "{ a: si.meter, b: |vec(3)?, c: [] dur }")
    assert s.error is None
    assert [(f.path, f.type, f.unknown, f.block) for f in s.fields] == [
        ("a", "str", "si.meter", False),
        ("b", "str", "vec(3)", True),
        ("c.[]", "str", "dur", False),
    ]
    with pytest.raises(SchemaError, match="unknown type 'si.meter'"):
        build_schema("t", 1, "1.0", "{ a: si.meter }", strict=True)


def test_unparseable_kept_when_lenient() -> None:
    s = build_schema("t", 1, "1.0", "{ a: }")
    assert s.root is None and s.fields == () and s.error is not None
    assert s.annotations == ()
    s = build_schema("t", 1, "2.0", "{ a: str }")
    assert s.root is None and "version" in str(s.error)


@pytest.mark.parametrize(
    ("text", "message", "line", "column"),
    [
        ("", "expected '{'", 1, 1),
        ("x { }", "expected '{'", 1, 1),
        ("{ a: str", "expected ',' or '}'", 1, 9),
        ("{ a str }", "expected ':'", 1, 5),
        ("{ a: str, a: num }", "duplicate key 'a'", 1, 11),
        ("{ 1a: str }", "expected a key", 1, 3),
        ("{\n  a: |[] str }", "'|' applies only to a leaf", 2, 6),
        ("{ a: ||str }", "'|' applies only to a leaf", 1, 6),
        ("{ a: str?? }", "repeated '?'", 1, 10),
        ("{ a: enum() }", "at least one word", 1, 6),
        ("{ a: enum(x, x) }", "duplicate enum word", 1, 14),
        ("{ a: enum(x y) }", "expected ',' or ')'", 1, 13),
        ("{ a: enum(1) }", "expected an enum word", 1, 11),
        ("{ a: enum }", "expected '(' after enum", 1, 10),
        ("{ a: str(x) }", "str takes no parameters", 1, 9),
        ("{ a: num @ }", "expected an annotation name", 1, 11),
        ("{ a: num @x(oops }", "unclosed '('", 1, 12),
        ("{ a: num @x(a\n) }", "unclosed '('", 1, 12),
        ("{ a: = }", "expected a type", 1, 6),
        ("{ a: [ str }", "expected ']'", 1, 8),
        ("{ a: str }?", "can't be optional", 1, 11),
        ("{ a: str } x", "unexpected text", 1, 12),
        ("{ a: str } // é", "schemas are ASCII", 1, 15),
        ("{ a: str,\r\n}", "expected a key", 1, 10),
    ],
)
def test_errors(text: str, message: str, line: int, column: int) -> None:
    with pytest.raises(SchemaError, match=re.escape(message)) as e:
        parse_definition(text)
    assert (e.value.line, e.value.column) == (line, column)


def test_top_level_annotation_only() -> None:
    root, desc = parse_definition("{}")
    assert root.members == [] and desc is None
