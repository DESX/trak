"""Run the shared conformance vectors in ../conformance."""

from __future__ import annotations

import json
import pathlib
from typing import Any

import pytest
from conftest import ROOT

import trak

VECTORS = ROOT / "conformance"


def names(sub: str) -> list[str]:
    return sorted(p.name[: -len(".trak")] for p in (VECTORS / sub).glob("*.trak"))


def plain(v: Any) -> Any:
    if isinstance(v, dict):
        return {k: plain(x) for k, x in v.items()}
    if isinstance(v, (list, tuple)):
        return [plain(x) for x in v]
    return v


def annots(items: tuple[trak.Annotation, ...]) -> list[dict[str, Any]]:
    return [{"name": a.name, "text": a.text} for a in items]


def decode(path: pathlib.Path) -> dict[str, Any]:
    with trak.open(path) as r:
        types = [
            {
                "name": s.name,
                "index": s.index,
                "version": s.version,
                "description": s.description,
                "parsed": s.root is not None,
                "annotations": annots(s.annotations),
                "fields": [
                    {
                        "path": f.path,
                        "type": f.type,
                        "enum": list(f.enum),
                        "unknown": f.unknown,
                        "block": f.block,
                        "optional": f.optional,
                        "annotations": annots(f.annotations),
                        "description": f.description,
                    }
                    for f in s.fields
                ],
            }
            for s in r.types()
        ]
        events = [
            {
                "type": e.type,
                "n": e.n,
                "timestamp": e.timestamp,
                "value": plain(e.to_native()) if e.schema.root is not None else None,
            }
            for e in r
        ]
        # Navigation must agree with the file-order scan.
        for s in r.types():
            rows = [e for e in events if e["type"] == s.name]
            table = r.table(s.name)
            assert table.count() == len(rows)
            for row in rows:
                assert table.get(row["n"]).timestamp == row["timestamp"]
        return {"types": types, "events": events}


def test_minimum_counts() -> None:
    assert len(names("valid")) >= 25
    assert len(names("invalid")) >= 10
    assert len(names("write")) >= 10


@pytest.mark.parametrize("name", names("valid"))
def test_valid(name: str) -> None:
    path = VECTORS / "valid" / f"{name}.trak"
    expected = json.loads(path.with_suffix(".expect.json").read_text(encoding="utf-8"))
    assert decode(path) == expected
    trak.check(path)


@pytest.mark.parametrize("name", names("invalid"))
def test_invalid(name: str) -> None:
    path = VECTORS / "invalid" / f"{name}.trak"
    reason = path.with_suffix(".reason.txt").read_text(encoding="utf-8")
    with pytest.raises(trak.FormatError) as e:
        trak.check(path)
    for line in reason.splitlines():
        if line.startswith("offset: "):
            assert e.value.offset == int(line[len("offset: ") :])


@pytest.mark.parametrize("name", names("write"))
def test_write(name: str, tmp_path: pathlib.Path) -> None:
    ops = json.loads((VECTORS / "write" / f"{name}.input.json").read_text(encoding="utf-8"))
    out = tmp_path / "out.trak"
    with trak.create(out) as w:
        for op in ops:
            if op["op"] == "define":
                w.define(op["name"], op["schema"])
            else:
                w.write(op["type"], op["value"], op["timestamp"])
    assert out.read_bytes() == (VECTORS / "write" / f"{name}.trak").read_bytes()
