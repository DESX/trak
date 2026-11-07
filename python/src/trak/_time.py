"""Timestamps: ``YYYYMMDDTHHMMSS.sssZ`` in UTC, handled as integer milliseconds.

Conversions use plain integer arithmetic (Howard Hinnant's civil-date algorithms)
rather than ``datetime``, since they sit on the write path.
"""

from __future__ import annotations

import datetime as _dt

_EPOCH = _dt.datetime(1970, 1, 1, tzinfo=_dt.timezone.utc)
_MONTH_DAYS = (31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31)
_DIGITS = frozenset("0123456789")


def _days_from_civil(y: int, m: int, d: int) -> int:
    y -= m <= 2
    era = y // 400
    yoe = y - era * 400
    doy = (153 * (m - 3 if m > 2 else m + 9) + 2) // 5 + d - 1
    doe = yoe * 365 + yoe // 4 - yoe // 100 + doy
    return era * 146097 + doe - 719468


def _civil_from_days(z: int) -> tuple[int, int, int]:
    z += 719468
    era = z // 146097
    doe = z - era * 146097
    yoe = (doe - doe // 1460 + doe // 36524 - doe // 146096) // 365
    doy = doe - (365 * yoe + yoe // 4 - yoe // 100)
    mp = (5 * doy + 2) // 153
    d = doy - (153 * mp + 2) // 5 + 1
    m = mp + 3 if mp < 10 else mp - 9
    return yoe + era * 400 + (m <= 2), m, d


def parse(stamp: str) -> int:
    """Parse a trak timestamp to milliseconds; raises ValueError."""
    if (
        len(stamp) != 20
        or stamp[8] != "T"
        or stamp[15] != "."
        or stamp[19] != "Z"
        or not _DIGITS.issuperset(stamp[:8] + stamp[9:15] + stamp[16:19])
    ):
        raise ValueError(f"bad timestamp {stamp!r}, expected YYYYMMDDTHHMMSS.sssZ")
    y, mo, d = int(stamp[:4]), int(stamp[4:6]), int(stamp[6:8])
    h, mi, s, ms = int(stamp[9:11]), int(stamp[11:13]), int(stamp[13:15]), int(stamp[16:19])
    leap = y % 4 == 0 and (y % 100 != 0 or y % 400 == 0)
    if (
        y == 0
        or not 1 <= mo <= 12
        or not 1 <= d <= _MONTH_DAYS[mo - 1] + (mo == 2 and leap)
        or h > 23
        or mi > 59
        or s > 59
    ):
        raise ValueError(f"bad timestamp {stamp!r}: not a real time")
    return ((_days_from_civil(y, mo, d) * 24 + h) * 60 + mi) * 60_000 + s * 1000 + ms


def to_ms(value: str | _dt.datetime) -> int:
    """Convert a trak timestamp string or an aware datetime to milliseconds since the epoch."""
    if isinstance(value, str):
        return parse(value)
    if not isinstance(value, _dt.datetime):
        raise TypeError("timestamp must be a str or datetime")
    if value.tzinfo is None:
        raise ValueError("naive datetime: pass an aware datetime (e.g. tzinfo=timezone.utc)")
    delta = value - _EPOCH
    return (delta.days * 86_400 + delta.seconds) * 1000 + delta.microseconds // 1000


def from_ms(ms: int) -> str:
    """Format milliseconds since the epoch as a trak timestamp."""
    days, rem = divmod(ms, 86_400_000)
    y, m, d = _civil_from_days(days)
    return (
        f"{y:04d}{m:02d}{d:02d}T{rem // 3_600_000:02d}{rem // 60_000 % 60:02d}"
        f"{rem // 1000 % 60:02d}.{rem % 1000:03d}Z"
    )


def to_datetime(stamp: str) -> _dt.datetime:
    return _EPOCH + _dt.timedelta(milliseconds=parse(stamp))
