"""Reading trak files: navigation, tables and events (SPEC §7, §8, §9, §11)."""

from __future__ import annotations

import builtins
import contextlib
import datetime as _dt
import heapq
import mmap
import os
import re
from collections import OrderedDict
from collections.abc import Callable, Iterator
from typing import Any

from . import _footer, _time
from ._footer import Footer
from ._jump import jump
from ._values import LeafRec, MapNode, Node, Walk
from .errors import FormatError, SchemaError, TrakError, UnsupportedVersion, ValueError
from .schema import Schema, build_schema

__all__ = ["Event", "Family", "Reader", "Table", "open"]

_BELOW = -(1 << 62)


def build_schemas(reader: Reader, footers: list[Footer]) -> list[Schema]:
    """Parse schema objects in index order, so that children find their parents."""
    out: list[Schema] = []
    by_name: dict[str, Schema] = {}
    for f in footers:
        s = build_schema(f.name, f.num, f.stamp, reader._schema_text(f), types=by_name)
        out.append(s)
        by_name[s.name] = s
    return out


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
        self._schemas = build_schemas(self, self._schema_footers)
        self._by_name = {s.name: s for s in self._schemas}
        # Where each type's first child schema ends: from there on, its events carry their
        # number in its own family (SPEC §6).
        self._child_from: dict[str, int] = {}
        for s, f in zip(self._schemas, self._schema_footers, strict=True):
            if s.parent is not None and s.parent not in self._child_from:
                self._child_from[s.parent] = f.end

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

    def family(self, name: str) -> Family:
        """The events of a type and all its descendants, in file order (SPEC §1)."""
        return Family(self, self.schema(name))

    def before(self, cursor: int | None = None, limit: int = 100) -> list[Event]:
        """Up to ``limit`` events that start before ``cursor``, newest first.

        A cursor is an event's ``offset``, or None for the end of the file. Pass the last
        event returned to get the next page. An empty list means there are no more.
        """
        c = self._end if cursor is None else self._cursor(cursor)
        out: list[Event] = []
        f = None if c == self._magic_end else self._footer(c)
        while f is not None and len(out) < limit:
            if not f.is_schema:
                out.append(self._event(f))
            f = self._prev(f)
        return out

    def after(self, cursor: int | None = None, limit: int = 100) -> list[Event]:
        """Up to ``limit`` events that start after ``cursor``, oldest first.

        A cursor is an event's ``offset``, or None for the start of the file. Pass the last
        event returned to get the next page. An empty list means there are no more.
        """
        c = -1 if cursor is None else self._cursor(cursor)
        if limit <= 0:
            return []
        # The objects after c can't be found by scanning forward, since logged content may
        # look like footers. Instead find, per type, its first event after c by seeking on
        # offsets, then walk back from a point that has at least `limit` events before it.
        bound = self._end
        self._latest_all()
        for s in self._schemas:
            top = self._latest_footer(s)
            if top.is_schema:
                continue
            first = self._seek(s, _start, c + 1)
            if first is not None and first.node + limit - 1 <= top.node:
                bound = min(bound, self._navigate(top, first.node + limit - 1).end)
        found: list[Footer] = []
        f = None if bound == self._magic_end else self._footer(bound)
        while f is not None and f.start > c:
            if not f.is_schema:
                found.append(f)
            f = self._prev(f)
        return [self._event(g) for g in reversed(found[-limit:])]

    def _cursor(self, c: int) -> int:
        """Check that ``c`` is where an object starts (SPEC §11's footer test)."""
        if c == self._magic_end or c == self._end:
            return c
        if not self._magic_end < c < self._end:
            raise ValueError(f"cursor {c} is outside the file's valid objects")
        try:
            f = self._footer(c)
            schema = self._by_name.get(f.name)
            if schema is None:
                raise FormatError(f"unknown type {f.name!r}", f.fstart)
            if not f.is_schema and schema.root is not None:
                Walk(schema.root, f.values, f.start, f.fstart)
        except FormatError:
            raise ValueError(f"cursor {c} is not the start of an object") from None
        return c

    def _event(self, f: Footer) -> Event:
        schema = self._by_name.get(f.name)
        if schema is None:
            raise FormatError(f"event of type {f.name!r} has no schema", f.fstart)
        return Event(self, schema, f)

    def __iter__(self) -> Iterator[Event]:
        """Every event in file order."""
        footers = []
        f = self._last()
        while f is not None:
            footers.append(f)
            f = self._prev(f)
        for f in reversed(footers):
            if not f.is_schema:
                yield self._event(f)

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
            if not any(c.name == f.name for c in chain):
                raise FormatError(f"event of type {f.name!r} has no schema", f.fstart)
            schema = next(s for s in build_schemas(self, chain) if s.name == f.name)
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

    def _hop(self, f: Footer, jump_: bool) -> Footer:
        """Follow ``f``'s jump or step pointer, checking that it lands on the right node."""
        want = jump(f.node) if jump_ else f.node - 1
        g = self._follow(f, f.vlqs[2] if jump_ else f.vlqs[1])
        if g.name != f.name or g.node != want:
            raise FormatError(f"pointer leads to the wrong node (wanted {f.name} {want})", f.fstart)
        return g

    def _seek(self, schema: Schema, key: Callable[[Footer], int], m: int) -> Footer | None:
        """The first event of a type whose key is ``>= m``, for a key that never decreases
        along the chain (SPEC §7, Seek). None when there is none."""
        f = self._latest_footer(schema)
        if f.is_schema or key(f) < m:
            return None
        while f.node > 1:
            if jump(f.node) > 0:
                g = self._hop(f, True)
                if key(g) >= m:
                    f = g
                    continue
            g = self._hop(f, False)
            if key(g) < m:
                break
            f = g
        return f

    def family_number(self, f: Footer, family: str) -> int:
        """An event's number in one of its families (SPEC §6, Family numbers)."""
        schema = self._by_name[f.name]
        level = schema.lineage.index(family)
        if level == len(schema.lineage) - 1:
            child_from = self._child_from.get(f.name)
            if child_from is None or child_from > f.start:
                return f.num
        if level >= f.vlqs[5]:
            raise FormatError(f"{f.name} event has no number in family {family}", f.fstart)
        return f.vlqs[6 + level]

    def _latest_all(self) -> dict[int, Footer]:
        """The latest node of every type, in one walk back: each event names its own type and
        points at its dir type's latest node, so about 2K events cover every type (SPEC §7)."""
        latest = self._latest
        want = len(self._schemas)
        f = self._last()
        while f is not None and len(latest) < want:
            s = self._by_name.get(f.name)
            if s is not None and s.index not in latest:
                latest[s.index] = f
            if not f.is_schema:
                dt = f.vlqs[4]
                if dt and dt not in latest and dt <= want:
                    g = self._follow(f, f.vlqs[3])
                    if g.name != self._schemas[dt - 1].name:
                        raise FormatError("directory pointer leads to the wrong type", g.end)
                    latest[dt] = g
            f = self._prev(f)
        return latest

    def _navigate(self, f: Footer, m: int) -> Footer:
        """From node ``f`` walk down to node ``m`` of the same type (SPEC §7)."""
        while f.node > m:
            f = self._hop(f, jump(f.node) >= m)
        return f


