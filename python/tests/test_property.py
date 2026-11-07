"""Random schemas, values and interleavings: write, read back, compare, check."""

from __future__ import annotations

import pathlib
import tempfile
from typing import Any

from hypothesis import HealthCheck, given, settings
from hypothesis import strategies as st

import trak

# A type is a tuple: (kind, optional, block-or-children...).
KEYS = st.sampled_from(["a", "b", "c", "dd", "_e", "f1", "Gg"])
TEXT = st.text(max_size=12) | st.binary(max_size=12).map(lambda b: b.decode("latin-1"))
MAP_KEYS = st.text(min_size=0, max_size=6)


def leaves() -> st.SearchStrategy[tuple[Any, ...]]:
    return st.tuples(
        st.sampled_from(["str", "num", "enum"]), st.booleans(), st.booleans()
    )  # kind, optional, block


def required(t: tuple[Any, ...]) -> tuple[Any, ...]:
    """List elements and map values can't be optional: ``[] T?`` is an optional list (D1)."""
    return (t[0], False, *t[2:])


def types(depth: int = 3) -> st.SearchStrategy[tuple[Any, ...]]:
    if depth == 0:
        return leaves()
    inner = types(depth - 1)
    elem = inner.map(required)
    return st.one_of(
        leaves(),
        st.tuples(st.just("list"), st.booleans(), elem),
        st.tuples(st.just("map"), st.booleans(), elem),
        st.tuples(
            st.just("obj"),
            st.booleans(),
            st.lists(st.tuples(KEYS, inner), max_size=4, unique_by=lambda kt: kt[0]),
        ),
    )


def render(t: tuple[Any, ...]) -> str:
    kind, opt = t[0], t[1]
    q = "?" if opt else ""
    if kind in ("str", "num", "enum"):
        base = "enum(x, y_2, Z)" if kind == "enum" else kind
        return ("|" if t[2] else "") + base + q
    if kind == "list":
        return f"[] {render(t[2])}{q}"
    if kind == "map":
        return f"{{}} {render(t[2])}{q}"
    return "{ " + ", ".join(f"{k}: {render(v)}" for k, v in t[2]) + " }" + q


def value(data: st.DataObject, t: tuple[Any, ...]) -> tuple[Any, Any]:
    """Draw (input value, expected native value)."""
    kind, opt = t[0], t[1]
    if opt and data.draw(st.booleans()):
        return None, None
    if kind == "str":
        s = data.draw(TEXT)
        return s, s
    if kind == "num":
        n = data.draw(st.integers(-(10**20), 10**20) | st.sampled_from(["1.5", "-0", "2e-3"]))
        return n, str(n)
    if kind == "enum":
        w = data.draw(st.sampled_from(["x", "y_2", "Z"]))
        return w, w
    if kind == "list":
        items = [value(data, t[2]) for _ in range(data.draw(st.integers(0, 3)))]
        return [i for i, _ in items], [e for _, e in items]
    if kind == "map":
        keys = data.draw(st.lists(MAP_KEYS, max_size=3, unique=True))
        pairs = [(k, value(data, t[2])) for k in keys]
        return (
            {k: v for k, (v, _) in pairs},
            [(k if has_leaf(e) else None, e) for k, (_, e) in pairs],
        )
    members = [(k, value(data, sub)) for k, sub in t[2]]
    return {k: v for k, (v, _) in members}, {k: e for k, (_, e) in members}


def has_leaf(native: Any) -> bool:
    if native is None:
        return False
    if isinstance(native, str):
        return True
    if isinstance(native, dict):
        return any(has_leaf(v) for v in native.values())
    return any(has_leaf(v[1] if isinstance(v, tuple) else v) for v in native)


ROOTS = st.lists(st.tuples(KEYS, types()), min_size=0, max_size=5, unique_by=lambda kt: kt[0])


@settings(max_examples=150, deadline=None, suppress_health_check=[HealthCheck.too_slow])
@given(st.lists(ROOTS, min_size=1, max_size=4), st.data())
def test_round_trip(roots: list[list[tuple[str, Any]]], data: st.DataObject) -> None:
    with tempfile.TemporaryDirectory() as d:
        p = pathlib.Path(d) / "p.trak"
        expected: dict[str, list[Any]] = {}
        with trak.create(p) as w:
            names = [f"t{i}" for i in range(len(roots))]
            for name, root in zip(names, roots, strict=True):
                w.define(name, render(("obj", False, root)))
                expected[name] = []
            for _ in range(data.draw(st.integers(0, 12))):
                i = data.draw(st.integers(0, len(roots) - 1))
                v, e = value(data, ("obj", False, roots[i]))
                w.write(names[i], v)
                expected[names[i]].append(e)
        trak.check(p)
        with trak.open(p) as r:
            for name, rows in expected.items():
                t = r.table(name)
                assert t.count() == len(rows)
                assert [ev.to_native() for ev in t] == rows
