"""Event families (SPEC §1, §5, §6, §7): schemas, numbering, tables, seeks and append."""

from __future__ import annotations

import pathlib
import random

import pytest

import trak
from trak import _vlq

STAMP = "20261107T140000.000Z"


def stamp(i: int) -> str:
    return f"20261107T14{i // 60000 % 60:02d}{i // 1000 % 60:02d}.{i % 1000:03d}Z"


def make(path: pathlib.Path, ops: list[tuple[str, dict[str, object]]]) -> None:
    with trak.create(path, overwrite=True) as w:
        w.define("build", "// A build.\n{ user: str, ok: enum(yes, no) }")
        w.define("build.step", "// One step.\n{ step: str }")
        w.define("build.step.cmd", "{ argv: [] str }")
        w.define("build.test", "{ passed: num }")
        w.define("other", "{ x: str }")
        for i, (name, value) in enumerate(ops):
            w.write(name, value, stamp(i))


BUILD: dict[str, object] = {"user": "ana", "ok": "yes"}
OPS: list[tuple[str, dict[str, object]]] = [
    ("build", BUILD),
    ("build.step", {**BUILD, "step": "compile"}),
    ("other", {"x": "1"}),
    ("build.step.cmd", {**BUILD, "step": "compile", "argv": ["cc", "-c"]}),
    ("build.test", {**BUILD, "passed": 10}),
    ("build.step", {**BUILD, "step": "link"}),
    ("build", {**BUILD, "user": "bo"}),
]


@pytest.fixture
def fam(tmp_path: pathlib.Path) -> pathlib.Path:
    p = tmp_path / "f.trak"
    make(p, OPS)
    return p


def test_child_schemas(fam: pathlib.Path) -> None:
    with trak.open(fam) as r:
        step = r.schema("build.step")
        assert step.parent == "build"
        assert step.lineage == ("build", "build.step")
        assert step.version == "1.1"
        assert r.schema("build").version == "1.0"
        assert r.schema("build").parent is None
        assert step.description == "One step."
        assert [f.path for f in step.fields] == ["user", "ok", "step"]
        assert [f.path for f in r.schema("build.step.cmd").fields] == [
            "user",
            "ok",
            "step",
            "argv.[]",
        ]
        assert r.schema("build.step.cmd").lineage == ("build", "build.step", "build.step.cmd")
        assert r.schema("build.step").text == "// One step.\n{ step: str }\n"


def test_family_numbers(fam: pathlib.Path) -> None:
    with trak.open(fam) as r:
        events = list(r)
        got = [(e.type, e.family_number(), e.family_number(e.type)) for e in events]
        assert got == [
            ("build", 1, 1),
            ("build.step", 2, 1),
            ("other", 1, 1),
            ("build.step.cmd", 3, 1),
            ("build.test", 4, 1),
            ("build.step", 5, 3),
            ("build", 6, 6),
        ]
        cmd = events[3]
        assert cmd.family_number("build.step") == 2
        with pytest.raises(KeyError):
            cmd.family_number("build.test")
        # Extensions as written: one per ancestor, plus its own once a child exists.
        assert [e.footer.vlqs[5] for e in events] == [1, 2, 0, 2, 1, 2, 1]


def test_family_table(fam: pathlib.Path) -> None:
    with trak.open(fam) as r:
        f = r.family("build")
        assert f.types[0].name == "build"
        assert f.count() == len(f) == 6
        assert [(e.type, e.n) for e in f] == [
            ("build", 1),
            ("build.step", 1),
            ("build.step.cmd", 1),
            ("build.test", 1),
            ("build.step", 2),
            ("build", 2),
        ]
        assert [(e.type, e.n) for e in f.range(5, 2)] == [
            ("build.step", 2),
            ("build.test", 1),
            ("build.step.cmd", 1),
            ("build.step", 1),
        ]
        assert f[4].type == "build.test"
        latest = f.latest()
        assert latest is not None and (latest.type, latest.n) == ("build", 2)
        with pytest.raises(IndexError):
            f.get(0)
        with pytest.raises(IndexError):
            f.get(7)
        with pytest.raises(IndexError):
            list(f.range(1, 7))
        sub = r.family("build.step")
        assert [(e.type, e.n) for e in sub] == [
            ("build.step", 1),
            ("build.step.cmd", 1),
            ("build.step", 2),
        ]
        assert r.family("other").count() == 1
        assert r.family("build.test").count() == 1
        at = f.at_time(stamp(3))
        assert at is not None and (at.type, at.family_number()) == ("build.step.cmd", 3)
        assert f.at_time(stamp(99)) is None


