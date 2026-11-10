"""Writing trak files (SPEC §10, plus the reference writer behavior in docs/api.md)."""

from __future__ import annotations

import builtins
import datetime as _dt
import os
import time
from typing import Any, NamedTuple

from . import _footer, _time
from ._footer import Footer
from ._jump import jump
from ._lock import lock
from ._values import encode
from .errors import FormatError, SchemaError, TrakError, ValueError
from .reader import Reader
from .schema import Schema, build_schema, valid_type_name

__all__ = ["Pos", "Writer", "create", "open_append"]

MAGIC = b"trak 1.1\n"
#: Schema footers declare the oldest version their definition needs: 1.1 for a child type.
VERSION = "1.0"
CHILD_VERSION = "1.1"
_UNSET = -(1 << 62)


class Pos(NamedTuple):
    """Where an event was written: its number in its type, and its object's start offset
    (the same as ``Event.offset``, usable as a reader cursor)."""

    n: int
    offset: int


class _Type:
    __slots__ = ("schema", "n", "spine", "last_ms", "lineage", "family_count", "has_child")

    def __init__(self, schema: Schema, spine: list[tuple[int, int]], lineage: list[_Type]) -> None:
        self.schema = schema
        self.n = spine[-1][0]
        # (node, end offset) for node n, J(n), J(J(n)), ..., 0; latest last.
        self.spine = spine
        # The latest timestamp in this type's family; only the root's is used.
        self.last_ms = _UNSET
        # The type's families, root first, ending with this type.
        self.lineage = [*lineage, self]
        # Events in this type's family so far.
        self.family_count = 0
        self.has_child = False


