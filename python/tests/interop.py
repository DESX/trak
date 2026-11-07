"""The Python side of the cross-implementation tests (conformance/crosscheck.py).

    python interop.py write OPS.json OUT.trak   replay operations into a new file
    python interop.py read FILE.trak            print the decoded file and check result as JSON

The helpers are also used by test_conformance.py.
"""

from __future__ import annotations

import json
import pathlib
import sys
from typing import Any

if __name__ == "__main__":
    sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1] / "src"))

import trak


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


def replay(path: pathlib.Path | str, ops: list[dict[str, Any]]) -> None:
    """Apply an input.json to a new file at path."""
    with trak.create(path, overwrite=True) as w:
        for op in ops:
            if op["op"] == "define":
                w.define(op["name"], op["schema"])
            else:
                w.write(op["type"], op["value"], op["timestamp"])


def main(argv: list[str]) -> int:
    if len(argv) == 3 and argv[0] == "write":
        replay(argv[2], json.loads(pathlib.Path(argv[1]).read_text(encoding="utf-8")))
        return 0
    if len(argv) == 2 and argv[0] == "read":
        out: dict[str, Any]
        try:
            out = decode(pathlib.Path(argv[1]))
        except (trak.TrakError, AssertionError) as e:
            out = {"error": str(e)}
        try:
            trak.check(argv[1])
            out["check"] = None
        except trak.TrakError as e:
            out["check"] = str(e)
        sys.stdout.write(json.dumps(out, ensure_ascii=False) + "\n")
        return 0
    sys.stderr.write("usage: interop.py write OPS OUT | interop.py read FILE\n")
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
