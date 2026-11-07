"""Generate the conformance vectors.

Run from the repository root:

    python conformance/generate.py

Write vectors go through the Python reference writer. Files that writer can't produce
(extensions, newer versions, other directory policies, corrupt files) are built by
``Raw``, a deliberately separate, minimal encoder. ``Raw`` must also reproduce the SPEC
example on its own, which cross-checks the library writer.

Every ``.expect.json`` comes from the Python reader. Review new vectors against SPEC.md
by hand before committing them.
"""

from __future__ import annotations

import json
import pathlib
import shutil
import sys
from typing import Any

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent / "python" / "src"))

import trak

# --- an independent minimal encoder --------------------------------------------------


def vlq(n: int) -> bytes:
    digits = [n % 26]
    n //= 26
    while n:
        digits.append(n % 26)
        n //= 26
    return bytes(97 + d for d in reversed(digits[1:])) + bytes([65 + digits[0]])


def J(n: int) -> int:
    # Straight from the recurrence in SPEC §7.
    j = [0]
    for k in range(1, n + 1):
        p = k - 1
        if p > j[p] and p - j[p] == j[p] - j[j[p]]:
            j.append(j[j[p]])
        else:
            j.append(p)
    return j[n]


class Raw:
    """Builds files object by object, with every footer field under the caller's control."""

    def __init__(self, magic: bytes = b"trak 1.0\n") -> None:
        self.buf = bytearray(magic)
        self.names: list[str] = []
        self.ends: dict[str, list[int]] = {}  # node ends per type, node 0 = schema
        self.schema_end: int | None = None
        self.cov = [-1]
        self.seq = 0

    def schema(
        self,
        name: str,
        body: str,
        *,
        version: str = "1.0",
        ext: tuple[int, ...] = (),
        prev: int | None = None,
        index: int | None = None,
        pad: tuple[int, ...] = (),
    ) -> None:
        """``pad`` lists VLQ positions to write with a (forbidden) leading zero digit."""
        start = len(self.buf)
        if prev is None:
            prev = 0 if self.schema_end is None else start - self.schema_end + 1
        data = body.encode("ascii")
        self.names.append(name)
        index = len(self.names) if index is None else index
        vlqs = [len(data), prev, len(ext), *ext]
        letters = b"".join(b"a" * (i in pad) + vlq(v) for i, v in enumerate(vlqs))
        self.buf += data + f"##[ {name} {index} {version} ]##".encode() + letters
        self.buf += b"\n"
        self.schema_end = len(self.buf)
        self.ends[name] = [self.schema_end]
        self.cov.append(-1)

    def event(
        self,
        name: str,
        ts: str,
        leaves: list[tuple[str, bytes, bool]],
        values: list[int],
        *,
        dir_type: int | None = None,
        ext: tuple[int, ...] = (),
        n: int | None = None,
    ) -> None:
        """``leaves`` are (path, value, block) in order; ``values`` are the value VLQs."""
        start = len(self.buf)
        body = bytearray()
        for path, value, block in leaves:
            p = path.encode("utf-8")
            body += value + b"\n--[ " + p + b" ]--\n" if block else p + b": " + value + b"\n"
        ends = self.ends[name]
        n = len(ends) if n is None else n
        t = self.names.index(name) + 1
        self.cov[t] = self.seq
        if dir_type is None:
            dir_type = min((c, i) for i, c in enumerate(self.cov) if i != t)[1]
        self.cov[dir_type] = self.seq
        self.seq += 1
        assert self.schema_end is not None
        dir_end = self.schema_end if dir_type == 0 else self.ends[self.names[dir_type - 1]][-1]
        vlqs = [
            len(body),
            start - ends[n - 1] + 1,
            start - ends[J(n)] + 1,
            start - dir_end + 1,
            dir_type,
            len(ext),
            *ext,
            *values,
        ]
        self.buf += body + f"==[ {name} {n} {ts} ]==".encode() + b"".join(map(vlq, vlqs)) + b"\n"
        ends.append(len(self.buf))

    def bytes(self) -> bytes:
        return bytes(self.buf)


