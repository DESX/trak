"""File-order paging with cursors, write positions and directory sync."""

from __future__ import annotations

import os
import pathlib
import random

import pytest

import trak

STAMP = "20261107T140000.000Z"


def page_all(r: trak.Reader, forward: bool, limit: int) -> list[tuple[str, int]]:
    out: list[tuple[str, int]] = []
    cursor = None
    while True:
        page = r.after(cursor, limit) if forward else r.before(cursor, limit)
        if not page:
            return out
        assert len(page) <= limit
        out += [(e.type, e.n) for e in page]
        cursor = page[-1].offset


def mixed(path: pathlib.Path, n: int, seed: int = 5, types: int = 4) -> list[trak.Pos]:
    rng = random.Random(seed)
    pos = []
    with trak.create(path, overwrite=True) as w:
        for k in range(types):
            w.define(f"t{k}", "{ i: num }")
        for i in range(n):
            k = 0 if rng.random() < 0.6 else rng.randrange(types)
            pos.append(w.write(f"t{k}", {"i": i}, STAMP))
            if rng.random() < 0.01:
                w.define(f"late{i}", "{}")
    return pos


@pytest.mark.parametrize("n", [0, 1, 2, 57])
@pytest.mark.parametrize("limit", [1, 2, 5, 100])
def test_pages_cover_every_event_once(tmp_path: pathlib.Path, n: int, limit: int) -> None:
    p = tmp_path / "m.trak"
    mixed(p, n)
    with trak.open(p) as r:
        everything = [(e.type, e.n) for e in r]
        assert page_all(r, True, limit) == everything
        assert page_all(r, False, limit) == everything[::-1]


def test_write_returns_position(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "m.trak"
    pos = mixed(p, 40)
    with trak.open(p) as r:
        events = list(r)
        assert [(e.n, e.offset) for e in events] == [(x.n, x.offset) for x in pos]
        n, offset = pos[3]
        assert (n, offset) == (pos[3].n, pos[3].offset)
        # A write position is a cursor.
        assert r.after(pos[9].offset, 1)[0].offset == pos[10].offset
        assert r.before(pos[10].offset, 1)[0].offset == pos[9].offset


def test_cursor_edges(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "m.trak"
    pos = mixed(p, 10)
    with trak.open(p) as r:
        assert r.after(pos[-1].offset) == []
        assert r.before(pos[0].offset) == []
        assert r.after(None, 0) == []
        assert [e.offset for e in r.before(r.end, 2)] == [e.offset for e in r.before(None, 2)]
        assert len(r.after(len(b"trak 1.1\n"), 100)) == 10  # the magic line's end
        for bad in (-5, 3, pos[2].offset + 1, r.end + 1, r.size + 100):
            with pytest.raises(trak.ValueError, match="cursor"):
                r.after(bad)
            with pytest.raises(trak.ValueError, match="cursor"):
                r.before(bad)


def test_forged_footer_is_not_a_cursor(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "f.trak"
    forged = "x\n==[ t 1 20261107T140000.000Z ]==BBBBBAB\n"
    with trak.create(p) as w:
        w.define("t", "{ out: |str }")
        w.write("t", {"out": forged}, STAMP)
        w.write("t", {"out": "y"}, STAMP)
    data = p.read_bytes()
    inside = data.index(b"]==BBBBBAB\n") + len(b"]==BBBBBAB\n")
    with trak.open(p) as r:
        with pytest.raises(trak.ValueError):
            r.after(inside)
        assert [e.n for e in r.after(None)] == [1, 2]


def test_cursor_survives_refresh_and_torn_tail(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "m.trak"
    pos = mixed(p, 20)
    with trak.open(p) as r:
        cursor = pos[-1].offset
        assert r.after(cursor) == []
        with trak.open_append(p) as w:
            more = [w.write("t1", {"i": i}, STAMP) for i in range(3)]
        assert r.refresh()
        assert [e.offset for e in r.after(cursor)] == [x.offset for x in more]
        with open(p, "ab") as f:
            f.write(b"i: 9\n==[ t1 9 2026")
        assert r.refresh() is False
        assert [e.offset for e in r.after(cursor)] == [x.offset for x in more]
    # After truncation, a cursor past the new end is stale.
    os.truncate(p, more[1].offset)
    with trak.open(p) as r:
        with pytest.raises(trak.ValueError):
            r.after(more[2].offset)
        assert [e.offset for e in r.after(cursor)] == [more[0].offset]


def test_forward_page_reads_are_bounded(tmp_path: pathlib.Path) -> None:
    p = tmp_path / "big.trak"
    pos = mixed(p, 20000, types=6)
    with trak.open(p) as r:
        k = len(r.types())
        for start in (0, 5000, 19990):
            r._cache.clear()
            r._latest.clear()
            before = r.footer_reads
            page = r.after(pos[start].offset, 50)
            assert [e.offset for e in page] == [x.offset for x in pos[start + 1 : start + 51]]
            # Every type's latest node in one walk (~3K reads), a seek and a navigation in
            # each of the 6 types that have events (~6 log2 n each), then the walk back over
            # at most 6 * 50 objects, plus the schemas among them.
            assert r.footer_reads - before <= 3 * k + 6 * 2 * 6 * 15 + 6 * 50 + 10
        r._cache.clear()
        before = r.footer_reads
        r.before(pos[10000].offset, 50)
        assert r.footer_reads - before <= 50 + 5  # plus any schemas in between


def test_sync_create_syncs_directory(
    tmp_path: pathlib.Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    synced: list[int] = []
    real = os.fsync

    def fsync(fd: int) -> None:
        synced.append(fd)
        real(fd)

    monkeypatch.setattr(os, "fsync", fsync)
    with trak.create(tmp_path / "s.trak", sync=True) as w:
        w.define("t", "{}")
    # The magic line, the directory (not on Windows), then the schema.
    assert len(synced) == (2 if os.name == "nt" else 3)
    synced.clear()
    with trak.create(tmp_path / "n.trak") as w:
        w.define("t", "{}")
    assert synced == []
