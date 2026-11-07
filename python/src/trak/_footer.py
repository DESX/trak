"""Footer lines: parsing and formatting (SPEC §5, §6, §8)."""

from __future__ import annotations

import mmap
import re
from dataclasses import dataclass

from . import _vlq
from .errors import FormatError

SCHEMA_OPEN = b"##[ "
SCHEMA_CLOSE = b" ]##"
EVENT_OPEN = b"==[ "
EVENT_CLOSE = b" ]=="

_DECIMAL = re.compile(rb"[1-9][0-9]*")
_TIMESTAMP = re.compile(rb"[0-9]{8}T[0-9]{6}\.[0-9]{3}Z")


@dataclass(frozen=True, slots=True)
class Footer:
    """A parsed footer. ``start`` is the object's first byte, ``fstart`` the footer's first
    byte and ``end`` the byte after the footer's LF."""

    is_schema: bool
    start: int
    fstart: int
    end: int
    name: str
    num: int  # schema: type index; event: n
    stamp: str  # schema: version; event: timestamp
    vlqs: tuple[int, ...]

    @property
    def node(self) -> int:
        """Node number in its type's chain: 0 for the schema, n for an event."""
        return 0 if self.is_schema else self.num

    @property
    def extensions(self) -> int:
        return self.vlqs[2] if self.is_schema else self.vlqs[5]

    @property
    def values(self) -> tuple[int, ...]:
        """Event value VLQs, after the extensions."""
        return self.vlqs[6 + self.vlqs[5] :]

    def target(self, pointer: int) -> int:
        """Resolve a pointer stored in this footer to the target object's end offset."""
        return self.start - (pointer - 1)


def parse(buf: bytes | memoryview | mmap.mmap, fstart: int, end: int, min_start: int) -> Footer:
    """Parse the footer line ``buf[fstart:end]`` (including its LF)."""
    line = bytes(buf[fstart : end - 1])
    head = line[:4]
    if head == SCHEMA_OPEN:
        is_schema, close = True, SCHEMA_CLOSE
    elif head == EVENT_OPEN:
        is_schema, close = False, EVENT_CLOSE
    else:
        raise FormatError("line is not a footer", fstart)
    k = line.find(close, 4)
    if k < 0:
        raise FormatError("footer has no closing marker", fstart)
    parts = line[4:k].split(b" ")
    if len(parts) != 3 or not all(parts):
        raise FormatError("footer needs exactly three fields", fstart + 4)
    letters = line[k + 4 :]
    if not letters:
        raise FormatError("footer has no VLQs", fstart + k + 4)
    vlqs = _vlq.decode(letters, fstart + k + 4)
    name_b, num_b, stamp_b = parts
    try:
        name = name_b.decode("ascii")
    except UnicodeDecodeError:
        raise FormatError("type name is not ASCII", fstart + 4) from None
    if _DECIMAL.fullmatch(num_b) is None:
        raise FormatError("bad decimal number in footer", fstart + 5 + len(name_b))
    num = int(num_b)
    stamp_at = fstart + 6 + len(name_b) + len(num_b)
    if is_schema:
        if re.fullmatch(rb"[0-9]+\.[0-9]+", stamp_b) is None:
            raise FormatError("bad version in schema footer", stamp_at)
        if len(vlqs) < 3 or len(vlqs) != 3 + vlqs[2]:
            raise FormatError("schema footer has the wrong number of VLQs", fstart + k + 4)
    else:
        if _TIMESTAMP.fullmatch(stamp_b) is None:
            raise FormatError("bad timestamp in event footer", stamp_at)
        if len(vlqs) < 6 or len(vlqs) < 6 + vlqs[5]:
            raise FormatError("event footer has too few VLQs", fstart + k + 4)
    start = fstart - vlqs[0]
    if start < min_start:
        raise FormatError("body length reaches past the first object", fstart + k + 4)
    return Footer(is_schema, start, fstart, end, name, num, stamp_b.decode("ascii"), tuple(vlqs))


def schema_line(name: str, index: int, version: str, vlqs: list[int]) -> bytes:
    return b"".join(
        (
            SCHEMA_OPEN,
            f"{name} {index} {version}".encode(),
            SCHEMA_CLOSE,
            _vlq.encode_all(vlqs),
            b"\n",
        )
    )


def event_line(name: str, n: int, timestamp: str, vlqs: list[int]) -> bytes:
    return b"".join(
        (EVENT_OPEN, f"{name} {n} {timestamp}".encode(), EVENT_CLOSE, _vlq.encode_all(vlqs), b"\n")
    )
