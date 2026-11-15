# trak

**trak is an append-only, human-readable file holding one versioned tree of structured data.** Every append creates a complete new version of the tree, and any field of any version is a few reads away. You can scroll, scan and grep the file. A program never mistakes content for structure, because every boundary comes from lengths in footers.

```
trak 1.2
##[ schema ]##hQcCA
# A CI server.
root ci {
  name: str,
  builds: event build,
}

# One build.
object build {
  user: str,                   # who started it
  status: event enum(running, ok, failed),
  log: |str,
}
name: ci-1
==[ ci 1 20261113T090000.000Z ]==LAAEABAAA
user: joel
--[ log ]--
Compiling core...
ld: undefined symbol main
--[ /log ]--
==[ builds.0 2 20261113T090001.000Z ]==dCBAEbRABBACABADAAAAAAABAAA
status: failed
==[ builds.0.status.0 3 20261113T090002.000Z ]==PAAGABBACABADBBDBDBABBAEABAF
```

Each `==[ … ]==` footer names the item its append wrote, such as `builds.0.status.0`. Its trailing letters are lengths and pointers: the new item and the path above it to the root, written by **path copying**, so every unchanged part of the tree is reached through pointers to earlier appends. The format is specified in [SPEC.md](SPEC.md), and [docs/DECISIONS.md](docs/DECISIONS.md) records where it differs from the draft it started from.

The reference application is [Sophie](docs/sophie.md), an agent harness that keeps all its state, configuration included, in one trak file.

This repository holds:

- the spec
- the reference Go library: writer, reader and check
- a JavaScript reader
- `sophie-gen`, which writes synthetic Sophie logs of any size
- `trak serve` and a browser viewer that stays responsive on a 5 GB file

## Go

```sh
go get github.com/desx/trak/go
```

```go
w, err := trak.Create("ci.trak", schema, map[string]any{"name": "ci-1"}, time.Now(), nil)
if err != nil {
	log.Fatal(err)
}
w.Append("builds", map[string]any{"user": "joel", "log": "Compiling core...\nld: undefined symbol main"}, time.Now())
w.Append("builds.0.status", "failed", time.Now())
w.Close()

f, _ := trak.Open("ci.trak")
latest, _ := f.Get("builds.-1.status.-1", f.Version()) // the newest build's status, now
old, _ := f.Get("builds", 1)                            // the list of builds at version 1: empty
fmt.Println(latest.(trak.Leaf).String(), old.(*trak.List).Len())
```

`trak.Options{Policy: trak.Delta}` writes smaller footers; every reader reads either policy. See [bench/README.md](bench/README.md) for the comparison.

## The CLI

```sh
go install github.com/desx/trak/go/cmd/trak@latest
```

| Command | Does |
|---|---|
| `trak check FILE` | Validates every append: each version must be an exact path copy of the one before |
| `trak stat FILE` | Sizes, footer bytes per append, node forms |
| `trak schema FILE` | Prints the schema |
| `trak get FILE PATH [--at V]` | A value, a list's length, or an object's fields, at any version |
| `trak serve DIR [--addr A] [--prefix /p]` | The viewer, for the `.trak` files under DIR |

## JavaScript

[js/](js) is a reader for browsers and Node with no dependencies. It reads over byte sources: memory, a `File`, HTTP Range, or `trak serve`. A block cache batches every read made in one turn of the event loop into one request.

```js
import { ServeSource, TrakFile } from "./js/src/index.js";

const f = await TrakFile.open(await ServeSource.open("api/read", "sophie.trak"));
const turns = await f.get("sessions.12.turns");
const rows = await turns.range(150000, 150050); // one trie read per 16 rows
```

## The viewer

```sh
trak serve ~/logs
```

Then open the address it prints.

- **Files.** A pane on the left lists the files you have opened, with their full paths; **+** opens one from the server or from your computer. The server's picker shows each file's description.
- **Tabs.** The first tab is the root, as a tree. Right-click any event list to open it as a **table**: one row per item, one column per leaf, nested objects as collapsible spanning headers. Or open it as a **tree** whose scrollbar steps through versions.
- **Log.** The file as text, from any version.

Every view has a URL. The viewer knows nothing about any schema: everything comes from the file, descriptions included. Its own version is in the status bar.

The debug panel, the request counter at the bottom right, lists every byte range read.

## Sophie logs at scale

```sh
go run ./cmd/sophie-gen --size 5GB --seed 1 sophie.trak   # about 90 s
go run ./cmd/trak check sophie.trak                      # about 100 s
```

The 5 GB file holds:
- 334 interleaved sessions and a million turns
- the largest session at 413,000 turns
- 4,110 changes to the defaults

Output depends only on `--size`, `--seed` and `--policy`. Generated files are test data and aren't committed.

[viewer/test](viewer/test) times the viewer in headless Chromium on such files, and [bench/README.md](bench/README.md) has the results.

## Tests

```sh
cd go && go test ./...                      # library, CLI, generator, conformance vectors
cd js && npm test                           # the JS reader against the same vectors
cd viewer/test && npm ci && npm test        # the viewer end to end, timing every target
```

The vectors in [conformance](conformance) are written by `go test -run TestVectors -update`.

## License

MIT