class Writer:
    """Appends schemas and events to a trak file. Use :func:`create` or :func:`open_append`."""

    def __init__(self, path: str | os.PathLike[str], *, append: bool, overwrite: bool, sync: bool):
        self.path = os.fspath(path)
        self.sync = sync
        self._types: dict[str, _Type] = {}
        self._order: list[_Type] = []
        self._last_schema_end = 0
        self._cov: list[int] = [-1]
        self._seq = 0
        flags = os.O_RDWR | getattr(os, "O_BINARY", 0)
        if append:
            self._fd = os.open(self.path, flags, 0o666)
        elif overwrite:
            self._fd = os.open(self.path, flags | os.O_CREAT, 0o666)
        else:
            self._fd = os.open(self.path, flags | os.O_CREAT | os.O_EXCL, 0o666)
        try:
            lock(self._fd)
            if append and os.fstat(self._fd).st_size > 0:
                self._offset = self._restore()
            else:
                os.ftruncate(self._fd, 0)
                self._offset = 0
                self._write(MAGIC)
                if sync:
                    _sync_dir(self.path)
        except BaseException:
            os.close(self._fd)
            raise

    # --- lifecycle ------------------------------------------------------------

    def flush(self) -> None:
        """Objects are handed to the OS as they're written; flush makes them durable."""
        os.fsync(self._fd)

    def close(self) -> None:
        if self._fd >= 0:
            os.close(self._fd)
            self._fd = -1

    @property
    def closed(self) -> bool:
        return self._fd < 0

    def __enter__(self) -> Writer:
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def _write(self, data: bytes) -> None:
        if self._fd < 0:
            raise TrakError("writer is closed")
        os.lseek(self._fd, self._offset, os.SEEK_SET)
        view = memoryview(data)
        while view:
            n = os.write(self._fd, view)
            view = view[n:]
        self._offset += len(data)
        if self.sync:
            os.fsync(self._fd)

    # --- public API -------------------------------------------------------------

    def schema(self, name: str) -> Schema:
        return self._types[name].schema

    def types(self) -> list[Schema]:
        return [t.schema for t in self._order]

    def define(self, name: str, definition: str) -> Schema:
        """Parse and validate a definition, then write its schema object."""
        if not valid_type_name(name):
            raise SchemaError(f"bad type name {name!r}")
        if name.startswith("trak."):
            raise SchemaError("type names starting with 'trak.' are reserved")
        if name in self._types:
            raise SchemaError(f"type {name!r} is already defined")
        parent: _Type | None = None
        if "." in name:
            parent = self._types.get(name.rsplit(".", 1)[0])
            if parent is None:
                raise SchemaError(f"define {name.rsplit('.', 1)[0]!r} before its child {name!r}")
        version = VERSION if parent is None else CHILD_VERSION
        if not definition.endswith("\n"):
            definition += "\n"
        index = len(self._order) + 1
        types = {n: t.schema for n, t in self._types.items()}
        schema = build_schema(name, index, version, definition, strict=True, types=types)
        start = self._offset
        body = definition.encode("ascii")
        prev = 0 if not self._order else start - self._last_schema_end + 1
        footer = _footer.schema_line(name, index, version, [len(body), prev, 0])
        self._write(body + footer)
        self._last_schema_end = self._offset
        state = _Type(schema, [(0, self._offset)], [] if parent is None else parent.lineage)
        if parent is not None:
            parent.has_child = True
        self._types[name] = state
        self._order.append(state)
        self._cov.append(-1)
        return schema

    def write(self, type: str, value: Any, timestamp: str | _dt.datetime | None = None) -> Pos:
        """Write one event. Returns its number ``n`` and offset."""
        try:
            t = self._types[type]
        except KeyError:
            raise ValueError(f"type {type!r} is not defined") from None
        if t.schema.root is None:
            raise SchemaError(f"can't write {type}: {t.schema.error}")
        root = t.lineage[0]
        stamp = None
        if timestamp is None:
            ms = max(time.time_ns() // 1_000_000, root.last_ms)
        else:
            try:
                ms = _time.to_ms(timestamp)
            except (TypeError, builtins.ValueError) as e:
                raise ValueError(str(e)) from None
            if isinstance(timestamp, str):
                stamp = timestamp  # valid stamps are canonical, so reuse the text
            if ms < root.last_ms:
                raise ValueError(
                    f"timestamp {_time.from_ms(ms)} is earlier than the last event in"
                    f" family {root.schema.name}"
                )
        body, values = encode(t.schema.root, value)
        n = t.n + 1
        j = jump(n)
        start = self._offset
        step_end = t.spine[-1][1]
        jump_end = next(end for node, end in reversed(t.spine) if node == j)
        index = t.schema.index
        cov = self._cov
        best = min((cov[i], i) for i in range(len(cov)) if i != index)[1]
        dir_end = self._last_schema_end if best == 0 else self._order[best - 1].spine[-1][1]
        vlqs = [
            len(body),
            start - step_end + 1,
            start - jump_end + 1,
            start - dir_end + 1,
            best,
        ]
        # Family numbers: one per ancestor, then the type's own once it has a child.
        families = t.lineage if t.has_child else t.lineage[:-1]
        vlqs.append(len(families))
        vlqs.extend(a.family_count + 1 for a in families)
        vlqs.extend(values)
        if stamp is None:
            stamp = _time.from_ms(ms)
        self._write(body + _footer.event_line(type, n, stamp, vlqs))
        while t.spine[-1][0] > j:
            t.spine.pop()
        t.spine.append((n, self._offset))
        t.n = n
        root.last_ms = ms
        for a in t.lineage:
            a.family_count += 1
        cov[index] = cov[best] = self._seq
        self._seq += 1
        return Pos(n, start)

    # --- reopening ----------------------------------------------------------------

    def _restore(self) -> int:
        with Reader(self.path) as r:
            end = r.end
            for sf in r._schema_footers:
                schema = r.schema(sf.name)
                if schema.parent is not None:
                    parent = self._types[schema.parent]
                    parent.has_child = True
                    lineage = parent.lineage
                else:
                    lineage = []
                self._order.append(_Type(schema, [(0, sf.end)], lineage))
                self._types[sf.name] = self._order[-1]
                self._last_schema_end = sf.end
            for t in self._order:
                top = r._latest_footer(t.schema)
                spine = [(top.node, top.end)]
                node = top
                while node.node > 0:
                    node = r._navigate(node, jump(node.node))
                    spine.append((node.node, node.end))
                spine.reverse()
                t.spine = spine
                t.n = top.node
                if not top.is_schema:
                    # A family's latest event has its latest timestamp and largest numbers.
                    for a in t.lineage:
                        a.family_count = max(a.family_count, r.family_number(top, a.schema.name))
                    root = t.lineage[0]
                    root.last_ms = max(root.last_ms, _time.to_ms(top.stamp))
            # Rebuild coverage approximately from the last ~2K events.
            cov = [_UNSET] * (len(self._order) + 1)
            f: Footer | None = r._last()
            seq = -1
            budget = 2 * len(self._order) + 2
            while f is not None and budget > 0:
                if not f.is_schema:
                    for i in (self._types[f.name].schema.index, f.vlqs[4]):
                        if i < len(cov) and cov[i] == _UNSET:
                            cov[i] = seq
                    seq -= 1
                    budget -= 1
                f = r._prev(f)
            self._cov = cov
        if end < os.fstat(self._fd).st_size:
            os.ftruncate(self._fd, end)
        if end < len(MAGIC):  # pragma: no cover - Reader rejects this earlier
            raise FormatError("file has no magic line", 0)
        return end


def _sync_dir(path: str) -> None:
    """Make a new file's directory entry durable. Windows has no directory fsync."""
    if os.name == "nt":  # pragma: no cover
        return
    fd = os.open(os.path.dirname(os.path.abspath(path)), os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def create(path: str | os.PathLike[str], *, overwrite: bool = False, sync: bool = False) -> Writer:
    """Create a new trak file. Fails if it exists, unless ``overwrite``."""
    return Writer(path, append=False, overwrite=overwrite, sync=sync)


def open_append(path: str | os.PathLike[str], *, sync: bool = False) -> Writer:
    """Open an existing trak file for appending, truncating any torn tail."""
    return Writer(path, append=True, overwrite=False, sync=sync)
