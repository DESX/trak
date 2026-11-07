"""Measure the Python library on the shared 1M-event workload (see README.md).

python bench/bench_python.py --file /tmp/bench.trak
"""

from __future__ import annotations

import argparse
import pathlib
import random
import sys
import time
from typing import Any

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1] / "python" / "src"))

import trak
from trak._time import from_ms

BASE = 1_794_000_000_000  # 2026-11-06, in ms

DEFS = [
    ("build", "{ user: str, status: enum(ok, failed), duration: num, tags: [] str }"),
    ("deploy", "{ target: str, version: str }"),
    ("log", "{ msg: str }"),
    ("metric", "{ name: str, value: num }"),
]


def event(i: int) -> tuple[str, dict[str, Any]]:
    k = i % 4
    if k == 0:
        return "build", {
            "user": f"user{i % 100}",
            "status": "failed" if i % 3 == 0 else "ok",
            "duration": i % 5000,
            "tags": ["ci", "nightly"] if i % 2 else ["ci"],
        }
    if k == 1:
        return "deploy", {
            "target": "staging" if i % 2 else "prod",
            "version": f"1.{i % 50}.0",
        }
    if k == 2:
        return "log", {"msg": f"request {i} served"}
    return "metric", {"name": "latency", "value": i % 1000}


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--file", default="bench.trak")
    ap.add_argument("-n", type=int, default=1_000_000)
    args = ap.parse_args()

    start = time.perf_counter()
    with trak.create(args.file, overwrite=True) as w:
        for name, definition in DEFS:
            w.define(name, definition)
        for i in range(args.n):
            name, value = event(i)
            w.write(name, value, from_ms(BASE + i))
    print(f"write              {args.n / (time.perf_counter() - start):8.0f} events/s")

    opens = 200
    start = time.perf_counter()
    for _ in range(opens):
        with trak.open(args.file) as r:
            r.table("build").count()
    print(f"open + count()     {(time.perf_counter() - start) / opens * 1e6:8.1f} µs")

    rng = random.Random(1)
    with trak.open(args.file) as r:
        t = r.table("build")
        count = t.count()
        gets = 5000
        start = time.perf_counter()
        for _ in range(gets):
            t.get(rng.randint(1, count)).get("user")
        print(f"get(random n)      {(time.perf_counter() - start) / gets * 1e6:8.1f} µs")

        scans = 500
        start = time.perf_counter()
        for _ in range(scans):
            a = rng.randint(1, count - 40)
            for e in t.range(a, a + 39):
                e.items()
        print(f"sequential 40 rows {(time.perf_counter() - start) / scans * 1e6:8.1f} µs")


if __name__ == "__main__":
    main()