BUILD = """// One CI build run.
{
  user: str,                  // who triggered the build
  status: enum(ok, failed),
  duration: num @ms,          // wall-clock time
  tags: [] str,
  stdout: |str @log,          // compiler output
  error: str?,                // set when the build failed
}
"""
DEPLOY = """// A release pushed to an environment.
{
  target: str,
  version: str @semver,
}
"""
T1 = "20261107T140210.120Z"
T2 = "20261107T140322.415Z"


def raw_spec_example() -> Raw:
    r = Raw()
    r.schema("build", BUILD)
    r.schema("deploy", DEPLOY)
    r.event(
        "build",
        T1,
        [
            ("user", b"ana", False),
            ("status", b"ok", False),
            ("duration", b"910", False),
            ("tags.0", b"ci", False),
            ("stdout", b"Compiling core...\nDone.", True),
        ],
        [3, 2, 3, 1, 2, 23, 0],
    )
    r.event(
        "build",
        T2,
        [
            ("user", b"joel", False),
            ("status", b"failed", False),
            ("duration", b"1520", False),
            ("tags.0", b"ci", False),
            ("tags.1", b"nightly", False),
            ("stdout", b"Compiling core...\nld: undefined symbol main", True),
            ("error", b"link failed", False),
        ],
        [4, 6, 4, 2, 2, 7, 43, 1, 11],
    )
    return r


# --- write vectors ---------------------------------------------------------------


class Ops:
    """Collects define/write operations for an ``input.json``."""

    def __init__(self) -> None:
        self.ops: list[dict[str, Any]] = []
        self.ms = 1_794_000_000_000  # 2026-11-07, advanced 1 ms per write unless pinned

    def define(self, name: str, schema: str) -> None:
        self.ops.append({"op": "define", "name": name, "schema": schema})

    def write(self, type_: str, value: Any, timestamp: str | None = None) -> None:
        if timestamp is None:
            self.ms += 1
            timestamp = trak._time.from_ms(self.ms)
        self.ops.append({"op": "write", "type": type_, "value": value, "timestamp": timestamp})


def pairs(*kv: Any) -> list[list[Any]]:
    """A map value in input.json: ordered [key, value] pairs."""
    return [[k, v] for k, v in kv]


def w_spec_example(o: Ops) -> None:
    o.define("build", BUILD)
    o.define("deploy", DEPLOY)
    o.write(
        "build",
        {
            "user": "ana",
            "status": "ok",
            "duration": 910,
            "tags": ["ci"],
            "stdout": "Compiling core...\nDone.",
            "error": None,
        },
        T1,
    )
    o.write(
        "build",
        {
            "user": "joel",
            "status": "failed",
            "duration": 1520,
            "tags": ["ci", "nightly"],
            "stdout": "Compiling core...\nld: undefined symbol main",
            "error": "link failed",
        },
        T2,
    )


def w_magic_only(o: Ops) -> None:
    pass


def w_type_without_events(o: Ops) -> None:
    o.define("idle", "// Never written.\n{ a: str }\n")
    o.define("busy", "{ a: str }\n")
    o.write("busy", {"a": "x"})
    o.write("busy", {"a": "y"})


def w_empty_lists_and_maps(o: Ops) -> None:
    o.define("t", "{ l: [] str, m: {} num, ll: [] [] str, after: str }\n")
    o.write("t", {"l": [], "m": [], "ll": [], "after": "x"})
    o.write("t", {"l": [], "m": [], "ll": [[], [], ["z"]], "after": "y"})


def w_absent_optionals(o: Ops) -> None:
    o.define("t", "{ a: str?, b: num?, c: |str?, d: enum(x)?, e: { f: str }?, g: str }\n")
    o.write("t", {"a": None, "b": None, "c": None, "d": None, "e": None, "g": "only"})
    o.write("t", {"a": "1", "b": 2, "c": "3", "d": "x", "e": {"f": "5"}, "g": "all"})
    o.write("t", {"g": "missing keys are absent too"})


