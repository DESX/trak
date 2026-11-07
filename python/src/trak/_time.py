"""Timestamps: ``YYYYMMDDTHHMMSS.sssZ`` in UTC, handled as integer milliseconds."""

from __future__ import annotations

import datetime as _dt
import re

_EPOCH = _dt.datetime(1970, 1, 1, tzinfo=_dt.timezone.utc)
_PATTERN = re.compile(r"([0-9]{4})([0-9]{2})([0-9]{2})T([0-9]{2})([0-9]{2})([0-9]{2})\.([0-9]{3})Z")


def to_ms(value: str | _dt.datetime) -> int:
    """Convert a trak timestamp string or an aware datetime to milliseconds since the epoch."""
    if isinstance(value, str):
        m = _PATTERN.fullmatch(value)
        if m is None:
            raise ValueError(f"bad timestamp {value!r}, expected YYYYMMDDTHHMMSS.sssZ")
        y, mo, d, h, mi, s, ms = (int(g) for g in m.groups())
        value = _dt.datetime(y, mo, d, h, mi, s, ms * 1000, tzinfo=_dt.timezone.utc)
    if not isinstance(value, _dt.datetime):
        raise TypeError("timestamp must be a str or datetime")
    if value.tzinfo is None:
        raise ValueError("naive datetime: pass an aware datetime (e.g. tzinfo=timezone.utc)")
    delta = value - _EPOCH
    return (delta.days * 86_400 + delta.seconds) * 1000 + delta.microseconds // 1000


def from_ms(ms: int) -> str:
    """Format milliseconds since the epoch as a trak timestamp."""
    t = _EPOCH + _dt.timedelta(milliseconds=ms)
    return (
        f"{t.year:04d}{t.month:02d}{t.day:02d}T"
        f"{t.hour:02d}{t.minute:02d}{t.second:02d}.{t.microsecond // 1000:03d}Z"
    )


def to_datetime(stamp: str) -> _dt.datetime:
    return _EPOCH + _dt.timedelta(milliseconds=to_ms(stamp))


def valid(stamp: str) -> bool:
    try:
        to_ms(stamp)
    except ValueError:
        return False
    return True
