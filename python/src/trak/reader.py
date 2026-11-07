"""Reading trak files: navigation, tables and events (SPEC §7, §8, §9, §11)."""

from __future__ import annotations

import builtins
import contextlib
import datetime as _dt
import mmap
import os
import re
from collections import OrderedDict
from collections.abc import Iterator
from typing import Any

from . import _footer, _time
from ._footer import Footer
from ._jump import jump
from ._values import LeafRec, MapNode, Node, Walk
from .errors import FormatError, SchemaError, TrakError, UnsupportedVersion
from .schema import Schema, build_schema

__all__ = ["Event", "Reader", "Table", "open"]

_MAGIC = re.compile(rb"trak (0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")
_CACHE_SIZE = 4096


class Reader:
    """A read-only view of a trak file. Use :func:`open`."""

    def __init__(self, path: str | os.PathLike[str]) -> None:
        self.path = os.fspath(path)
        self._file = builtins.open(self.path, "rb")  # noqa: SIM115
        self._map: mmap.mmap | None = None
        self._cache: OrderedDict[int, Footer] = OrderedDict()
        self._latest: dict[int, Footer] = {}
        #: Number of footer lookups so far, for navigation bound tests.
        self.footer_reads = 0
        try:
            self._load()
        except BaseException:
            self.close()
            raise

    # --- lifecycle ------------------------------------------------------------

    def _load(self) -> None:
        size = os.fstat(self._file.fileno()).st_size
        if size == 0:
            raise FormatError("empty file: missing magic line", 0)
        old = self._map
        self._map = mmap.mmap(self._file.fileno(), 0, access=mmap.ACCESS_READ)
        if old is not None:
            with contextlib.suppress(BufferError):
                old.close()
        buf = self._map
        nl = buf.find(b"\n", 0, 64)
        m = _MAGIC.fullmatch(buf[: max(nl, 0)]) if nl >= 0 else None
        if m is None:
            raise FormatError("bad magic line, expected 'trak MAJOR.MINOR'", 0)
        if m.group(1) != b"1":
            raise UnsupportedVersion(f"unsupported major version {m.group(1).decode()}", 5)
        self.version = f"{m.group(1).decode()}.{m.group(2).decode()}"
        self._magic_end = nl + 1
        self._size = size
        self._end, self._schema_footers = self._recover(size)
        self._latest = {}
        self._schemas = [
            build_schema(f.name, f.num, f.stamp, self._schema_text(f)) for f in self._schema_footers
        ]
        self._by_name = {s.name: s for s in self._schemas}

    def refresh(self) -> bool:
        """Pick up objects appended since open. Returns True when anything changed."""
        size = os.fstat(self._file.fileno()).st_size
        if size == self._size:
            return False
        old_end = self._end
        for end in [e for e in self._cache if e > old_end]:
            del self._cache[end]
        self._load()
        return self._end != old_end

    def close(self) -> None:
        if self._map is not None:
            with contextlib.suppress(BufferError):
                self._map.close()
            self._map = None
        self._file.close()

    def __enter__(self) -> Reader:
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    @property
    def end(self) -> int:
        """Offset just past the last valid object. Bytes after it are a torn tail."""
        return self._end

    @property
    def size(self) -> int:
        return self._size

    @property
    def buffer(self) -> mmap.mmap:
        if self._map is None:
            raise TrakError("reader is closed")
        return self._map

    # --- public API -------------------------------------------------------------

    def types(self) -> list[Schema]:
        """Every schema, in type index order."""
        return list(self._schemas)

    def schema(self, name: str) -> Schema:
        try:
            return self._by_name[name]
        except KeyError:
            raise KeyError(f"no type named {name!r}") from None

    def table(self, name: str) -> Table:
        return Table(self, self.schema(name))

    def __iter__(self) -> Iterator[Event]:
        """Every event in file order."""
        footers = []
        f = self._last()
        while f is not None:
            footers.append(f)
            f = self._prev(f)
        for f in reversed(footers):
            if f.is_schema:
                continue
            schema = self._by_name.get(f.name)
            if schema is None:
                raise FormatError(f"event of type {f.name!r} has no schema", f.fstart)
            yield Event(self, schema, f)

    # --- footers and navigation ---------------------------------------------------

    def _footer(self, end: int) -> Footer:
        self.footer_reads += 1
        f = self._cache.get(end)
        if f is not None:
            self._cache.move_to_end(end)
            return f
        buf = self.buffer
        if end <= self._magic_end or end > self._size or buf[end - 1] != 10:
            raise FormatError("pointer doesn't land on the end of an object", end)
        fstart = buf.rfind(b"\n", self._magic_end - 1, end - 1) + 1
        f = _footer.parse(buf, fstart, end, self._magic_end)
        self._cache[end] = f
        if len(self._cache) > _CACHE_SIZE:
            self._cache.popitem(last=False)
        return f

    def _last(self) -> Footer | None:
        return None if self._end == self._magic_end else self._footer(self._end)

    def _prev(self, f: Footer) -> Footer | None:
        return None if f.start == self._magic_end else self._footer(f.start)

    def _follow(self, f: Footer, pointer: int) -> Footer:
        if pointer == 0:
            raise FormatError("required pointer is 0", f.fstart)
        return self._footer(f.target(pointer))

    def _recover(self, size: int) -> tuple[int, list[Footer]]:
        """Find the end of the last valid footer (SPEC §11) and the schema chain it sees."""
        buf = self.buffer
        pos = size
        #: Why the last complete footer line in the ignored tail is invalid, if there is one.
        self.tail_error: FormatError | SchemaError | None = None
        if pos > self._magic_end and buf[pos - 1] != 10:
            pos = buf.rfind(b"\n", self._magic_end - 1, pos) + 1
        while pos > self._magic_end:
            fstart = buf.rfind(b"\n", self._magic_end - 1, pos - 1) + 1
            head = buf[fstart : fstart + 4]
            if head in (_footer.SCHEMA_OPEN, _footer.EVENT_OPEN):
                try:
                    return pos, self._validate_tail(pos)
                except (FormatError, SchemaError) as e:
                    self._cache.pop(pos, None)
                    if self.tail_error is None:
                        self.tail_error = e
            pos = fstart
        return self._magic_end, []

    def _validate_tail(self, end: int) -> list[Footer]:
        """Check that the footer ending at ``end`` is valid by SPEC §11: it parses, its type
        is known, and its VLQ count and walk match its schema. Returns the schema chain."""
        f = self._footer(end)
        chain = self._schema_chain(f)
        if not f.is_schema:
            s = next((c for c in chain if c.name == f.name), None)
            if s is None:
                raise FormatError(f"event of type {f.name!r} has no schema", f.fstart)
            schema = build_schema(s.name, s.num, s.stamp, self._schema_text(s))
            if schema.root is not None:
                Walk(schema.root, f.values, f.start, f.fstart)
        return chain

    def _schema_text(self, f: Footer) -> str:
        data = self.buffer[f.start : f.fstart]
        try:
            return data.decode("ascii")
        except UnicodeDecodeError:
            return data.decode("latin-1")  # parse fails on non-ASCII with a position

    def _schema_chain(self, f: Footer) -> list[Footer]:
        """Every schema footer visible from ``f``, in index order: walk back to a schema or an
        event with dir type 0, then follow previous-schema pointers (SPEC §7)."""
        g: Footer | None = f
        while g is not None and not g.is_schema:
            if g.vlqs[4] == 0:
                g = self._follow(g, g.vlqs[3])
                if not g.is_schema:
                    raise FormatError(
                        "directory pointer for the schema chain misses a schema", g.end
                    )
                break
            g = self._prev(g)
        if g is None:
            raise FormatError("event with no schema before it", f.fstart)
        chain = [g]
        while chain[-1].vlqs[1]:
            g = self._follow(chain[-1], chain[-1].vlqs[1])
            if not g.is_schema:
                raise FormatError("previous-schema pointer misses a schema", g.end)
            chain.append(g)
        chain.reverse()
        names: set[str] = set()
        for i, s in enumerate(chain, 1):
            if s.num != i:
                raise FormatError(f"schema {s.name} has index {s.num}, expected {i}", s.fstart)
            if s.name in names:
                raise FormatError(f"duplicate schema name {s.name}", s.fstart)
            names.add(s.name)
        return chain

    def _latest_footer(self, schema: Schema) -> Footer:
        """The latest node of a type: its last event, or its schema if it has none."""
        f = self._latest.get(schema.index)
        if f is not None:
            return f
        f = self._last()
        while f is not None:
            if f.name == schema.name:
                break
            if not f.is_schema and f.vlqs[4] == schema.index:
                f = self._follow(f, f.vlqs[3])
                if f.name != schema.name:
                    raise FormatError("directory pointer leads to the wrong type", f.end)
                break
            f = self._prev(f)
        if f is None:  # pragma: no cover - the schema itself is always found
            raise FormatError(f"type {schema.name} not found", self._end)
        self._latest[schema.index] = f
        return f

    def _navigate(self, f: Footer, m: int) -> Footer:
        """From node ``f`` walk down to node ``m`` of the same type (SPEC §7)."""
        name = f.name
        while f.node > m:
            j = jump(f.node)
            if j >= m:
                f2, want = self._follow(f, f.vlqs[2]), j
            else:
                f2, want = self._follow(f, f.vlqs[1]), f.node - 1
            if f2.name != name or f2.node != want:
                raise FormatError(
                    f"pointer leads to the wrong node (wanted {name} {want})", f.fstart
                )
            f = f2
        return f