def w_block_forged_structure(o: Ops) -> None:
    o.define("log", "{ out: |str, note: str }\n")
    forged = (
        "==[ log 1 20261107T140210.120Z ]==dHeEeEBAADCDBCXA\n"
        "##[ log 1 1.0 ]##kXAA\n"
        "--[ out ]--\n"
        "note: forged\n"
        "==[ log 99 20261107T140210.120Z ]==A"
    )
    o.write("log", {"out": forged, "note": "real"})
    o.write("log", {"out": forged + "\n", "note": "--[ note ]--"})


def w_inline_lf(o: Ops) -> None:
    o.define("t", "{ a: str, b: str }\n")
    o.write("t", {"a": "line one\nb: not a field\n==[ t 2 x ]==A", "b": "end"})


def w_utf8(o: Ops) -> None:
    o.define("t", "{ name: str, tags: {} str, block: |str }\n")
    o.write(
        "t",
        {
            "name": "Zoë 🚀 日本語",
            "tags": pairs(("ключ", "значение"), ("🔑", "∞"), ("é", "")),
            "block": "naïve\ncafé",
        },
    )


def w_map_key_punctuation(o: Ops) -> None:
    o.define("t", "{ m: {} str, nested: {} { v: str } }\n")
    o.write(
        "t",
        {
            "m": pairs(
                ("a.b", "1"),
                ("c:d", "2"),
                ("e]f", "3"),
                ("g h", "4"),
                (" ]==", "5"),
                ("", "6"),
            ),
            "nested": pairs(("x.y", {"v": "7"}), ("x", {"v": "8"})),
        },
    )


def w_many_types(o: Ops) -> None:
    for i in range(1, 36):
        o.define(f"type{i:02d}", f"// Type number {i}.\n{{ i: num }}\n")
    for round_ in range(3):
        for i in range(1, 36, 2 + round_):
            o.write(f"type{i:02d}", {"i": i})


def w_long_values(o: Ops) -> None:
    o.define("t", "{ short: str, long: str, block: |str, list: [] str }\n")
    o.write(
        "t",
        {
            "short": "x" * 675,
            "long": "y" * 676,
            "block": "line\n" * 4000,
            "list": ["z"] * 700,
        },
    )


def w_schema_long_before(o: Ops) -> None:
    o.define("early", "{ a: str }\n")
    o.define("noise", "{ i: num }\n")
    for i in range(200):
        o.write("noise", {"i": i})
    o.write("early", {"a": "finally"})
    o.write("noise", {"i": 200})


def w_deep_nesting(o: Ops) -> None:
    o.define(
        "t",
        "{\n  jobs: [] {\n    name: str,\n    env: {} str,\n    steps: [] { cmd: str, out: |str?, meta: {} [] num },\n  },\n}\n",
    )
    o.write(
        "t",
        {
            "jobs": [
                {
                    "name": "build",
                    "env": pairs(("CC", "gcc"), ("CFLAGS", "-O2")),
                    "steps": [
                        {
                            "cmd": "make",
                            "out": "ok\n",
                            "meta": pairs(("t", [1, 2]), ("e", [])),
                        },
                        {"cmd": "test", "out": None, "meta": []},
                    ],
                },
                {"name": "lint", "env": [], "steps": []},
            ]
        },
    )


def w_chain_10000(o: Ops) -> None:
    o.define("tick", "{ i: num }\n")
    for i in range(1, 10001):
        o.write("tick", {"i": i})


def w_interleaved(o: Ops) -> None:
    for name in ("a", "b", "c", "d"):
        o.define(name, "{ v: str }\n")
    seq = "aababcabcdaaaaddddbcbcbcab"
    for k, name in enumerate(seq):
        o.write(name, {"v": f"{name}{k}"})


def w_num_forms(o: Ops) -> None:
    o.define("t", "{ n: [] num }\n")
    o.write(
        "t",
        {
            "n": [
                0,
                -7,
                -9223372036854775808,
                18446744073709551615,
                "123456789012345678901234567890",
                "-0",
                "0.25",
                "1e10",
                "-1.5E-3",
                "2e+0",
                "10.000",
            ]
        },
    )


