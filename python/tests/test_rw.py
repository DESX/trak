from __future__ import annotations

import dataclasses
import datetime as dt
import os
import pathlib
from decimal import Decimal
from typing import Any

import pytest
from conftest import BUILD, DEPLOY, EVENTS, spec_example

import trak
from trak._time import from_ms, to_ms

UTC = dt.timezone.utc


def write_example(path: pathlib.Path) -> None:
    with trak.create(path) as w:
        w.define("build", BUILD)
        w.define("deploy", DEPLOY)
        for value, ts in EVENTS:
            w.write("build", value, ts)


# --- the spec example ---------------------------------------------------------------


def test_writer_reproduces_spec_example(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "x.trak"
    write_example(p)
    assert p.read_bytes() == spec_example()


def test_schema_text_gets_final_lf(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "x.trak"
    with trak.create(p) as w:
        w.define("build", BUILD.rstrip("\n"))
        w.define("deploy", DEPLOY)
        for value, ts in EVENTS:
            w.write("build", value, ts)
    assert p.read_bytes() == spec_example()


def test_reader_on_spec_example(example: pathlib.Path) -> None:
    with trak.open(example) as r:
        assert r.version == "1.1"
        assert [(s.name, s.index, s.version) for s in r.types()] == [
            ("build", 1, "1.0"),
            ("deploy", 2, "1.0"),
        ]
        assert r.schema("deploy").description == "A release pushed to an environment."
        builds = r.table("build")
        assert builds.name == "build"
        assert builds.count() == len(builds) == 2
        assert r.table("deploy").count() == 0
        assert r.table("deploy").latest() is None
        latest = builds.latest()
        assert latest is not None and latest.n == 2
        e = builds.get(1)
        assert (e.type, e.n, e.timestamp, e.offset) == ("build", 1, "20261107T140210.120Z", 423)
        assert e.time == dt.datetime(2026, 11, 7, 14, 2, 10, 120000, tzinfo=UTC)
        assert e.get("user") == "ana"
        assert e.get("duration") == "910"
        assert e.as_int("duration") == 910
        assert e.as_float("duration") == 910.0
        assert e.get("error") is None and e.as_int("error") is None and e.as_float("x") is None
        assert e.get(b"tags.0") == "ci"
        raw = e.raw("stdout")
        assert raw is not None and bytes(raw) == b"Compiling core...\nDone."
        del raw
        assert e.raw("nope") is None
        assert e.to_native() == {**EVENTS[0][0], "duration": "910", "error": None}
        assert builds[2].to_native()["tags"] == ["ci", "nightly"]
        assert builds.get(2).items() == [
            ("user", "joel"),
            ("status", "failed"),
            ("duration", "1520"),
            ("tags.0", "ci"),
            ("tags.1", "nightly"),
            ("stdout", "Compiling core...\nld: undefined symbol main"),
            ("error", "link failed"),
        ]
        assert "build 2" in repr(builds.get(2))
        assert [ev.n for ev in r] == [1, 2]
        with pytest.raises(KeyError):
            r.table("nope")
        with pytest.raises(IndexError):
            builds.get(3)
        with pytest.raises(IndexError):
            builds.get(0)


def test_check_spec_example(example: pathlib.Path) -> None:
    assert trak.check(example) == 4


# --- navigation -----------------------------------------------------------------


@pytest.fixture
def many(tmp_path: pathlib.Path) -> pathlib.Path:
    p = tmp_path / "many.trak"
    with trak.create(p) as w:
        w.define("a", "{ i: num }")
        w.define("b", "{ s: str }")
        w.define("c", "{ }")
        for i in range(1, 2001):
            w.write("a", {"i": i}, from_ms(1_000_000 + i * 10))
            if i % 3 == 0:
                w.write("b", {"s": f"s{i}"}, from_ms(1_000_000 + i * 10))
    return p


def test_get_every_row(many: pathlib.Path) -> None:
    with trak.open(many) as r:
        a = r.table("a")
        assert a.count() == 2000
        for n in range(1, 2001):
            assert a.get(n).as_int("i") == n
        assert r.table("b").count() == 666
        assert r.table("b").get(666).get("s") == "s1998"
        assert r.table("c").count() == 0
        assert trak.check(many) == 3 + 2000 + 666


def test_range_both_directions(many: pathlib.Path) -> None:
    with trak.open(many) as r:
        a = r.table("a")
        assert [e.n for e in a.range(5, 600)] == list(range(5, 601))
        assert [e.n for e in a.range(600, 590)] == list(range(600, 589, -1))
        assert [e.n for e in a.range(7, 7)] == [7]
        assert [e.n for e in a][-3:] == [1998, 1999, 2000]
        assert list(r.table("c")) == []
        with pytest.raises(IndexError):
            list(a.range(0, 3))
        with pytest.raises(IndexError):
            list(a.range(1, 2001))


def test_at_time(many: pathlib.Path) -> None:
    with trak.open(many) as r:
        a = r.table("a")
        e = a.at_time(from_ms(1_000_000 + 105))
        assert e is not None and e.n == 11
        e = a.at_time(from_ms(1_000_000 + 110))
        assert e is not None and e.n == 11
        e = a.at_time(dt.datetime(1970, 1, 1, tzinfo=UTC))
        assert e is not None and e.n == 1
        assert a.at_time(from_ms(1_000_000 + 20_011)) is None


def test_get_reads_few_footers(many: pathlib.Path) -> None:
    with trak.open(many) as r:
        a = r.table("a")
        a.count()
        for n in (1, 2, 777, 1999, 2000):
            r._cache.clear()
            before = r.footer_reads
            a.get(n)
            assert r.footer_reads - before <= 3 * 11 + 4


# --- values ---------------------------------------------------------------------

NESTED = """{
  jobs: [] {
    name: str,
    env: {} str,
    log: |str?,
  },
  labels: {} [] str,
  extra: { a: num, b: enum(x, y) }?,
  empty: [] {},
}"""


def test_nested_round_trip(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "n.trak"
    value: dict[str, Any] = {
        "jobs": [
            {"name": "a", "env": {"region": "eu", "k.e:y] ": "v\nw"}, "log": "x\n--[ log ]--"},
            {"name": "b", "env": [("ü", "ß")], "log": None},
            {"name": "c", "env": {}},
        ],
        "labels": {"none": [], "some": ["1", "2"]},
        "extra": {"a": Decimal("1.5e3"), "b": b"y"},
        "empty": [{}, {}],
    }
    with trak.create(p) as w:
        w.define("t", NESTED)
        w.write("t", value, "20260101T000000.000Z")
        w.write("t", {"jobs": [], "labels": {}, "empty": []}, "20260101T000000.000Z")
    trak.check(p)
    with trak.open(p) as r:
        e = r.table("t").get(1)
        assert e.to_native() == {
            "jobs": [
                {
                    "name": "a",
                    "env": [("region", "eu"), ("k.e:y] ", "v\nw")],
                    "log": "x\n--[ log ]--",
                },
                {"name": "b", "env": [("ü", "ß")], "log": None},
                {"name": "c", "env": [], "log": None},
            ],
            "labels": [(None, []), ("some", ["1", "2"])],
            "extra": {"a": "1.5E+3", "b": "y"},
            "empty": [{}, {}],
        }
        assert e.get("jobs.0.env.k.e:y] ") == "v\nw"
        assert e.get("jobs.1.env.ü") == "ß"
        assert e.get("labels.some.1") == "2"
        assert r.table("t").get(2).to_native() == {
            "jobs": [],
            "labels": [],
            "extra": None,
            "empty": [],
        }


def test_bytes_and_surrogates(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "b.trak"
    with trak.create(p) as w:
        w.define("t", "{ s: str, m: {} str }")
        w.write("t", {"s": b"\xff\x00raw", "m": {b"\xfe": bytearray(b"v")}})
    with trak.open(p) as r:
        e = r.table("t").get(1)
        s = e.get("s")
        assert s is not None and s.encode("utf-8", "surrogateescape") == b"\xff\x00raw"
        assert e.get(b"m.\xfe") == "v"


@dataclasses.dataclass
class Build:
    user: str
    duration: int
    error: str | None = None


def test_dataclass_values(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "d.trak"
    with trak.create(p) as w:
        w.define("build", "{ user: str, duration: num, error: str? }")
        assert w.write("build", Build("ana", 3)).n == 1
        assert w.write("build", Build("bo", 4, "x")).n == 2
    with trak.open(p) as r:
        assert r.table("build").get(2).to_native() == {"user": "bo", "duration": "4", "error": "x"}


@pytest.mark.parametrize(
    ("value", "out"),
    [(0, "0"), (-12, "-12"), (2**70, str(2**70)), (0.1, "0.1"), (1e16, "1e+16"), (-0.0, "-0.0")],
)
def test_num_formats(tmp_path: pathlib.Path, value: Any, out: str) -> None:
    p = tmp_path / "num.trak"
    with trak.create(p) as w:
        w.define("t", "{ x: num }")
        w.write("t", {"x": value})
    with trak.open(p) as r:
        assert r.table("t").get(1).get("x") == out


@pytest.mark.parametrize(
    ("value", "message"),
    [
        ({"x": "01"}, "not a valid num"),
        ({"x": "1."}, "not a valid num"),
        ({"x": float("nan")}, "finite"),
        ({"x": float("inf")}, "finite"),
        ({"x": True}, "bool"),
        ({"x": [1]}, "decimal string"),
        ({"x": 1, "e": "z"}, "not one of a, b"),
        ({"x": 1, "e": 3}, "enum needs a str"),
        ({"x": 1, "s": 3}, "str needs str or bytes"),
        ({"x": 1, "l": "ab"}, "expected a list"),
        ({"x": 1, "m": 3}, "expected a map"),
        ({"x": 1, "m": [("a",)]}, "pairs"),
        ({"x": 1, "m": {1: "a"}}, "str or bytes"),
        ({"x": 1, "m": [("a", "1"), ("a", "2")]}, "repeated map key"),
        ({"x": 1, "o": 5}, "expected a mapping"),
        ({"x": 1, "zzz": 1}, "unknown field 'zzz'"),
        ({}, "x: value is required"),
        ({"x": 1, "o": {}}, "o.y: value is required"),
    ],
)
def test_value_errors(tmp_path: pathlib.Path, value: Any, message: str) -> None:
    with trak.create(tmp_path / "e.trak") as w:
        w.define("t", "{ x: num, e: enum(a, b)?, s: str?, l: [] str?, m: {} str?, o: {y: str}? }")
        with pytest.raises(trak.ValueError, match=message):
            w.write("t", value)
        assert w.write("t", {"x": 1}).n == 1


# --- timestamps -----------------------------------------------------------------


def test_timestamps(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "t.trak"
    with trak.create(p) as w:
        w.define("t", "{}")
        w.define("u", "{}")
        w.write("t", {}, "20991231T235959.999Z")
        w.write("t", {})  # now is earlier, so the last timestamp is reused
        w.write("u", {}, dt.datetime(2020, 5, 6, 7, 8, 9, 123456, tzinfo=UTC))
        w.write("u", {})
        with pytest.raises(trak.ValueError, match="earlier"):
            w.write("t", {}, "20991231T235959.998Z")
        with pytest.raises(trak.ValueError, match="bad timestamp"):
            w.write("t", {}, "2099-12-31")
        with pytest.raises(trak.ValueError, match="naive"):
            w.write("t", {}, dt.datetime(2099, 12, 31))
        with pytest.raises(trak.ValueError, match="str or datetime"):
            w.write("t", {}, 5)  # type: ignore[arg-type]
        with pytest.raises(trak.ValueError, match="not defined"):
            w.write("nope", {})
    with trak.open(p) as r:
        t = r.table("t")
        assert [e.timestamp for e in t] == ["20991231T235959.999Z"] * 2
        u = r.table("u")
        assert u.get(1).timestamp == "20200506T070809.123Z"
        assert u.get(2).timestamp > "2025"


def test_time_helpers() -> None:
    assert from_ms(0) == "19700101T000000.000Z"
    assert to_ms("19700101T000001.002Z") == 1002
    assert from_ms(to_ms("20261107T140322.415Z")) == "20261107T140322.415Z"


# --- writer behavior --------------------------------------------------------------


def test_define_errors(tmp_path: pathlib.Path) -> None:
    with trak.create(tmp_path / "s.trak") as w:
        w.define("a", "{}")
        assert [s.name for s in w.types()] == ["a"]
        assert w.schema("a").index == 1
        with pytest.raises(trak.SchemaError, match="already defined"):
            w.define("a", "{}")
        with pytest.raises(trak.SchemaError, match="reserved"):
            w.define("trak.x", "{}")
        with pytest.raises(trak.SchemaError, match="bad type name"):
            w.define("1x", "{}")
        with pytest.raises(trak.SchemaError, match="unknown type"):
            w.define("b", "{ x: foo }")


def test_create_refuses_existing(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "x.trak"
    p.write_bytes(b"precious")
    with pytest.raises(FileExistsError):
        trak.create(p)
    assert p.read_bytes() == b"precious"
    with trak.create(p, overwrite=True, sync=True) as w:
        w.define("a", "{}")
        w.write("a", {})
        w.flush()
    assert trak.check(p) == 2


def test_lock(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "x.trak"
    with trak.create(p) as w:
        with pytest.raises(trak.TrakError, match="locked"):
            trak.open_append(p)
        with trak.open(p) as r:  # readers take no lock
            assert r.types() == []
    with trak.open_append(p) as w2:
        assert not w2.closed
    assert w.closed and w2.closed
    with pytest.raises(trak.TrakError, match="closed"):
        w.define("a", "{}")
    w.close()


def test_append_continues_identically(tmp_path: pathlib.Path) -> None:
    whole = tmp_path / "whole.trak"
    split = tmp_path / "split.trak"
    with trak.create(whole) as w:
        w.define("build", BUILD)
        w.define("deploy", DEPLOY)
        w.write("build", EVENTS[0][0], EVENTS[0][1])
        w.write("build", EVENTS[1][0], EVENTS[1][1])
    with trak.create(split) as w:
        w.define("build", BUILD)
        w.define("deploy", DEPLOY)
        w.write("build", EVENTS[0][0], EVENTS[0][1])
    with trak.open_append(split) as w:
        assert [s.name for s in w.types()] == ["build", "deploy"]
        w.write("build", EVENTS[1][0], EVENTS[1][1])
    assert split.read_bytes() == whole.read_bytes()


def test_append_many(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "x.trak"
    with trak.create(p) as w:
        for name in "abcde":
            w.define(name, "{ i: num }")
    for round_ in range(20):
        with trak.open_append(p) as w:
            for i in range(37):
                w.write("abcde"[(i * 7 + round_) % 5], {"i": i})
            if round_ == 10:
                w.define("late", "{}")
                w.write("late", {})
        assert trak.check(p) > 0
    with trak.open(p) as r:
        assert sum(r.table(n).count() for n in "abcde") == 20 * 37
        with pytest.raises(trak.ValueError, match="earlier"), trak.open_append(p) as w:
            w.write("late", {}, "19700101T000000.000Z")


def test_append_to_magic_only_and_empty(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "x.trak"
    trak.create(p).close()
    assert p.read_bytes() == b"trak 1.1\n"
    with trak.open_append(p) as w:
        w.define("a", "{}")
    q = tmp_path / "empty.trak"
    q.write_bytes(b"")
    with trak.open_append(q) as w:
        w.define("a", "{}")
    assert p.read_bytes() == q.read_bytes()


# --- recovery and refresh -----------------------------------------------------------


def test_torn_tail_ignored_and_truncated(example: pathlib.Path) -> None:
    good = example.read_bytes()
    complete_line = b"==[ build 3 20261107T140322.415Z ]==fQBB\n"
    for tail in [b"user: x\nstat", b"==[ build 3 2026", complete_line]:
        example.write_bytes(good + tail)
        with trak.open(example) as r:
            assert r.end == len(good) and r.size == len(good) + len(tail)
            assert r.table("build").count() == 2
        if tail == complete_line:
            with pytest.raises(trak.FormatError, match="ignored tail"):
                trak.check(example)
        else:
            assert trak.check(example) == 4
        with trak.open_append(example):
            pass
        assert example.read_bytes() == good


def test_torn_tail_to_magic(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "x.trak"
    p.write_bytes(b"trak 1.0\n// half a schema\n{")
    with trak.open(p) as r:
        assert r.types() == [] and r.end == 9


def test_refresh(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "x.trak"
    with trak.create(p) as w, trak.open(p) as r:
        assert r.types() == []
        assert not r.refresh()
        w.define("a", "{ i: num }")
        w.write("a", {"i": 1})
        assert r.refresh()
        t = r.table("a")
        assert t.count() == 1
        w.write("a", {"i": 2})
        assert r.refresh()
        assert t.count() == 2 and t.get(2).get("i") == "2"


# --- reader errors ------------------------------------------------------------------


@pytest.mark.parametrize(
    ("data", "error", "message"),
    [
        (b"", trak.FormatError, "empty"),
        (b"trac 1.0\n", trak.FormatError, "magic"),
        (b"trak 1.0", trak.FormatError, "magic"),
        (b"trak 01.0\n", trak.FormatError, "magic"),
        (b"trak 2.0\n", trak.UnsupportedVersion, "major"),
    ],
)
def test_bad_magic(tmp_path: pathlib.Path, data: bytes, error: type, message: str) -> None:
    p = tmp_path / "x.trak"
    p.write_bytes(data)
    with pytest.raises(error, match=message):
        trak.open(p)


def test_newer_minor_reads(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "x.trak"
    body = b"{ a: str, b: vec(3) }\n"
    p.write_bytes(
        b"trak 1.7\n" + body + b"##[ t 1 1.7 ]##" + bytes([ord("A") + len(body)]) + b"AA\n"
    )
    with trak.open(p) as r:
        assert r.version == "1.7"
        assert r.schema("t").fields[1].unknown == "vec(3)"


def test_unparseable_schema_still_navigates(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "x.trak"
    with trak.create(p) as w:
        w.define("t", "{ a: str }")
        w.write("t", {"a": "x"})
    data = p.read_bytes().replace(b"{ a: str }", b"{ a: s?? }")
    p.write_bytes(data)
    with trak.open(p) as r:
        t = r.table("t")
        assert t.count() == 1 and r.schema("t").error is not None
        with pytest.raises(trak.SchemaError, match="unavailable"):
            t.get(1).get("a")
    with pytest.raises(trak.FormatError, match="schema t"):
        trak.check(p)
    with trak.open_append(p) as w, pytest.raises(trak.SchemaError, match="can't write"):
        w.write("t", {"a": "y"})


@pytest.mark.skipif(os.name != "posix", reason="POSIX permissions")
def test_new_files_are_not_executable(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "x.trak"
    trak.create(p).close()
    assert p.stat().st_mode & 0o111 == 0


def test_time_conversions_match_datetime() -> None:
    import random as _random

    epoch = dt.datetime(1970, 1, 1, tzinfo=UTC)
    rng = _random.Random(5)
    lo, hi = -62135596800000, 253402300799999  # years 0001 to 9999
    for ms in [0, -1, -86_400_000, lo, hi] + [rng.randint(lo, hi) for _ in range(20000)]:
        t = epoch + dt.timedelta(milliseconds=ms)
        want = t.strftime("%Y%m%dT%H%M%S.") + f"{t.microsecond // 1000:03d}Z"
        want = want.zfill(20) if t.year < 1000 else want
        assert from_ms(ms) == want, ms
        assert to_ms(want) == ms
    for bad in [
        "20261332T000000.000Z",
        "20230229T000000.000Z",
        "00000101T000000.000Z",
        "20260101T240000.000Z",
        "20260101T000060.000Z",
        "2026010aT000000.000Z",
    ]:
        with pytest.raises(ValueError):
            to_ms(bad)