class Table:
    """The events of one type, numbered from 1."""

    def __init__(self, reader: Reader, schema: Schema) -> None:
        self.reader = reader
        self.schema = schema

    @property
    def name(self) -> str:
        return self.schema.name

    def count(self) -> int:
        return self.reader._latest_footer(self.schema).node

    def __len__(self) -> int:
        return self.count()

    def latest(self) -> Event | None:
        f = self.reader._latest_footer(self.schema)
        return None if f.is_schema else Event(self.reader, self.schema, f)

    def _footer(self, n: int) -> Footer:
        top = self.reader._latest_footer(self.schema)
        if not 1 <= n <= top.node:
            raise IndexError(f"{self.name} has no event {n}")
        return self.reader._navigate(top, n)

    def get(self, n: int) -> Event:
        return Event(self.reader, self.schema, self._footer(n))

    def __getitem__(self, n: int) -> Event:
        return self.get(n)

    def range(self, a: int, b: int) -> Iterator[Event]:
        """Events ``a`` to ``b`` inclusive, descending when ``a > b``."""
        count = self.count()
        for x in (a, b):
            if not 1 <= x <= count:
                raise IndexError(f"{self.name} has no event {x}")
        r = self.reader
        if a > b:
            f: Footer | None = self._footer(a)
            while f is not None and f.node >= b:
                yield Event(r, self.schema, f)
                f = r._follow(f, f.vlqs[1]) if f.node > b else None
            return
        lo = a
        while lo <= b:
            hi = min(b, lo + 255)
            chunk = [self._footer(hi)]
            while chunk[-1].node > lo:
                g = chunk[-1]
                chunk.append(r._navigate(g, g.node - 1))
            for g in reversed(chunk):
                yield Event(r, self.schema, g)
            lo = hi + 1

    def __iter__(self) -> Iterator[Event]:
        count = self.count()
        return self.range(1, count) if count else iter(())

    def at_time(self, ts: str | _dt.datetime) -> Event | None:
        """The first event with timestamp ``>= ts``, or None."""
        want = _time.to_ms(ts)
        lo, hi = 1, self.count() + 1
        while lo < hi:
            mid = (lo + hi) // 2
            if _time.to_ms(self._footer(mid).stamp) >= want:
                hi = mid
            else:
                lo = mid + 1
        return None if lo > self.count() else self.get(lo)


