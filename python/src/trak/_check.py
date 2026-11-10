"""Full validation of a trak file, beyond what readers need (``trak check``)."""

from __future__ import annotations

import os
from typing import Any

from . import _time
from ._footer import Footer
from ._jump import jump
from ._values import NUM, LeafRec, MapNode, Node, Walk
from .errors import FormatError, SchemaError
from .reader import Reader
from .schema import (
    Leaf,
    List,
    Map,
    Object,
    Schema,
    Type,
    build_schema,
    parent_name,
    valid_type_name,
)


class _TypeState:
    __slots__ = ("schema", "ends", "last_ms", "family_count", "has_child")

    def __init__(self, schema: Schema, end: int) -> None:
        self.schema = schema
        self.ends = [end]  # end offset of node 0, 1, 2, ...
        self.last_ms = -(1 << 62)  # the family's latest timestamp, kept on its root
        self.family_count = 0
        self.has_child = False


def check(path: str | os.PathLike[str]) -> int:
    """Validate a whole file. Returns the number of objects; raises FormatError on the first
    problem, with its byte offset.

    A torn tail (SPEC §11) is allowed, but only when it holds no complete footer line: an
    invalid footer after the last valid one is reported rather than ignored.
    """
    with Reader(path) as r:
        if r.tail_error is not None:
            e = r.tail_error
            if isinstance(e, FormatError):
                raise FormatError(f"ignored tail has an invalid footer: {e.message}", e.offset)
            raise FormatError(f"ignored tail has an invalid footer: {e}", r.end)
        return _Checker(r).run()


