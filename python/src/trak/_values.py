"""Event values: encoding a native tree into a body and VLQs, and walking it back (SPEC §6, §8)."""

from __future__ import annotations

import dataclasses
import math
import re
from collections.abc import Mapping, Sequence
from decimal import Decimal
from typing import Any, TypeAlias

from .errors import FormatError, SchemaError, ValueError
from .schema import Leaf, List, Map, Object, Type

NUM = re.compile(rb"-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?")

_BYTES = (bytes, bytearray, memoryview)


# --- encoding ---------------------------------------------------------------


def encode(root: Object, value: Any) -> tuple[bytes, list[int]]:
    """Return the body bytes and value VLQs for ``value``."""
    body = bytearray()
    vlqs: list[int] = []
    _encode_object(root, value, b"", body, vlqs)
    return bytes(body), vlqs


def _show(path: bytes) -> str:
    return path.decode("utf-8", "backslashreplace") or "<root>"


def _members(value: Any, path: bytes) -> Mapping[str, Any]:
    if isinstance(value, Mapping):
        return value
    if dataclasses.is_dataclass(value) and not isinstance(value, type):
        return {f.name: getattr(value, f.name) for f in dataclasses.fields(value)}
    raise ValueError(f"{_show(path)}: expected a mapping, got {type(value).__name__}")


def _encode_object(t: Object, value: Any, path: bytes, body: bytearray, vlqs: list[int]) -> None:
    members = _members(value, path)
    known = {m.key for m in t.members}
    for k in members:
        if k not in known:
            raise ValueError(f"{_show(path)}: unknown field {k!r}")
    for m in t.members:
        key = m.key.encode("ascii")
        _encode(m.type, members.get(m.key), path + b"." + key if path else key, body, vlqs)


def _encode(t: Type, value: Any, path: bytes, body: bytearray, vlqs: list[int]) -> None:
    if value is None:
        if not t.optional:
            raise ValueError(f"{_show(path)}: value is required")
        vlqs.append(0)
        return
    if t.optional:
        vlqs.append(1)
    if isinstance(t, Leaf):
        data = _leaf_bytes(t, value, path)
        vlqs.append(len(data))
        if t.block:
            body += data
            body += b"\n--[ "
            body += path
            body += b" ]--\n"
        else:
            body += path
            body += b": "
            body += data
            body += b"\n"
    elif isinstance(t, Object):
        _encode_object(t, value, path, body, vlqs)
    elif isinstance(t, List):
        if isinstance(value, (str, *_BYTES, Mapping)) or not isinstance(value, Sequence):
            raise ValueError(f"{_show(path)}: expected a list, got {type(value).__name__}")
        vlqs.append(len(value))
        for i, item in enumerate(value):
            _encode(t.elem, item, b"%b.%d" % (path, i), body, vlqs)
    elif isinstance(t, Map):
        entries = _entries(value, path)
        vlqs.append(len(entries))
        seen: set[bytes] = set()
        for k, v in entries:
            if k in seen:
                raise ValueError(f"{_show(path)}: repeated map key {_show(k)!r}")
            seen.add(k)
            vlqs.append(len(k))
            _encode(t.value, v, path + b"." + k, body, vlqs)
    else:  # pragma: no cover
        raise SchemaError(f"unknown type node {t!r}")


def _key(k: Any, path: bytes) -> bytes:
    if isinstance(k, str):
        return k.encode("utf-8", "surrogateescape")
    if isinstance(k, _BYTES):
        return bytes(k)
    raise ValueError(f"{_show(path)}: map keys must be str or bytes")


def _entries(value: Any, path: bytes) -> list[tuple[bytes, Any]]:
    if isinstance(value, Mapping):
        return [(_key(k, path), v) for k, v in value.items()]
    if isinstance(value, Sequence) and not isinstance(value, (str, *_BYTES)):
        out = []
        for pair in value:
            if not isinstance(pair, Sequence) or len(pair) != 2:
                raise ValueError(f"{_show(path)}: map entries must be (key, value) pairs")
            out.append((_key(pair[0], path), pair[1]))
        return out
    raise ValueError(f"{_show(path)}: expected a map, got {type(value).__name__}")


def _leaf_bytes(t: Leaf, value: Any, path: bytes) -> bytes:
    if t.kind == "str":
        if isinstance(value, str):
            return value.encode("utf-8", "surrogateescape")
        if isinstance(value, _BYTES):
            return bytes(value)
        raise ValueError(f"{_show(path)}: str needs str or bytes, got {type(value).__name__}")
    if t.kind == "num":
        return format_num(value, path)
    if isinstance(value, str):
        data = value.encode("utf-8", "surrogateescape")
    elif isinstance(value, _BYTES):
        data = bytes(value)
    else:
        raise ValueError(f"{_show(path)}: enum needs a str, got {type(value).__name__}")
    if data.decode("ascii", "replace") not in t.words:
        raise ValueError(f"{_show(path)}: {_show(data)!r} is not one of {', '.join(t.words)}")
    return data


