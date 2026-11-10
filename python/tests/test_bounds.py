"""Navigation read-count bounds (handover §9) on a large generated file.

The file has 1M events when TRAK_SLOW=1 (as in CI) and 200k otherwise. ``n`` in
``3·log2(n) + 4`` is the number of events in the table.
"""

from __future__ import annotations

import math
import os
import pathlib
import random

import pytest

import trak

N = 1_000_000 if os.environ.get("TRAK_SLOW") else 200_000
K = 8


@pytest.fixture(scope="module")
def big(tmp_path_factory: pytest.TempPathFactory) -> pathlib.Path:
    p = tmp_path_factory.mktemp("bounds") / "big.trak"
    rng = random.Random(7)
    with trak.create(p) as w:
        for k in range(K):
            w.define(f"t{k}", "{ i: num }")
        for i in range(N):
            # Type 0 gets half the events; type 7 is rare.
            t = 0 if rng.random() < 0.5 else 7 if rng.random() < 0.001 else rng.randrange(1, K - 1)
            w.write(f"t{t}", {"i": i})
    return p


def reads(r: trak.Reader, fn: object) -> int:
    assert callable(fn)
    before = r.footer_reads
    fn()
    return r.footer_reads - before


def test_open_reads_about_k_footers(big: pathlib.Path) -> None:
    with trak.open(big) as r:
        assert r.footer_reads <= 2 * K + 4


def test_count_and_latest(big: pathlib.Path) -> None:
    with trak.open(big) as r:
        total = 0
        for s in r.types():
            r._cache.clear()
            r._latest.clear()
            t = r.table(s.name)
            assert reads(r, t.count) <= 2 * K + 4
            total += t.count()
            r._cache.clear()
            r._latest.clear()
            assert reads(r, t.latest) <= 2 * K + 4
        assert total == N


def test_get_reads_log_n_footers(big: pathlib.Path) -> None:
    rng = random.Random(1)
    with trak.open(big) as r:
        for s in r.types():
            t = r.table(s.name)
            n = t.count()
            bound = 3 * math.log2(n) + 4
            for m in {1, 2, n // 2, n - 1, n, *(rng.randint(1, n) for _ in range(200))}:
                if not 1 <= m <= n:
                    continue
                r._cache.clear()
                assert reads(r, lambda m=m, t=t: t.get(m)) <= bound, (s.name, m)


FAMILY_N = N // 4
KIDS = 5


@pytest.fixture(scope="module")
def family(tmp_path_factory: pytest.TempPathFactory) -> pathlib.Path:
    p = tmp_path_factory.mktemp("bounds") / "family.trak"
    rng = random.Random(8)
    with trak.create(p) as w:
        w.define("root", "{ i: num }")
        for k in range(KIDS):
            w.define(f"root.k{k}", "{}")
        w.define("other", "{ i: num }")
        for i in range(FAMILY_N):
            pick = rng.randrange(KIDS + 2)
            name = "root" if pick == KIDS else "other" if pick == KIDS + 1 else f"root.k{pick}"
            w.write(name, {"i": i}, f"20261107T{i // 3600000 % 24:02d}0000.000Z")
    return p


def test_family_row_reads_k_log_n(family: pathlib.Path) -> None:
    rng = random.Random(2)
    with trak.open(family) as r:
        f = r.family("root")
        rows = f.count()
        k = len(f.types)
        # A seek reads at most two footers per step of the walk to that node.
        bound = k * (2 * (3 * math.log2(rows) + 4))
        for m in {1, rows, *(rng.randint(1, rows) for _ in range(100))}:
            r._cache.clear()
            assert reads(r, lambda m=m: f.get(m)) <= bound, m


def test_seek_reads_log_n(big: pathlib.Path) -> None:
    with trak.open(big) as r:
        t = r.table("t0")
        n = t.count()
        for ts in ("20000101T000000.000Z", "20991231T000000.000Z"):
            r._cache.clear()
            assert reads(r, lambda ts=ts: t.at_time(ts)) <= 2 * (3 * math.log2(n) + 4)
        rng = random.Random(4)
        for _ in range(50):
            target = t.get(rng.randint(1, n)).offset
            r._cache.clear()
            before = r.footer_reads
            found = r._seek(t.schema, lambda f: f.start, target)
            assert r.footer_reads - before <= 2 * (3 * math.log2(n) + 4)
            assert found is not None and found.start == target
