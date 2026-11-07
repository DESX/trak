"""Typed errors raised by the trak library."""

from __future__ import annotations

import builtins

__all__ = ["FormatError", "SchemaError", "TrakError", "UnsupportedVersion", "ValueError"]


class TrakError(Exception):
    """Base class for every error raised by trak."""


class FormatError(TrakError):
    """The file's bytes don't follow the format. ``offset`` is where the problem was found."""

    def __init__(self, message: str, offset: int | None = None) -> None:
        self.message = message
        self.offset = offset
        super().__init__(message if offset is None else f"{message} (at byte {offset})")


class UnsupportedVersion(FormatError):
    """The file's major version is not 1."""


class SchemaError(TrakError):
    """A schema definition is invalid. ``line`` and ``column`` are 1-based when known."""

    def __init__(self, message: str, line: int | None = None, column: int | None = None) -> None:
        self.message = message
        self.line = line
        self.column = column
        where = "" if line is None else f" (line {line}, column {column})"
        super().__init__(message + where)


class ValueError(TrakError, builtins.ValueError):  # noqa: A001
    """A value doesn't fit its schema: a bad ``num`` or ``enum``, a missing field, or similar."""