def _start(f: Footer) -> int:
    return f.start if not f.is_schema else _BELOW


def _stamp_ms(f: Footer) -> int:
    return _time.to_ms(f.stamp) if not f.is_schema else _BELOW


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
        f = self.reader._seek(self.schema, _stamp_ms, _time.to_ms(ts))
        return None if f is None else Event(self.reader, self.schema, f)


class Family:
    """The events of a type and its descendants, numbered from 1 in file order (SPEC §1)."""

    def __init__(self, reader: Reader, schema: Schema) -> None:
        self.reader = reader
        self.schema = schema
        #: The family's types: the type itself first, then its descendants in index order.
        self.types = [s for s in reader._schemas if schema.name in s.lineage]

    @property
    def name(self) -> str:
        return self.schema.name

    def _key(self, f: Footer) -> int:
        return self.reader.family_number(f, self.name) if not f.is_schema else _BELOW

    def _latest(self) -> list[Footer]:
        if len(self.types) > 1:
            self.reader._latest_all()
        tops = (self.reader._latest_footer(s) for s in self.types)
        return [f for f in tops if not f.is_schema]

    def count(self) -> int:
        return max((self._key(f) for f in self._latest()), default=0)

    def __len__(self) -> int:
        return self.count()

    def latest(self) -> Event | None:
        tops = self._latest()
        if not tops:
            return None
        return self.reader._event(max(tops, key=self._key))

    def get(self, m: int) -> Event:
        """Row ``m``: a seek in each of the family's types, O(k log n)."""
        if m >= 1:
            for s in self.types:
                f = self.reader._seek(s, self._key, m)
                if f is not None and self._key(f) == m:
                    return Event(self.reader, s, f)
        raise IndexError(f"family {self.name} has no row {m}")

    def __getitem__(self, m: int) -> Event:
        return self.get(m)

    def range(self, a: int, b: int) -> Iterator[Event]:
        """Rows ``a`` to ``b`` inclusive, descending when ``a > b``: one range per type,
        merged by family number."""
        count = self.count()
        for x in (a, b):
            if not 1 <= x <= count:
                raise IndexError(f"family {self.name} has no row {x}")
        lo, hi = min(a, b), max(a, b)
        runs = []
        for s in self.types:
            first = self.reader._seek(s, self._key, lo)
            if first is None or self._key(first) > hi:
                continue
            after = self.reader._seek(s, self._key, hi + 1)
            last = (after.node if after is not None else Table(self.reader, s).count() + 1) - 1
            t = Table(self.reader, s)
            run = t.range(first.node, last) if a <= b else t.range(last, first.node)
            runs.append((self._key(e.footer), e) for e in run)
        merged = heapq.merge(*runs, key=lambda p: p[0], reverse=a > b)
        return (e for _, e in merged)

    def __iter__(self) -> Iterator[Event]:
        count = self.count()
        return self.range(1, count) if count else iter(())

    def at_time(self, ts: str | _dt.datetime) -> Event | None:
        """The first row with timestamp ``>= ts``, or None. Timestamps never decrease within a
        family, so it's the earliest of each type's first such event."""
        want = _time.to_ms(ts)
        found = [f for s in self.types if (f := self.reader._seek(s, _stamp_ms, want)) is not None]
        return None if not found else self.reader._event(min(found, key=self._key))


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

    def prev(self) -> Event | None:
        """The previous event of the same type, in one read, or None for event 1."""
        if self.footer.num == 1:
            return None
        return Event(self._reader, self.schema, self._reader._hop(self.footer, False))

    def family_number(self, family: str | None = None) -> int:
        """This event's row number in ``family``: its own type or an ancestor. The default
        is the root family."""
        name = self.schema.lineage[0] if family is None else family
        if name not in self.schema.lineage:
            raise KeyError(f"{self.type} is not in family {name!r}")
        return self._reader.family_number(self.footer, name)

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
