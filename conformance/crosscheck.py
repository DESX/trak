"""Cross-implementation property tests.

For each seed this generates random schemas, values and interleavings, has every
implementation write them, and requires:

- every writer produces the same bytes,
- every reader decodes them to the values that were written,
- every implementation's ``check`` accepts the file.

Usage (from the repository root):

    python conformance/crosscheck.py --seeds 200 \
        --impl python="python python/tests/interop.py" \
        --impl go=go/interop --impl cpp=cpp/build/trak_interop

Each ``--impl`` command is run as ``CMD write OPS.json OUT.trak`` and
``CMD read FILE.trak`` (see the interop tools in each language).
"""

from __future__ import annotations

import argparse
import json
import pathlib
import random
import shlex
import subprocess
import sys
import tempfile
from typing import Any

KEYS = ["a", "b", "c", "dd", "_e", "f1", "Gg", "status", "x_y"]
WORDS = ["ok", "failed", "x", "y_2", "Z"]
STRINGS = [
    "",
    "ana",
    "two words",
    "line one\nline two",
    "trailing newline\n",
    "==[ t 1 20261107T140210.120Z ]==dHeEeEBAADCDBCXA",
    "##[ t 1 1.0 ]##kXAA\n--[ a ]--",
    "Zoë 🚀 日本語",
    "tab\tand \\ backslash",
    "a: b",
    "x" * 700,
]
MAP_KEYS = ["k", "a.b", "c:d", "e]f", "g h", "é", "", "🔑", " ]=="]
NUM_STRINGS = ["-0", "0.25", "1e10", "-1.5E-3", "2e+0", "10.000", "123456789012345678901234567890"]


class Gen:
    def __init__(self, seed: int) -> None:
        self.r = random.Random(seed)

    # --- types: (kind, optional, annotations, extra) ---

    def type(self, depth: int, element: bool = False) -> dict[str, Any]:
        r = self.r
        kinds = ["str", "num", "enum"] + (["list", "map", "obj", "empty"] if depth > 0 else [])
        kind = r.choice(kinds)
        t: dict[str, Any] = {
            "kind": kind,
            # List elements and map values can't be optional: `[] T?` is an optional list (D1).
            "optional": not element and r.random() < 0.3,
            "annotations": [r.choice(["@x", "@unit(ms)", "@a.b-c", "@note(two words)"])]
            if r.random() < 0.2
            else [],
        }
        if kind in ("str", "num", "enum"):
            t["block"] = r.random() < 0.2
            t["words"] = r.sample(WORDS, r.randint(1, 3))
        elif kind in ("list", "map"):
            t["elem"] = self.type(depth - 1, element=True)
        elif kind == "obj":
            t["members"] = self.members(depth - 1)
        return t

    def members(self, depth: int) -> list[tuple[str, dict[str, Any], str | None]]:
        keys = self.r.sample(KEYS, self.r.randint(0, 5))
        return [(k, self.type(depth), self.r.choice([None, None, "a description"])) for k in keys]

    def render(self, t: dict[str, Any]) -> str:
        kind = t["kind"]
        if kind in ("str", "num", "enum"):
            base = f"enum({', '.join(t['words'])})" if kind == "enum" else kind
            text = ("|" if t["block"] else "") + base
        elif kind == "list":
            text = "[] " + self.render(t["elem"])
        elif kind == "map":
            text = "{} " + self.render(t["elem"])
        elif kind == "empty":
            text = "{}"
        else:
            text = self.render_object(t["members"], "  ")
        if t["optional"]:
            text += "?"
        for a in t["annotations"]:
            text += " " + a
        return text

    def render_object(
        self, members: list[tuple[str, dict[str, Any], str | None]], indent: str
    ) -> str:
        if not members:
            return "{ }"
        lines = ["{"]
        for k, t, desc in members:
            line = f"{indent}{k}: {self.render(t)},"
            if desc:
                line += f"  // {desc}"
            lines.append(line)
        lines.append(indent[:-2] + "}")
        return "\n".join(lines)

    # --- values: (input JSON, expected decoded value, whether any leaf is written) ---

    def value(self, t: dict[str, Any]) -> tuple[Any, Any, bool]:
        r = self.r
        kind = t["kind"]
        if t["optional"] and r.random() < 0.3:
            return None, None, False
        if kind == "str":
            s = r.choice(STRINGS)
            return s, s, True
        if kind == "num":
            if r.random() < 0.5:
                n = r.randint(-(2**63), 2**63 - 1) if r.random() < 0.3 else r.randint(-1000, 1000)
                return n, str(n), True
            s = r.choice(NUM_STRINGS)
            return s, s, True
        if kind == "enum":
            w = r.choice(t["words"])
            return w, w, True
        if kind == "list":
            items = [self.value(t["elem"]) for _ in range(r.randint(0, 3))]
            return [i for i, _, _ in items], [e for _, e, _ in items], any(x for _, _, x in items)
        if kind == "map":
            keys = r.sample(MAP_KEYS, r.randint(0, 3))
            entries = [(k, self.value(t["elem"])) for k in keys]
            return (
                [[k, v] for k, (v, _, _) in entries],
                # A key is unknown to readers when its entry writes no leaves (D3).
                [[k if leafy else None, e] for k, (_, e, leafy) in entries],
                any(leafy for _, (_, _, leafy) in entries),
            )
        if kind == "empty":
            return {}, {}, False
        return self.object(t["members"])

    def object(
        self, members: list[tuple[str, dict[str, Any], str | None]]
    ) -> tuple[Any, Any, bool]:
        inp: dict[str, Any] = {}
        exp: dict[str, Any] = {}
        leafy = False
        for k, t, _ in members:
            v, e, x = self.value(t)
            if v is not None or self.r.random() < 0.5:
                inp[k] = v  # absent optionals are sometimes omitted, sometimes null
            exp[k] = e
            leafy = leafy or x
        return inp, exp, leafy

    def case(self) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
        r = self.r
        count = r.randint(27, 32) if r.random() < 0.1 else r.randint(1, 6)
        types = []
        for i in range(count):
            desc = "// Type description.\n" if r.random() < 0.3 else ""
            types.append((f"t{i}", self.members(3), desc))
        ops: list[dict[str, Any]] = []
        events: list[dict[str, Any]] = []
        defined: set[str] = set()
        numbers: dict[str, int] = {}
        ms = 1_794_000_000_000
        for name, members, desc in types:
            if r.random() < 0.3:
                ops.append(
                    {
                        "op": "define",
                        "name": name,
                        "schema": desc + self.render_object(members, "  "),
                    }
                )
                defined.add(name)
        for _ in range(r.randint(0, 80)):
            name, members, desc = r.choice(types)
            if name not in defined:
                ops.append(
                    {
                        "op": "define",
                        "name": name,
                        "schema": desc + self.render_object(members, "  "),
                    }
                )
                defined.add(name)
            ms += r.choice([0, 0, 1, 7, 1000])
            stamp = format_ms(ms)
            inp, exp, _ = self.object(members)
            numbers[name] = numbers.get(name, 0) + 1
            ops.append({"op": "write", "type": name, "value": inp, "timestamp": stamp})
            events.append({"type": name, "n": numbers[name], "timestamp": stamp, "value": exp})
        return ops, events