class Event:
    """One event. Values are decoded lazily on first access."""

    def __init__(self, reader: Reader, schema: Schema, footer: Footer) -> None:
        self._reader = reader
        self.schema = schema
        self.footer = footer
        self._walk: Walk | None = None
        self._index: dict[bytes, LeafRec] | None = None

    type = property(lambda self: self.footer.name)
    n = property(lambda self: self.footer.num)
    timestamp = property(lambda self: self.footer.stamp)
    offset = property(lambda self: self.footer.start)

    @property
    def time(self) -> _dt.datetime:
        return _time.to_datetime(self.footer.stamp)

    def __repr__(self) -> str:
        return f"<trak.Event {self.type} {self.n} {self.timestamp}>"

    def walk(self) -> Walk:
        if self._walk is None:
            root = self.schema.root
            if root is None:
                assert self.schema.error is not None
                raise SchemaError(f"values of {self.type} are unavailable: {self.schema.error}")
            self._walk = Walk(root, self.footer.values, self.footer.start, self.footer.fstart)
        return self._walk

    def _path(self, rec: LeafRec) -> bytes:
        off = rec.path_off
        return bytes(self._reader.buffer[off : off + rec.plen])

    def _lookup(self, path: str | bytes) -> LeafRec | None:
        if self._index is None:
            index: dict[bytes, LeafRec] = {}
            for rec in self.walk().leaves:
                index.setdefault(self._path(rec), rec)
            self._index = index
        key = path.encode("utf-8", "surrogateescape") if isinstance(path, str) else path
        return self._index.get(key)

    def raw(self, path: str | bytes) -> memoryview | None:
        """A zero-copy view of a leaf's value bytes, or None when absent."""
        rec = self._lookup(path)
        if rec is None:
            return None
        return memoryview(self._reader.buffer)[rec.voff : rec.voff + rec.length]

    def get(self, path: str | bytes) -> str | None:
        """A leaf's value as text (``num`` as its decimal string), or None when absent.

        Paths are as written in the file, e.g. ``tags.1`` or ``env.region``. When map
        keys contain ``.``, two leaves can share a path; the first one wins.
        """
        rec = self._lookup(path)
        return None if rec is None else self._text(rec)

    def as_int(self, path: str | bytes) -> int | None:
        v = self.get(path)
        return None if v is None else int(v)

    def as_float(self, path: str | bytes) -> float | None:
        v = self.get(path)
        return None if v is None else float(v)

    def _text(self, rec: LeafRec) -> str:
        data = self._reader.buffer[rec.voff : rec.voff + rec.length]
        return data.decode("utf-8", "surrogateescape")

    def items(self) -> list[tuple[str, str]]:
        """Every present leaf as ``(path, value)``, in file order."""
        return [
            (self._path(r).decode("utf-8", "surrogateescape"), self._text(r))
            for r in self.walk().leaves
        ]

    def to_native(self) -> dict[str, Any]:
        """The whole value as a tree. Leaves are str, absent values None, and maps are lists
        of ``(key, value)`` pairs; a key is None when its entry has no leaves (SPEC §6)."""
        w = self.walk()
        out = self._native(w.tree, w)
        assert isinstance(out, dict)
        return out

    def _native(self, node: Node, w: Walk) -> Any:
        if node is None:
            return None
        if isinstance(node, int):
            return self._text(w.leaves[node])
        if isinstance(node, dict):
            return {k: self._native(v, w) for k, v in node.items()}
        if isinstance(node, list):
            return [self._native(v, w) for v in node]
        assert isinstance(node, MapNode)
        buf = self._reader.buffer
        out = []
        for klen, first, child in node.entries:
            key = None
            if first is not None:
                off = w.leaves[first].path_off + node.plen + 1
                key = buf[off : off + klen].decode("utf-8", "surrogateescape")
            out.append((key, self._native(child, w)))
        return out


def open(path: str | os.PathLike[str]) -> Reader:  # noqa: A001
    """Open a trak file for reading."""
    return Reader(path)
