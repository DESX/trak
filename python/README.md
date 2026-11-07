# trak-log

The Python library for [trak](https://github.com/buk/trak), a log format that
programs write and read, and people can read too.

```sh
pip install trak-log
```

```python
import trak

with trak.create("builds.trak") as w:
    w.define("build", "{ user: str, status: enum(ok, failed), duration: num, stdout: |str }")
    w.write("build", {"user": "ana", "status": "ok", "duration": 910, "stdout": "Done.\n"})

with trak.open("builds.trak") as r:
    builds = r.table("build")
    print(builds.count(), builds.latest().get("user"))
```

The library has no dependencies and needs Python 3.10 or later.

The format is specified in
[SPEC.md](https://github.com/buk/trak/blob/main/SPEC.md), and the API is
documented in [docs/api.md](https://github.com/buk/trak/blob/main/docs/api.md).
