"""``check`` must catch each corruption. Corruptions keep byte lengths, so only the targeted
rule breaks, and they sit before a later valid object, so recovery can't drop them."""

from __future__ import annotations

import pathlib

import pytest
from conftest import BUILD, DEPLOY, EVENTS

import trak


@pytest.fixture
def base(tmp_path: pathlib.Path) -> bytes:
    p = tmp_path / "base.trak"
    with trak.create(p) as w:
        w.define("build", BUILD)
        w.define("deploy", DEPLOY)
        w.define("built", "{ n: num }")
        for value, ts in EVENTS:
            w.write("build", value, ts)
        w.write("deploy", {"target": "prod", "version": "1.2.3"}, "20261107T150000.000Z")
        w.write("built", {"n": 1}, "20261107T150000.000Z")
        w.write("built", {"n": 2}, "20261107T150000.001Z")
    assert trak.check(p) == 8
    return p.read_bytes()


def footer(data: bytes, prefix: bytes) -> bytes:
    i = data.index(prefix)
    return data[i : data.index(b"\n", i)]


def swap_vlq(line: bytes, index: int, value: int) -> bytes:
    """Replace the index-th VLQ of a footer line with a value of the same width."""
    from trak._vlq import decode, encode_all

    marker = b"]==" if line.startswith(b"==[") else b"]##"
    head, letters = line.split(marker)
    vlqs = decode(letters)
    vlqs[index] = value
    new = head + marker + encode_all(vlqs)
    assert len(new) == len(line)
    return new


def run(tmp_path: pathlib.Path, data: bytes) -> trak.FormatError:
    p = tmp_path / "bad.trak"
    p.write_bytes(data)
    with pytest.raises(trak.FormatError) as e:
        trak.check(p)
    return e.value


CASES = {
    "bad num": (b"duration: 910", b"duration: 9x0", "bad num value"),
    "bad enum": (b"status: ok", b"status: no", "bad enum value"),
    "inline path": (b"user: ana", b"usex: ana", "doesn't start with"),
    "block label": (b"--[ stdout ]--\n==[ build 1", b"--[ stdouT ]--\n==[ build 1", "block label"),
    "duplicate name": (b"##[ built 3", b"##[ build 3", "duplicate schema name"),
    "schema index": (b"##[ deploy 2", b"##[ deploy 4", "index 4, expected 2"),
    "event number": (b"==[ build 2 ", b"==[ build 3 ", "event number"),
    "bad date": (b"==[ build 1 20261107T", b"==[ build 1 20261307T", "invalid timestamp"),
    "backwards": (b"==[ build 1 20261107T14", b"==[ build 1 20261107T16", "backwards"),
    "event before schema": (b"##[ built 3", b"##[ bxilt 3", "ignored tail"),
}


@pytest.mark.parametrize("case", CASES)
def test_byte_corruptions(tmp_path: pathlib.Path, base: bytes, case: str) -> None:
    old, new, message = CASES[case]
    assert len(old) == len(new) and base.count(old) == 1
    e = run(tmp_path, base.replace(old, new))
    assert message in str(e) and e.offset is not None


@pytest.mark.parametrize(
    ("prefix", "index", "value", "message"),
    [
        (b"==[ build 2 ", 1, 2, "step pointer"),
        (b"==[ build 2 ", 2, 2, "jump pointer"),
        (b"==[ build 2 ", 3, 140, "dir pointer"),
        (b"==[ build 2 ", 4, 1, "dir pointer"),
        (b"==[ build 2 ", 4, 9, "not defined"),
        (b"==[ build 1 ", 11, 22, "walk"),
        (b"==[ build 1 ", 9, 2, "walk runs past"),
        (b"##[ deploy 2", 1, 2, "pointer"),
        (b"##[ build 1", 1, 2, "pointer"),
    ],
)
def test_footer_corruptions(
    tmp_path: pathlib.Path, base: bytes, prefix: bytes, index: int, value: int, message: str
) -> None:
    line = footer(base, prefix)
    e = run(tmp_path, base.replace(line, swap_vlq(line, index, value)))
    assert message in str(e)


def test_body_length_past_start(tmp_path: pathlib.Path) -> None:
    e = run(tmp_path, b"trak 1.0\n{}\n##[ a 1 1.0 ]##EAA\n")
    assert "past the first object" in str(e)


def test_check_counts_objects(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "x.trak"
    p.write_bytes(b"trak 1.0\n")
    assert trak.check(p) == 0


def test_iterating_event_without_schema(tmp_path: pathlib.Path, base: bytes) -> None:
    p = tmp_path / "x.trak"
    p.write_bytes(base.replace(b"==[ build 2 ", b"==[ bxild 2 "))
    with trak.open(p) as r, pytest.raises(trak.FormatError, match="has no schema"):
        list(r)