def format_ms(ms: int) -> str:
    import datetime as dt

    t = dt.datetime(1970, 1, 1, tzinfo=dt.timezone.utc) + dt.timedelta(milliseconds=ms)
    return t.strftime("%Y%m%dT%H%M%S.") + f"{t.microsecond // 1000:03d}Z"


def run(cmd: list[str], *args: str) -> subprocess.CompletedProcess[bytes]:
    return subprocess.run([*cmd, *args], check=False, capture_output=True, timeout=120)


def main() -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("--seeds", type=int, default=100)
    ap.add_argument("--first-seed", type=int, default=0)
    ap.add_argument("--impl", action="append", default=[], metavar="NAME=CMD")
    args = ap.parse_args()
    impls = {}
    for spec in args.impl:
        name, _, cmd = spec.partition("=")
        impls[name] = shlex.split(cmd, posix=not sys.platform.startswith("win"))
    if len(impls) < 2:
        ap.error("give at least two --impl")
    failures = 0
    with tempfile.TemporaryDirectory() as tmp:
        d = pathlib.Path(tmp)
        for seed in range(args.first_seed, args.first_seed + args.seeds):
            ops, events = Gen(seed).case()
            ops_path = d / "ops.json"
            ops_path.write_text(json.dumps(ops, ensure_ascii=False), encoding="utf-8")
            outputs: dict[str, bytes] = {}
            for name, cmd in impls.items():
                out = d / f"{name}.trak"
                p = run(cmd, "write", str(ops_path), str(out))
                if p.returncode != 0:
                    print(f"seed {seed}: {name} write failed: {p.stderr.decode(errors='replace')}")
                    failures += 1
                    break
                outputs[name] = out.read_bytes()
            else:
                reference = next(iter(outputs.values()))
                differ = [n for n, b in outputs.items() if b != reference]
                if differ:
                    print(
                        f"seed {seed}: writers disagree: {', '.join(differ)} differ from {next(iter(outputs))}"
                    )
                    failures += 1
                    (d / f"seed{seed}.ops.json").write_text(
                        ops_path.read_text(encoding="utf-8"), encoding="utf-8"
                    )
                    continue
                decoded: dict[str, Any] = {}
                for name, cmd in impls.items():
                    for writer in impls:
                        p = run(cmd, "read", str(d / f"{writer}.trak"))
                        result = (
                            json.loads(p.stdout.decode("utf-8"))
                            if p.returncode == 0
                            else {"error": p.stderr.decode()}
                        )
                        problem = None
                        if "error" in result:
                            problem = result["error"]
                        elif result["check"] is not None:
                            problem = f"check failed: {result['check']}"
                        elif result["events"] != events:
                            problem = "decoded events differ from what was written"
                        if problem:
                            print(f"seed {seed}: {name} reading {writer}'s file: {problem}")
                            failures += 1
                        decoded[name] = result.get("types")
                if len({json.dumps(t, sort_keys=True) for t in decoded.values()}) > 1:
                    print(f"seed {seed}: readers disagree on schemas")
                    failures += 1
            if failures > 10:
                break
    total = args.seeds
    print(
        f"{total} seeds, {len(impls)} implementations: {'ok' if not failures else f'{failures} failures'}"
    )
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
