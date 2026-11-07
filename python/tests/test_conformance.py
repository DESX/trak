"""Run the shared conformance vectors in ../conformance."""

from __future__ import annotations

import json
import pathlib

import pytest
from conftest import ROOT
from interop import decode, replay

import trak

VECTORS = ROOT / "conformance"


def names(sub: str) -> list[str]:
    return sorted(p.name[: -len(".trak")] for p in (VECTORS / sub).glob("*.trak"))


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
    replay(out, ops)
    assert out.read_bytes() == (VECTORS / "write" / f"{name}.trak").read_bytes()