def w_enums_annotations(o: Ops) -> None:
    o.define(
        "t",
        "{\n  level: enum(debug, info, warn, error,) @color,\n"
        "  size: num @unit(bytes) @x.y-z(a b, c),\n  ids: [] str @uuid,\n}\n",
    )
    o.write("t", {"level": "warn", "size": 1024, "ids": ["a", "b"]})
    o.write("t", {"level": "debug", "size": 0, "ids": []})


def w_descriptions(o: Ops) -> None:
    o.define(
        "t",
        "// First line of the type description.\n"
        "//   Second line, indented.\n"
        "\n"
        "{\n"
        "  a: str, b: str,          // describes b, the last key on the line\n"
        "  // a standalone comment describes nothing\n"
        "  c: {                     // describes c\n"
        "    d: [] str,             // describes d\n"
        "  },\n"
        "}\n"
        "// trailing comment\n",
    )
    o.write("t", {"a": "1", "b": "2", "c": {"d": ["3"]}})


def w_empty_objects(o: Ops) -> None:
    o.define("t", "{ e: {}, l: [] {}, m: {} {}, o: { inner: {} }? }\n")
    o.write("t", {"e": {}, "l": [{}, {}, {}], "m": pairs(("k", {})), "o": {"inner": {}}})
    o.define("bare", "{}\n")
    o.write("bare", {})


def w_keyless_map_entries(o: Ops) -> None:
    o.define("t", "{ m: {} [] str, n: {} str? }\n")
    o.write("t", {"m": pairs(("empty", []), ("full", ["x"]), ("also empty", [])), "n": None})


def w_empty_strings(o: Ops) -> None:
    o.define("t", "{ a: str, b: |str, c: [] str }\n")
    o.write("t", {"a": "", "b": "", "c": ["", ""]})


def w_optional_containers(o: Ops) -> None:
    o.define("t", "{ l: [] str?, m: {} str?, o: { x: str }? }\n")
    o.write("t", {"l": None, "m": None, "o": None})
    o.write("t", {"l": [], "m": [], "o": {"x": "1"}})
    o.write("t", {"l": ["a"], "m": pairs(("k", "v")), "o": None})


def w_same_timestamps(o: Ops) -> None:
    o.define("a", "{ i: num }\n")
    o.define("b", "{ i: num }\n")
    for i in range(5):
        o.write("a", {"i": i}, "20261107T000000.000Z")
        o.write("b", {"i": i}, "20000101T000000.000Z")


def w_blocks_in_lists(o: Ops) -> None:
    o.define("t", "{ outs: [] |str, by: {} |str }\n")
    o.write("t", {"outs": ["one\n", "two", ""], "by": pairs(("k.1", "v\nv"))})


def w_top_level_annotation(o: Ops) -> None:
    o.define("t", "// Annotated type.\n{ a: str } @table(wide) @sorted\n")
    o.write("t", {"a": "x"})


WRITE = {
    "001-spec-example": w_spec_example,
    "002-magic-only": w_magic_only,
    "003-type-without-events": w_type_without_events,
    "004-empty-lists-and-maps": w_empty_lists_and_maps,
    "005-absent-optionals": w_absent_optionals,
    "006-block-forged-structure": w_block_forged_structure,
    "007-inline-lf": w_inline_lf,
    "008-utf8": w_utf8,
    "009-map-key-punctuation": w_map_key_punctuation,
    "010-many-types": w_many_types,
    "011-long-values": w_long_values,
    "012-schema-long-before": w_schema_long_before,
    "013-deep-nesting": w_deep_nesting,
    "014-chain-10000": w_chain_10000,
    "015-interleaved": w_interleaved,
    "016-num-forms": w_num_forms,
    "017-enums-annotations": w_enums_annotations,
    "018-descriptions": w_descriptions,
    "019-empty-objects": w_empty_objects,
    "020-keyless-map-entries": w_keyless_map_entries,
    "021-empty-strings": w_empty_strings,
    "022-optional-containers": w_optional_containers,
    "023-same-timestamps": w_same_timestamps,
    "024-blocks-in-lists": w_blocks_in_lists,
    "025-top-level-annotation": w_top_level_annotation,
}