def test_empty_family(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "e.trak"
    with trak.create(p) as w:
        w.define("a", "{}")
        w.define("a.b", "{}")
    with trak.open(p) as r:
        f = r.family("a")
        assert f.count() == 0
        assert f.latest() is None
        assert list(f) == []
        assert f.at_time(STAMP) is None


def test_writer_rules(tmp_path: pathlib.Path) -> None:
    with trak.create(tmp_path / "w.trak") as w:
        with pytest.raises(trak.SchemaError, match="before its child"):
            w.define("a.b", "{}")
        w.define("a", "{ x: str }")
        with pytest.raises(trak.SchemaError, match="already defined by 'a'") as e:
            w.define("a.b", "{\n  y: str,\n  x: str,\n}")
        assert (e.value.line, e.value.column) == (3, 3)
        w.define("a.b", "{ y: str }")
        w.write("a", {"x": "1"}, "20261107T140000.005Z")
        with pytest.raises(trak.ValueError, match="family a"):
            w.write("a.b", {"x": "2", "y": "3"}, "20261107T140000.004Z")
        with pytest.raises(trak.ValueError, match="required"):
            w.write("a.b", {"y": "3"}, "20261107T140000.006Z")
        # The default timestamp never goes back within the family.
        w.write("a.b", {"x": "2", "y": "3"}, "20991107T140000.000Z")
        w.write("a", {"x": "4"})
    with trak.open(tmp_path / "w.trak") as r:
        assert [e.timestamp for e in r][-1] == "20991107T140000.000Z"


def test_late_child_numbers(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "late.trak"
    with trak.create(p) as w:
        w.define("job", "{ i: num }")
        for i in range(5):
            w.write("job", {"i": i})
        w.define("job.retry", "{}")
        w.write("job", {"i": 5})
        w.write("job.retry", {"i": 6})
    with trak.open(p) as r:
        events = list(r)
        assert [e.footer.vlqs[5] for e in events] == [0, 0, 0, 0, 0, 1, 1]
        assert [e.family_number() for e in events] == list(range(1, 8))
        assert r.family("job").get(7).type == "job.retry"
    assert trak.check(p) == 9


def test_append_restores_family_state(tmp_path: pathlib.Path) -> None:
    rng = random.Random(3)
    names = ["build", "build.step", "build.step.cmd", "build.test", "other"]
    ops = []
    for _ in range(300):
        name = rng.choice(names)
        v: dict[str, object] = {"x": "1"} if name == "other" else dict(BUILD)
        if name.startswith("build.step"):
            v["step"] = "s"
        if name == "build.step.cmd":
            v["argv"] = []
        if name == "build.test":
            v["passed"] = 1
        ops.append((name, v))
    whole, parts = tmp_path / "whole.trak", tmp_path / "parts.trak"
    make(whole, ops)
    make(parts, ops[:100])
    for lo, hi in ((100, 101), (101, 250), (250, 300)):
        with trak.open_append(parts) as w:
            for i in range(lo, hi):
                w.write(ops[i][0], ops[i][1], stamp(i))
    assert parts.read_bytes() == whole.read_bytes()
    with trak.open_append(parts) as w, pytest.raises(trak.ValueError, match="family build"):
        w.write("build.test", {**BUILD, "passed": 1}, stamp(0))


def test_family_get_matches_scan(tmp_path: pathlib.Path) -> None:
    rng = random.Random(9)
    p = tmp_path / "wide.trak"
    with trak.create(p) as w:
        w.define("r", "{ i: num }")
        kids = [f"r.k{i}" for i in range(9)]
        for k in kids:
            w.define(k, "{}")
        w.define("r.k0.g", "{}")
        names = ["r", *kids, "r.k0.g"]
        for i in range(3000):
            w.write(rng.choice(names), {"i": i}, stamp(i // 3))
    with trak.open(p) as r:
        events = list(r)
        for fam_name in ("r", "r.k0"):
            f = r.family(fam_name)
            members = [e for e in events if fam_name in e.schema.lineage]
            assert f.count() == len(members)
            for m in [1, 2, len(members), *(rng.randint(1, len(members)) for _ in range(100))]:
                e = f.get(m)
                assert (e.type, e.n) == (members[m - 1].type, members[m - 1].n)
            a, b = 17, min(400, len(members))
            assert [e.offset for e in f.range(a, b)] == [e.offset for e in members[a - 1 : b]]
            assert [e.offset for e in f.range(b, a)] == [e.offset for e in members[a - 1 : b]][::-1]
            for ms_i in (0, 1, 500, 999):
                at = f.at_time(stamp(ms_i))
                want = next((e for e in members if e.timestamp >= stamp(ms_i)), None)
                assert (at and at.offset) == (want and want.offset)
    assert trak.check(p) == 3011


def test_check_rejects_bad_families(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "f.trak"
    make(p, OPS)
    data = p.read_bytes()
    line = next(x for x in data.split(b"\n") if x.startswith(b"==[ build.step 1 "))
    head, letters = line.split(b"]==")
    vlqs = _vlq.decode(letters, 0)
    assert vlqs[5:8] == [2, 2, 1]  # E = 2: number 2 in build, 1 in build.step
    vlqs[6] = 9
    p.write_bytes(data.replace(line, head + b"]==" + _vlq.encode_all(vlqs)))
    with pytest.raises(trak.FormatError, match="family numbers"):
        trak.check(p)


def test_prev(fam: pathlib.Path) -> None:
    with trak.open(fam) as r:
        t = r.table("build.step")
        e = t.latest()
        assert e is not None and e.n == 2
        before = r.footer_reads
        p = e.prev()
        assert r.footer_reads - before == 1
        assert p is not None and (p.n, p.get("step")) == (1, "compile")
        assert p.prev() is None


def test_seek_and_at_time(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "s.trak"
    with trak.create(p) as w:
        w.define("t", "{ i: num }")
        for i in range(1000):
            w.write("t", {"i": i}, stamp(i // 4 * 4))
    with trak.open(p) as r:
        t = r.table("t")
        for i in range(0, 1001, 7):
            e = t.at_time(stamp(i))
            want = -(-i // 4) * 4
            if want >= 1000:
                assert e is None
            else:
                assert e is not None and e.n == want + 1 and e.timestamp == stamp(want)


def test_append_refuses_child_before_parent(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "bad.trak"
    root = pathlib.Path(__file__).resolve().parents[2]
    p.write_bytes((root / "conformance" / "invalid" / "021-child-before-parent.trak").read_bytes())
    with pytest.raises(trak.FormatError, match="before its parent"):
        trak.open_append(p)
