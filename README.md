# trak

**trak is a log format that programs write and read, and people can read too.**

A trak file is plain text you can scroll, scan and grep. Each event type is a table:
one row per event, one column per field. A program can reach any row in O(log n)
reads, and logged content can never be mistaken for structure, because every
boundary comes from lengths in footers. Values are copied as they are, never
escaped, so a multi-line compiler log costs nothing extra.

```
trak 1.1
// One CI build run.
{
  user: str,                  // who triggered the build
  status: enum(ok, failed),
  duration: num @ms,          // wall-clock time
  stdout: |str @log,          // compiler output
}
##[ build 1 1.0 ]##iAAA
user: joel
status: failed
duration: 1520
Compiling core...
ld: undefined symbol main
--[ stdout ]--
==[ build 1 20261107T140322.415Z ]==dWBBBAAEGEbR
```

The lines starting `##[` and `==[` are footers. They close a schema and an event,
and their trailing letters are lengths and pointers. The format is specified in
[SPEC.md](SPEC.md).

Related types form a **family**: `build.step` is a child of `build`, inherits its
fields, and adds its own. Each event records its row in every family it belongs
to, so a family is one table in one guaranteed order, and any row is a few seeks
away.

This repository holds the spec, reference libraries in Python, Go and C++, a
JavaScript reader, and a web viewer. All four libraries pass the same
[conformance suite](conformance), and the three writers produce identical bytes.

## Python

```sh
pip install trak-log
```

<!-- readme-test: python -->
```python
import trak

with trak.create("builds.trak", overwrite=True) as w:
    w.define("build", """
// One CI build run.
{
  user: str,                // who triggered the build
  status: enum(ok, failed),
  duration: num @ms,
  stdout: |str @log,
}""")
    w.write("build", {"user": "ana", "status": "ok", "duration": 910, "stdout": "Done.\n"})
    w.write("build", {"user": "joel", "status": "failed", "duration": 1520, "stdout": "ld: error\n"})

with trak.open("builds.trak") as r:
    builds = r.table("build")
    print(builds.count(), "builds")              # 2 builds
    for e in builds.range(1, builds.count()):
        print(e.n, e.get("user"), e.get("status"), e.as_int("duration"))
```

## Go

```sh
go get github.com/desx/trak/go
```

<!-- readme-test: go -->
```go
package main

import (
	"fmt"
	"log"

	trak "github.com/desx/trak/go"
)

func main() {
	w, err := trak.Create("builds.trak", trak.Options{Overwrite: true})
	if err != nil {
		log.Fatal(err)
	}
	w.Define("build", "{ user: str, status: enum(ok, failed), duration: num }")
	w.Write("build", map[string]any{"user": "ana", "status": "ok", "duration": 910})
	w.Write("build", map[string]any{"user": "joel", "status": "failed", "duration": 1520})
	w.Close()

	r, err := trak.Open("builds.trak")
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close()
	builds, _ := r.Table("build")
	latest, _ := builds.Latest()
	user, _, _ := latest.Get("user")
	fmt.Println("latest build by", user) // latest build by joel
}
```

## C++

C++17, CMake 3.20 or later, no dependencies. Add it with FetchContent and link
`trak::trak`:

```cmake
FetchContent_Declare(trak GIT_REPOSITORY https://github.com/desx/trak GIT_TAG v1.1.0 SOURCE_SUBDIR cpp)
FetchContent_MakeAvailable(trak)
target_link_libraries(app PRIVATE trak::trak)
```

<!-- readme-test: cpp -->
```cpp
#include <iostream>
#include <trak/trak.hpp>

int main() {
  {
    auto w = trak::Writer::create("builds.trak", {/*overwrite=*/true});
    w.define("build", "{ user: str, status: enum(ok, failed), duration: num }");
    w.write("build", trak::Value::dict({{"user", "ana"}, {"status", "ok"}, {"duration", 910}}));
  }
  auto r = trak::Reader::open("builds.trak");
  auto builds = r.table("build");
  std::cout << builds.count() << " build by " << *builds.get(1).get("user") << "\n";  // 1 build by ana
}
```

## JavaScript

A reader for browsers and Node.js, with no dependencies. It reads through byte
sources: a dropped `File`, HTTP Range requests, or bytes in memory. See
[js/README.md](js/README.md).

```js
import { Reader, HttpSource } from "./js/src/index.js";

const r = await Reader.open(await HttpSource.open("/files/builds.trak"));
const latest = await r.table("build").latest();
console.log(await latest.get("user"));
```

## Command line

The `trak` command is part of the Go module:

```sh
go install github.com/desx/trak/go/cmd/trak@latest
```

<!-- readme-test: cli -->
```sh
trak types builds.trak                    # types, event counts, descriptions
trak schema builds.trak build             # a type's definition and columns
trak cat builds.trak build --last 20      # events as an aligned table
trak export builds.trak --format jsonl    # one JSON object per event
trak tail -f builds.trak                  # follow a growing file
trak check builds.trak                    # validate every byte
```

`trak cat` shows a type as a table:

```
N  TIMESTAMP             user  status  duration  stdout
1  20261107T140210.120Z  ana   ok      910       ▸ 1 line
2  20261107T140322.415Z  joel  failed  1520      ▸ 1 line
```

## Viewer

```sh
trak serve ~/logs                         # then open http://127.0.0.1:8765/
```

`trak serve DIR` serves a web viewer for the `.trak` files under `DIR`. It reads
only the byte ranges it shows, so a multi-GB file over a remote connection opens
in a few reads. It shows the file as a log in file order, or any type or family
as a table: nested objects get grouped headers, and blocks, lists and maps expand
on demand. You can also drop a file on the page, which reads it in the browser.
The table is a widget other pages can embed; see [viewer/](viewer).

## Documentation

| Document | Contents |
|---|---|
| [SPEC.md](SPEC.md) | The format. It is normative. |
| [docs/api.md](docs/api.md) | The shared library API, with notes for each language |
| [docs/cli.md](docs/cli.md) | The `trak` command |
| [docs/DECISIONS.md](docs/DECISIONS.md) | Spec clarifications and implementation choices |
| [js/README.md](js/README.md) | The JavaScript reader |
| [conformance/](conformance) | Test vectors every implementation must pass |
| [bench/](bench) | Benchmarks |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Building and testing each library |

## License

MIT, see [LICENSE](LICENSE).