def run_ops(path: pathlib.Path, ops: list[dict[str, Any]]) -> None:
    with trak.create(path, overwrite=True) as w:
        for op in ops:
            if op["op"] == "define":
                w.define(op["name"], op["schema"])
            else:
                w.write(op["type"], op["value"], op["timestamp"])


# --- valid-only vectors (built with Raw) ---------------------------------------------------


def v_torn_tail() -> bytes:
    good = raw_spec_example().bytes()
    return good + b"user: ana\nstatus: ok\nduration: 9"


def v_torn_tail_partial_footer() -> bytes:
    good = raw_spec_example().bytes()
    return good + b"user: x\n==[ build 3 20261107T1403"


def v_extensions() -> bytes:
    r = Raw()
    r.schema("t", "{ a: str, b: num }\n", ext=(5, 0, 700))
    r.schema("u", "{ c: str }\n", ext=(1,))
    r.event("t", T1, [("a", b"x", False), ("b", b"1", False)], [1, 1], ext=(3, 1000))
    r.event("u", T1, [("c", b"y", False)], [1], ext=(0,))
    r.event("t", T2, [("a", b"zz", False), ("b", b"22", False)], [2, 2])
    return r.bytes()


def v_unknown_leaf_and_annotation() -> bytes:
    r = Raw(b"trak 1.2\n")
    r.schema(
        "t",
        "{\n  d: duration(ms) @future.thing(x y),\n  v: |si.meter?,\n  l: [] ipaddr @also.new,\n}\n",
        version="1.2",
    )
    r.event(
        "t",
        T1,
        [("d", b"1500", False), ("v", b"3.5\n", True), ("l.0", b"10.0.0.1", False)],
        [4, 1, 4, 1, 8],
    )
    r.event("t", T2, [("d", b"0", False)], [1, 0, 0])
    return r.bytes()


def v_newer_minor() -> bytes:
    r = Raw(b"trak 1.9\n")
    r.schema("old", "{ a: str }\n")
    r.schema("new", "// From a later minor.\n{ a: str @since(1.9) }\n", version="1.9")
    r.event("new", T1, [("a", b"x", False)], [1])
    r.event("old", T2, [("a", b"y", False)], [1])
    return r.bytes()


def v_unparseable_newer_schema() -> bytes:
    r = Raw(b"trak 1.4\n")
    r.schema("ok", "{ a: str }\n")
    r.schema("future", '{ a: str = "default" }\n', version="1.4")
    r.event("future", T1, [("a", b"x", False)], [1])
    r.event("ok", T1, [("a", b"y", False)], [1])
    r.event("future", T2, [("a", b"z", False)], [1])
    return r.bytes()


def v_other_dir_policy() -> bytes:
    # Always point the directory at the schema chain; readers must still work.
    r = Raw()
    r.schema("a", "{ i: num }\n")
    r.schema("b", "{ i: num }\n")
    r.schema("c", "{ i: num }\n")
    for k in range(40):
        name = "abc"[k % 3] if k < 30 else "a"
        r.event(name, T1, [("i", str(k).encode(), False)], [len(str(k))], dir_type=0)
    return r.bytes()


def v_self_dir() -> bytes:
    # The directory may name the event's own type: the pointer is then its own step.
    r = Raw()
    r.schema("a", "{ i: num }\n")
    r.schema("b", "{ i: num }\n")
    r.event("b", T1, [("i", b"0", False)], [1], dir_type=2)
    r.event("a", T1, [("i", b"1", False)], [1], dir_type=1)
    r.event("a", T1, [("i", b"2", False)], [1], dir_type=2)
    return r.bytes()