def format_num(value: Any, path: bytes = b"") -> bytes:
    """Format a native number as ``num`` bytes, or validate a decimal string."""
    if isinstance(value, bool):
        raise ValueError(f"{_show(path)}: num can't be a bool")
    if isinstance(value, int):
        return str(value).encode("ascii")
    if isinstance(value, float):
        if not math.isfinite(value):
            raise ValueError(f"{_show(path)}: num must be finite")
        data = repr(value).encode("ascii")
    elif isinstance(value, Decimal):
        data = str(value).encode("ascii")
    elif isinstance(value, str):
        data = value.encode("utf-8", "surrogateescape")
    elif isinstance(value, _BYTES):
        data = bytes(value)
    else:
        raise ValueError(f"{_show(path)}: num needs a number or a decimal string")
    if NUM.fullmatch(data) is None:
        raise ValueError(f"{_show(path)}: {_show(data)!r} is not a valid num")
    return data


# --- decoding -----------------------------------------------------------------

# A decoded tree mirrors the schema. Leaves are indexes into ``Walk.leaves``, absent
# values are None, objects are dicts, lists are lists and maps are MapNode.


MAX_EMPTY_ELEMENTS = 1 << 20


def zero_size(t: Type) -> bool:
    """True when a value of type ``t`` writes no VLQs and no bytes: a required empty object."""
    return isinstance(t, Object) and not t.optional and all(zero_size(m.type) for m in t.members)


def empty_value(t: Type) -> dict[str, Any]:
    assert isinstance(t, Object)
    return {m.key: empty_value(m.type) for m in t.members}


class MapNode:
    __slots__ = ("plen", "entries")

    def __init__(self, plen: int) -> None:
        self.plen = plen
        # (key byte length, index of the entry's first leaf or None, value node)
        self.entries: list[tuple[int, int | None, Node]] = []


Node: TypeAlias = "int | None | dict[str, Any] | list[Any] | MapNode"


class LeafRec:
    """Where one leaf sits in the body: ``start`` of its bytes, ``voff`` of its value."""

    __slots__ = ("leaf", "start", "voff", "length", "plen")

    def __init__(self, leaf: Leaf, start: int, voff: int, length: int, plen: int) -> None:
        self.leaf = leaf
        self.start = start
        self.voff = voff
        self.length = length
        self.plen = plen

    @property
    def path_off(self) -> int:
        if self.leaf.block:
            return self.voff + self.length + 5
        return self.start


class Walk:
    """Walk an event's VLQs over its schema, computing every leaf's offsets (SPEC §8)."""

    __slots__ = ("vals", "vi", "off", "end", "leaves", "tree")

    def __init__(self, root: Object, vals: tuple[int, ...], start: int, end: int) -> None:
        self.vals = vals
        self.vi = 0
        self.off = start
        self.end = end
        self.leaves: list[LeafRec] = []
        self.tree = self._object(root, -1)
        if self.vi != len(vals):
            raise FormatError("event has more value VLQs than its schema uses", end)
        if self.off != end:
            raise FormatError("value walk doesn't end at the footer", self.off)

    def _next(self) -> int:
        if self.vi >= len(self.vals):
            raise FormatError("event has fewer value VLQs than its schema needs", self.end)
        v = self.vals[self.vi]
        self.vi += 1
        return v

    def _object(self, t: Object, plen: int) -> dict[str, Any]:
        return {m.key: self._node(m.type, plen + 1 + len(m.key)) for m in t.members}

    def _node(self, t: Type, plen: int) -> Node:
        if t.optional:
            flag = self._next()
            if flag == 0:
                return None
            if flag != 1:
                raise FormatError("optional flag must be 0 or 1", self.end)
        if isinstance(t, Leaf):
            length = self._next()
            start = self.off
            if t.block:
                voff = start
                self.off = start + length + plen + 10
            else:
                voff = start + plen + 2
                self.off = voff + length + 1
            if self.off > self.end:
                raise FormatError("value walk runs past the footer", start)
            self.leaves.append(LeafRec(t, start, voff, length, plen))
            return len(self.leaves) - 1
        if isinstance(t, Object):
            return self._object(t, plen)
        if isinstance(t, List):
            count = self._next()
            if zero_size(t.elem):
                if count > MAX_EMPTY_ELEMENTS:
                    raise FormatError("list of empty objects is too long to decode", self.end)
                return [empty_value(t.elem) for _ in range(count)]
            if count > len(self.vals):
                raise FormatError("list count exceeds the footer's VLQs", self.end)
            return [self._node(t.elem, plen + 1 + len(str(i))) for i in range(count)]
        if isinstance(t, Map):
            count = self._next()
            if count > len(self.vals):
                raise FormatError("map count exceeds the footer's VLQs", self.end)
            node = MapNode(plen)
            for _ in range(count):
                klen = self._next()
                first = len(self.leaves)
                child = self._node(t.value, plen + 1 + klen)
                node.entries.append((klen, first if len(self.leaves) > first else None, child))
            return node
        raise SchemaError(f"unknown type node {t!r}")  # pragma: no cover
