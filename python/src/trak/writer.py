"""Writing trak files (SPEC §10, plus the reference writer behavior in docs/api.md)."""

from __future__ import annotations

import builtins
import datetime as _dt
import os
import time
from typing import Any

from . import _footer, _time
from ._footer import Footer
from ._jump import jump
from ._lock import lock
from ._values import encode
from .errors import FormatError, SchemaError, TrakError, ValueError
from .reader import Reader
from .schema import Schema, build_schema, valid_type_name

__all__ = ["Writer", "create", "open_append"]

MAGIC = b"trak 1.0\n"
VERSION = "1.0"
_UNSET = -(1 << 62)


class _Type:
    __slots__ = ("schema", "n", "spine", "last_ms")

    def __init__(self, schema: Schema, spine: list[tuple[int, int]], last_ms: int) -> None:
        self.schema = schema
        self.n = spine[-1][0]
        # (node, end offset) for node n, J(n), J(J(n)), ..., 0; latest last.
        self.spine = spine
        self.last_ms = last_ms


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
            self._fd = os.open(self.path, flags)
        elif overwrite:
            self._fd = os.open(self.path, flags | os.O_CREAT)
        else:
            self._fd = os.open(self.path, flags | os.O_CREAT | os.O_EXCL)
        try:
            lock(self._fd)
            if append and os.fstat(self._fd).st_size > 0:
                self._offset = self._restore()
            else:
                os.ftruncate(self._fd, 0)
                self._offset = 0
                self._write(MAGIC)
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
        if not definition.endswith("\n"):
            definition += "\n"
        index = len(self._order) + 1
        schema = build_schema(name, index, VERSION, definition, strict=True)
        start = self._offset
        body = definition.encode("ascii")
        prev = 0 if not self._order else start - self._last_schema_end + 1
        footer = _footer.schema_line(name, index, VERSION, [len(body), prev, 0])
        self._write(body + footer)
        self._last_schema_end = self._offset
        state = _Type(schema, [(0, self._offset)], _UNSET)
        self._types[name] = state
        self._order.append(state)
        self._cov.append(-1)
        return schema

    def write(self, type: str, value: Any, timestamp: str | _dt.datetime | None = None) -> int:
        """Write one event and return its number ``n``."""
        try:
            t = self._types[type]
        except KeyError:
            raise ValueError(f"type {type!r} is not defined") from None
        if t.schema.root is None:
            raise SchemaError(f"can't write {type}: {t.schema.error}")
        if timestamp is None:
            ms = max(time.time_ns() // 1_000_000, t.last_ms)
        else:
            try:
                ms = _time.to_ms(timestamp)
            except (TypeError, builtins.ValueError) as e:
                raise ValueError(str(e)) from None
            if ms < t.last_ms:
                raise ValueError(
                    f"timestamp {_time.from_ms(ms)} is earlier than {type}'s last event"
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
            0,
        ]
        vlqs.extend(values)
        self._write(body + _footer.event_line(type, n, _time.from_ms(ms), vlqs))
        while t.spine[-1][0] > j:
            t.spine.pop()
        t.spine.append((n, self._offset))
        t.n = n
        t.last_ms = ms
        cov[index] = cov[best] = self._seq
        self._seq += 1
        return n

    # --- reopening ----------------------------------------------------------------

    def _restore(self) -> int:
        with Reader(self.path) as r:
            end = r.end
            for sf in r._schema_footers:
                schema = r.schema(sf.name)
                self._order.append(_Type(schema, [(0, sf.end)], _UNSET))
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
                    t.last_ms = _time.to_ms(top.stamp)
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


def create(path: str | os.PathLike[str], *, overwrite: bool = False, sync: bool = False) -> Writer:
    """Create a new trak file. Fails if it exists, unless ``overwrite``."""
    return Writer(path, append=False, overwrite=overwrite, sync=sync)


def open_append(path: str | os.PathLike[str], *, sync: bool = False) -> Writer:
    """Open an existing trak file for appending, truncating any torn tail."""
    return Writer(path, append=True, overwrite=False, sync=sync)