VALID_RAW = {
    "101-torn-tail": v_torn_tail,
    "102-torn-tail-partial-footer": v_torn_tail_partial_footer,
    "103-extensions": v_extensions,
    "104-unknown-leaf-and-annotation": v_unknown_leaf_and_annotation,
    "105-newer-minor": v_newer_minor,
    "106-unparseable-newer-schema": v_unparseable_newer_schema,
    "107-other-dir-policy": v_other_dir_policy,
    "108-self-dir": v_self_dir,
}


# --- invalid vectors ------------------------------------------------------------------------


def spec() -> bytes:
    return raw_spec_example().bytes()


def replace_once(data: bytes, old: bytes, new: bytes) -> bytes:
    assert data.count(old) == 1, old
    return data.replace(old, new)


def with_trailer(r: Raw) -> bytes:
    """Append one more valid event so a corruption isn't the file's last object."""
    if "zz" not in r.names:
        r.schema("zz", "{ }\n")
    r.event("zz", T2, [], [])
    return r.bytes()


def i_bad_magic() -> tuple[bytes, str]:
    return b"TRAK 1.0\n" + spec()[9:], "bad magic line"


def i_unknown_major() -> tuple[bytes, str]:
    return b"trak 2.0\n" + spec()[9:], "unknown major version 2"


def i_body_length_past_start() -> tuple[bytes, str]:
    return (
        b"trak 1.0\n{}\n##[ a 1 1.0 ]##EAA\n",
        "schema body length 4 reaches past the magic line",
    )


def i_walk_not_at_footer() -> tuple[bytes, str]:
    r = Raw()
    r.schema("t", "{ a: str, b: str }\n")
    r.event("t", T1, [("a", b"xy", False), ("b", b"z", False)], [2, 2])  # b is 1 byte
    return with_trailer(r), "value lengths walk one byte past the footer start"


def i_bad_vlq_letter() -> tuple[bytes, str]:
    return replace_once(spec(), b"]##kXAA", b"]##kX0A"), "'0' is not a VLQ letter"


def i_vlq_leading_zero() -> tuple[bytes, str]:
    r = Raw()
    r.schema("a", "{ }\n")
    r.schema("b", "{ }\n", pad=(1,))
    return with_trailer(r), "b's previous-schema VLQ is written aB, with a leading zero (D5)"


def i_pointer_wrong_type() -> tuple[bytes, str]:
    r = Raw()
    r.schema("a", "{ }\n")
    r.schema("b", "{ }\n")
    r.event("a", T1, [], [])
    r.event("b", T1, [], [])
    # b's second event steps to a's event instead of b's.
    start = len(r.buf)
    vlqs = [
        0,
        start - r.ends["a"][1] + 1,
        start - r.ends["b"][0] + 1,
        start - r.ends["a"][1] + 1,
        1,
        0,
    ]
    r.buf += b"==[ b 2 " + T2.encode() + b" ]==" + b"".join(map(vlq, vlqs)) + b"\n"
    r.ends["b"].append(len(r.buf))
    return with_trailer(r), "b 2's step pointer lands on an event of type a"


def i_duplicate_schema_name() -> tuple[bytes, str]:
    r = Raw()
    r.schema("a", "{ }\n")
    r.schema("b", "{ }\n")
    r.names[-1] = "b_"
    r.schema("a", "{ x: str }\n")
    r.ends["a"] = [r.schema_end or 0]
    r.schema("zz", "{ }\n")
    return r.bytes(), "type name a is defined twice"


def i_event_before_schema() -> tuple[bytes, str]:
    r = Raw()
    r.schema("a", "{ }\n")
    r.event("a", T1, [], [])
    data = r.bytes()
    # Rename the event's type so its schema is missing.
    data = replace_once(data, b"==[ a 1", b"==[ q 1")
    r2 = Raw()
    r2.buf = bytearray(data)
    r2.names, r2.ends, r2.schema_end, r2.cov = r.names, r.ends, r.schema_end, r.cov
    return with_trailer(r2), "event of type q appears before any schema q"


def i_bad_num() -> tuple[bytes, str]:
    r = Raw()
    r.schema("t", "{ n: num }\n")
    r.event("t", T1, [("n", b"01", False)], [2])
    return with_trailer(r), "num value 01 has a leading zero"