class _Checker:
    def __init__(self, reader: Reader) -> None:
        self.r = reader
        self.buf = reader.buffer
        self.types: dict[str, _TypeState] = {}
        self.order: list[_TypeState] = []
        self.last_schema_end = 0

    def run(self) -> int:
        footers: list[Footer] = []
        f = self.r._last()
        while f is not None:
            footers.append(f)
            f = self.r._prev(f)
        footers.reverse()
        for f in footers:
            if f.is_schema:
                self.schema(f)
            else:
                self.event(f)
        return len(footers)

    def pointer(self, f: Footer, value: int, target_end: int, what: str) -> None:
        want = f.start - target_end + 1
        if value != want:
            raise FormatError(f"{what} pointer is {value}, expected {want}", f.fstart)

    def schema(self, f: Footer) -> None:
        if not valid_type_name(f.name):
            raise FormatError(f"bad type name {f.name!r}", f.fstart)
        if f.name in self.types:
            raise FormatError(f"duplicate schema name {f.name!r}", f.fstart)
        if f.num != len(self.order) + 1:
            raise FormatError(f"schema index {f.num}, expected {len(self.order) + 1}", f.fstart)
        if not self.order:
            if f.vlqs[1] != 0:
                raise FormatError("first schema's previous-schema pointer must be 0", f.fstart)
        else:
            self.pointer(f, f.vlqs[1], self.last_schema_end, "previous-schema")
        body = bytes(self.buf[f.start : f.fstart])
        if not body.endswith(b"\n"):
            raise FormatError("schema body must end with LF", f.fstart)
        major, minor = f.stamp.split(".")
        if major != "1":
            raise FormatError(f"unsupported schema version {f.stamp}", f.fstart)
        # 1.0 and 1.1 are parsed strictly; newer minors leniently (DECISIONS I5).
        strict = minor in ("0", "1")
        parent = parent_name(f.name, f.stamp)
        if parent is not None:
            p = self.types.get(parent)
            if p is None:
                raise FormatError(f"schema {f.name} comes before its parent {parent}", f.fstart)
            p.has_child = True
            if p.schema.root is None:
                strict = False  # the child's object can't be known either
        try:
            text = body.decode("ascii")
        except UnicodeDecodeError as e:
            raise FormatError("schema is not ASCII", f.start + e.start) from None
        types = {n: t.schema for n, t in self.types.items()}
        try:
            schema = build_schema(f.name, f.num, f.stamp, text, strict=strict, types=types)
        except SchemaError as e:
            raise FormatError(f"schema {f.name}: {e}", f.start) from None
        state = _TypeState(schema, f.end)
        self.types[f.name] = state
        self.order.append(state)
        self.last_schema_end = f.end

    def event(self, f: Footer) -> None:
        t = self.types.get(f.name)
        if t is None:
            raise FormatError(f"event of type {f.name!r} before its schema", f.fstart)
        n = len(t.ends)
        if f.num != n:
            raise FormatError(f"{f.name} event number {f.num}, expected {n}", f.fstart)
        try:
            ms = _time.to_ms(f.stamp)
        except ValueError:
            raise FormatError(f"invalid timestamp {f.stamp}", f.fstart) from None
        lineage = [self.types[a] for a in t.schema.lineage]
        family = lineage[0]
        if ms < family.last_ms:
            what = f"family {family.schema.name}" if family.has_child else f.name
            raise FormatError(f"{what} timestamp goes backwards", f.fstart)
        levels = lineage if t.has_child else lineage[:-1]
        want = [a.family_count + 1 for a in levels]
        got = list(f.vlqs[6 : 6 + min(f.vlqs[5], len(want))])
        if got != want:
            raise FormatError(f"{f.name} family numbers are {got}, expected {want}", f.fstart)
        self.pointer(f, f.vlqs[1], t.ends[n - 1], "step")
        self.pointer(f, f.vlqs[2], t.ends[jump(n)], "jump")
        dt = f.vlqs[4]
        if dt > len(self.order):
            raise FormatError(f"dir type {dt} is not defined yet", f.fstart)
        dir_end = self.last_schema_end if dt == 0 else self.order[dt - 1].ends[-1]
        self.pointer(f, f.vlqs[3], dir_end, "dir")
        root = t.schema.root
        if root is not None:
            walk = Walk(root, f.values, f.start, f.fstart)
            self.framing(root, walk.tree, b"", walk, -1)
        t.ends.append(f.end)
        family.last_ms = ms
        for a in lineage:
            a.family_count += 1

    def framing(self, t: Type, node: Node, path: bytes, walk: Walk, plen: int) -> None:
        """Check the bytes around each value: paths, separators and block labels."""
        if node is None:
            return
        if isinstance(t, Leaf):
            assert isinstance(node, int)
            self.leaf(walk.leaves[node], path)
        elif isinstance(t, Object):
            assert isinstance(node, dict)
            for m in t.members:
                key = m.key.encode("ascii")
                self.framing(m.type, node[m.key], path + b"." + key if path else key, walk, 0)
        elif isinstance(t, List):
            assert isinstance(node, list)
            for i, item in enumerate(node):
                self.framing(t.elem, item, b"%b.%d" % (path, i), walk, 0)
        elif isinstance(t, Map):
            assert isinstance(node, MapNode)
            for klen, first, child in node.entries:
                if first is None:
                    continue
                off = walk.leaves[first].path_off + len(path) + 1
                key = bytes(self.buf[off : off + klen])
                self.framing(t.value, child, path + b"." + key, walk, 0)

    def leaf(self, rec: LeafRec, path: bytes) -> None:
        buf = self.buf
        end = rec.voff + rec.length
        if rec.leaf.block:
            label = b"\n--[ " + path + b" ]--\n"
            if bytes(buf[end : end + len(label)]) != label:
                raise FormatError(f"block label for {path!r} doesn't match", end)
        else:
            head = path + b": "
            if bytes(buf[rec.start : rec.start + len(head)]) != head:
                raise FormatError(f"inline leaf doesn't start with {head!r}", rec.start)
            if buf[end] != 10:
                raise FormatError("inline leaf doesn't end with LF", end)
        value: Any = bytes(buf[rec.voff : end])
        leaf = rec.leaf
        if leaf.unknown is None and leaf.kind == "num" and NUM.fullmatch(value) is None:
            raise FormatError(f"bad num value {value!r}", rec.voff)
        if leaf.kind == "enum" and value.decode("ascii", "replace") not in leaf.words:
            raise FormatError(f"bad enum value {value!r}", rec.voff)
