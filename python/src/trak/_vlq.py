"""Letter VLQ: base-26 numbers written in letters (SPEC §4)."""

from __future__ import annotations

from .errors import FormatError

MAX = (1 << 64) - 1

_UPPER = b"ABCDEFGHIJKLMNOPQRSTUVWXYZ"
_LOWER = b"abcdefghijklmnopqrstuvwxyz"
_SMALL = [bytes([c]) for c in _UPPER]


def encode(n: int) -> bytes:
    """Encode one unsigned value."""
    if n < 26:
        if n < 0:
            raise OverflowError("VLQ values are unsigned")
        return _SMALL[n]
    if n > MAX:
        raise OverflowError("VLQ values are at most 64 bits")
    out = bytearray()
    out.append(_UPPER[n % 26])
    n //= 26
    while n:
        out.append(_LOWER[n % 26])
        n //= 26
    out.reverse()
    return bytes(out)


def encode_all(values: list[int]) -> bytes:
    """Encode a sequence of values, concatenated."""
    return b"".join([_SMALL[v] if 0 <= v < 26 else encode(v) for v in values])


def decode(data: bytes, offset: int = 0) -> list[int]:
    """Decode concatenated VLQs. ``offset`` locates ``data`` in the file for error messages."""
    out: list[int] = []
    v = 0
    started = False
    for i, c in enumerate(data):
        if 65 <= c <= 90:
            v = v * 26 + (c - 65)
            if v > MAX:
                raise FormatError("VLQ overflows 64 bits", offset + i)
            out.append(v)
            v = 0
            started = False
        elif 97 <= c <= 122:
            if not started and c == 97:
                raise FormatError("VLQ has a leading zero digit", offset + i)
            v = v * 26 + (c - 97)
            if v > MAX:
                raise FormatError("VLQ overflows 64 bits", offset + i)
            started = True
        else:
            raise FormatError(f"bad VLQ letter {chr(c)!r}", offset + i)
    if started:
        raise FormatError("VLQ ends without a final capital letter", offset + len(data))
    return out
