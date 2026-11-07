"""Reference library for trak, a log format that programs write and read, and people can read too.

Write::

    with trak.create("app.trak") as w:
        w.define("build", "{ user: str, duration: num }")
        w.write("build", {"user": "ana", "duration": 910})

Read::

    with trak.open("app.trak") as r:
        builds = r.table("build")
        print(builds.count(), builds.latest().get("user"))
"""

from ._check import check
from .errors import FormatError, SchemaError, TrakError, UnsupportedVersion, ValueError
from .reader import Event, Reader, Table, open
from .schema import Annotation, Field, Schema
from .writer import Writer, create, open_append

__version__ = "1.0.0"

__all__ = [
    "Annotation",
    "Event",
    "Field",
    "FormatError",
    "Reader",
    "Schema",
    "SchemaError",
    "Table",
    "TrakError",
    "UnsupportedVersion",
    "ValueError",
    "Writer",
    "check",
    "create",
    "open",
    "open_append",
]