def i_bad_enum() -> tuple[bytes, str]:
    r = Raw()
    r.schema("t", "{ e: enum(on, off) }\n")
    r.event("t", T1, [("e", b"maybe", False)], [5])
    return with_trailer(r), "enum value maybe is not one of on, off"


def i_timestamp_backwards() -> tuple[bytes, str]:
    r = Raw()
    r.schema("t", "{ }\n")
    r.event("t", T2, [], [])
    r.event("t", T1, [], [])
    return with_trailer(r), "t 2 is earlier than t 1"


def i_wrong_event_number() -> tuple[bytes, str]:
    r = Raw()
    r.schema("t", "{ }\n")
    r.event("t", T1, [], [])
    r.event("t", T1, [], [], n=2)
    data = r.bytes()
    data = replace_once(data, b"==[ t 2 ", b"==[ t 5 ")
    r.buf = bytearray(data)
    return with_trailer(r), "second event of t is numbered 5"


def i_bad_dir_pointer() -> tuple[bytes, str]:
    r = Raw()
    r.schema("a", "{ }\n")
    r.schema("b", "{ }\n")
    r.event("a", T1, [], [])
    r.event("a", T1, [], [], dir_type=2)
    data = r.bytes()
    # Point the second event's directory at a's first event while claiming type b.
    line = data[data.rindex(b"==[ a 2") :].split(b"\n")[0]
    vlqs = trak._vlq.decode(line.split(b"]==")[1])
    start = len(data) - len(line) - 1 - vlqs[0]
    vlqs[3] = start - r.ends["a"][1] + 1
    new = line.split(b"]==")[0] + b"]==" + b"".join(map(vlq, vlqs))
    r.buf = bytearray(data.replace(line, new))
    r.ends["a"][-1] = len(r.buf)
    return with_trailer(r), "dir pointer for type b lands on an event of type a"


def i_bad_block_label() -> tuple[bytes, str]:
    data = replace_once(spec(), b"Done.\n--[ stdout ]--", b"Done.\n--[ stdouX ]--")
    return data, "block label names stdouX instead of stdout"


def i_schema_index_gap() -> tuple[bytes, str]:
    r = Raw()
    r.schema("a", "{ }\n")
    r.schema("b", "{ }\n", index=3)
    r.event("a", T1, [], [])
    return r.bytes(), "second schema has index 3"


def i_bad_timestamp() -> tuple[bytes, str]:
    r = Raw()
    r.schema("t", "{ }\n")
    r.event("t", "20261332T250000.000Z", [], [])
    return with_trailer(r), "timestamp 20261332T250000.000Z is not a real time"


def i_schema_syntax() -> tuple[bytes, str]:
    r = Raw()
    r.schema("t", "{ a: str,, }\n")
    return with_trailer(r), "1.0 schema doesn't parse"


INVALID = {
    "001-bad-magic": i_bad_magic,
    "002-unknown-major": i_unknown_major,
    "003-body-length-past-start": i_body_length_past_start,
    "004-walk-not-at-footer": i_walk_not_at_footer,
    "005-bad-vlq-letter": i_bad_vlq_letter,
    "006-vlq-leading-zero": i_vlq_leading_zero,
    "007-pointer-wrong-type": i_pointer_wrong_type,
    "008-duplicate-schema-name": i_duplicate_schema_name,
    "009-event-before-schema": i_event_before_schema,
    "010-bad-num": i_bad_num,
    "011-bad-enum": i_bad_enum,
    "012-timestamp-backwards": i_timestamp_backwards,
    "013-wrong-event-number": i_wrong_event_number,
    "014-bad-dir-pointer": i_bad_dir_pointer,
    "015-bad-block-label": i_bad_block_label,
    "016-schema-index-gap": i_schema_index_gap,
    "017-bad-timestamp": i_bad_timestamp,
    "018-schema-syntax": i_schema_syntax,
}


# --- expectations ---------------------------------------------------------------------------


