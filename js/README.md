# trak for JavaScript

The JavaScript reader for [trak](../README.md). It runs in browsers and Node.js 18+, has
no dependencies, and passes the shared [conformance suite](../conformance) like the
Python, Go and C++ libraries. It reads only; there is no writer yet.

Every read goes through a byte source, so the same code reads a file the user dropped on a
page, a file served over HTTP with Range requests, or bytes in memory. Nothing loads the
whole file: opening reads about 2K footers, and each row costs its footer plus one read.

```js
import { Reader, HttpSource } from "./src/index.js";

const r = await Reader.open(await HttpSource.open("/files/builds.trak"));
const builds = r.table("build");
const latest = await builds.latest();
console.log(await builds.count(), await latest.get("user"));
for await (const e of r.family("build").range(1, 10)) console.log(e.type, await e.native());
```

| Source | For |
|---|---|
| `BytesSource(bytes)` | a `Uint8Array` in memory |
| `BlobSource(file)` | a browser `File` or `Blob` |
| `await HttpSource.open(url)` | a URL that answers `HEAD` and `Range` requests |
| `await FileSource.open(path)` (`./src/node.js`) | a local file in Node.js |

The API follows [docs/api.md](../docs/api.md), with `async` methods and camelCase names:
`r.types()`, `r.table(name)`, `r.family(name)`, `r.before(cursor, limit)`,
`r.after(cursor, limit)`, `r.events()`, `table.get(n)`, `table.range(a, b)`,
`table.atTime(ts)`, `event.get(path)`, `event.native()`, `event.prev()`,
`event.familyNumber(family)` and `check(reader)`.

```sh
node --test test/      # unit tests and the conformance vectors
```