def native(v: Any) -> Any:
    if isinstance(v, dict):
        return {k: native(x) for k, x in v.items()}
    if isinstance(v, list):
        return [native(x) for x in v]
    if isinstance(v, tuple):
        return [native(x) for x in v]
    return v


def expect(path: pathlib.Path) -> dict[str, Any]:
    with trak.open(path) as r:
        types = []
        for s in r.types():
            types.append(
                {
                    "name": s.name,
                    "index": s.index,
                    "version": s.version,
                    "description": s.description,
                    "parsed": s.root is not None,
                    "annotations": [{"name": a.name, "text": a.text} for a in s.annotations],
                    "fields": [
                        {
                            "path": f.path,
                            "type": f.type,
                            "enum": list(f.enum),
                            "unknown": f.unknown,
                            "block": f.block,
                            "optional": f.optional,
                            "annotations": [
                                {"name": a.name, "text": a.text} for a in f.annotations
                            ],
                            "description": f.description,
                        }
                        for f in s.fields
                    ],
                }
            )
        events = []
        for e in r:
            events.append(
                {
                    "type": e.type,
                    "n": e.n,
                    "timestamp": e.timestamp,
                    "value": native(e.to_native()) if e.schema.root is not None else None,
                }
            )
        return {"types": types, "events": events}


def lines(items: list[Any]) -> str:
    """A JSON list with one compact item per line."""
    if not items:
        return "[]"
    return "[\n" + ",\n".join(json.dumps(x, ensure_ascii=False) for x in items) + "\n]"


def dump_ops(path: pathlib.Path, ops: list[dict[str, Any]]) -> None:
    path.write_text(lines(ops) + "\n", encoding="utf-8")


def dump_expect(path: pathlib.Path, data: dict[str, Any]) -> None:
    types = json.dumps(data["types"], indent=2, ensure_ascii=False)
    text = f'{{\n"types": {types},\n"events": {lines(data["events"])}\n}}\n'
    path.write_text(text, encoding="utf-8")


def main() -> None:
    assert raw_spec_example().bytes() == spec(), "Raw disagrees with itself"
    for sub in ("valid", "invalid", "write"):
        shutil.rmtree(HERE / sub, ignore_errors=True)
        (HERE / sub).mkdir()

    for name, fn in WRITE.items():
        o = Ops()
        fn(o)
        trak_path = HERE / "write" / f"{name}.trak"
        run_ops(trak_path, o.ops)
        dump_ops(HERE / "write" / f"{name}.input.json", o.ops)
        shutil.copy(trak_path, HERE / "valid" / f"{name}.trak")

    spec_text = (HERE.parent / "SPEC.md").read_text(encoding="utf-8")
    section = spec_text[spec_text.index("## 13. Example") :]
    start = section.index("```\n") + 4
    example = section[start : section.index("```", start)].encode()
    assert (HERE / "write" / "001-spec-example.trak").read_bytes() == example
    assert raw_spec_example().bytes() == example

    for name, vfn in VALID_RAW.items():
        (HERE / "valid" / f"{name}.trak").write_bytes(vfn())

    for p in sorted((HERE / "valid").glob("*.trak")):
        try:
            trak.check(p)
        except trak.TrakError as e:
            raise SystemExit(f"valid/{p.name} fails check: {e}") from None
        dump_expect(p.with_suffix(".expect.json"), expect(p))

    for name, ifn in INVALID.items():
        data, reason = ifn()
        p = HERE / "invalid" / f"{name}.trak"
        p.write_bytes(data)
        try:
            trak.check(p)
        except trak.TrakError as e:
            offset = getattr(e, "offset", None)
            text = f"{reason}\n" + (f"offset: {offset}\n" if offset is not None else "")
            (HERE / "invalid" / f"{name}.reason.txt").write_text(text, encoding="utf-8")
            print(f"invalid/{name}: {e}")
        else:
            raise SystemExit(f"invalid/{name} passes check")

    counts = {sub: len(list((HERE / sub).glob("*.trak"))) for sub in ("valid", "invalid", "write")}
    print(counts)


if __name__ == "__main__":
    main()
