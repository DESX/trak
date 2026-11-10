# The `trak` command

`trak` inspects trak files. It is part of the Go module:

```sh
go install github.com/desx/trak/go/cmd/trak@latest
```

Release builds for Linux, macOS and Windows are attached to each
[GitHub release](https://github.com/desx/trak/releases).

| Command | Does |
|---|---|
| `trak types FILE` | Lists types with their index, version, parent, event count and the first line of their description |
| `trak schema FILE TYPE` | Prints a type's definition, then a table of its columns |
| `trak cat FILE TYPE [--family] [--from n] [--to n] [--last k] [--since ts]` | Prints events as an aligned table; with `--family`, the type and its descendants |
| `trak export FILE [TYPE] [--format jsonl]` | Prints one JSON object per event |
| `trak tail [-f] FILE [TYPE] [--format jsonl]` | Prints the last 10 events. With `-f`, keeps following the file. |
| `trak check FILE` | Validates the whole file |
| `trak serve DIR [--addr host:port] [--prefix /path]` | Serves a web viewer for the `.trak` files under `DIR` |

Flags may come before or after the file and type, as `--flag value` or
`--flag=value`.

## `trak cat`

```
$ trak cat build.trak build --last 2
N  TIMESTAMP             user  status  duration  tags.[]      stdout     error
1  20261107T140210.120Z  ana   ok      910       ci           ▸ 2 lines
2  20261107T140322.415Z  joel  failed  1520      ci, nightly  ▸ 2 lines  link failed
```

Each column is one leaf, named by its path.

| Value | Shown as |
|---|---|
| List elements | Joined with `, ` |
| Map entries | `key=value` |
| Block values | `▸ N lines` |
| Inline values | Line breaks and tabs escaped, cut at 40 characters with `…` |
| A type whose schema can't be parsed | `(values unavailable)` |

| Flag | Selects |
|---|---|
| `--from n`, `--to n` | Events `n` and later, or `n` and earlier |
| `--last k` | The last `k` events of the selection |
| `--since ts` | Events at or after `ts`, found by binary search. `ts` is a trak timestamp (`20261107T140000.000Z`) or RFC 3339 (`2026-11-07T14:00:00Z`). |

With `--family`, the rows are the family's, numbered in file order, with a TYPE
column. The columns are the family type's leaves, then each child's own:

```
$ trak cat builds.trak build --family --last 3
N  TYPE        TIMESTAMP             user  status  duration  tags.[]  stdout   error        step   ok
3  build.step  20261106T212000.003Z  ana   ok      20                 ▸ empty               link   no
4  build       20261106T212000.004Z  joel  failed  1                  ▸ empty  link failed
5  build.step  20261106T212000.005Z  ana   ok      1                  ▸ empty               retry  yes
```

`--from`, `--to`, `--last` and `--since` then select family rows.

## `trak export`

Each line is `{"type", "n", "timestamp", "families", "value"}`, using the same
shapes as the conformance suite's `expect.json`. `families` gives the event's
row in each of its families, root first:

- leaves are strings, including `num`
- absent values are `null`
- object members keep schema order
- maps are `[key, value]` pairs, with a `null` key when the entry has no leaves

Without a type, events are printed in file order.

```
$ trak export build.trak build
{"type":"build","n":1,"timestamp":"20261107T140210.120Z","value":{"user":"ana","status":"ok","duration":"910","tags":["ci"],"stdout":"Compiling core...\nDone.","error":null}}
```

## `trak tail`

Prints the last 10 events, of one type or of all types, one per line:

```
build 2 20261107T140322.415Z user=joel status=failed duration=1520 tags.0=ci tags.1=nightly stdout=▸ 2 lines error=link failed
```

With `-f`, it then checks the file every 250 ms (`--interval` changes this) and
prints new events as writers append them. `--format jsonl` prints the same
lines as `export`.

## `trak check`

```
$ trak check build.trak
build.trak: ok, 4 objects
$ trak check broken.trak
broken.trak: bad num value "01" (at byte 42)
```

`check` validates every footer, pointer, schema, value walk, `num` and `enum`,
and the bytes around each value. It reports the first problem with its byte
offset. A torn tail at the end of the file is accepted, unless it contains a
complete footer line.

## `trak serve`

```
$ trak serve ~/logs
serving /home/ana/logs at http://127.0.0.1:8765/
```

Open the address to browse the `.trak` files under the directory and view one as
a log or as tables. The viewer is built into the binary. It reads only the byte
ranges it shows, so a multi-GB file opens in a few reads, even over a remote
connection. Pressing Follow picks up events as writers append them.

- Files are served read-only. Paths can't leave the directory, even through
  symlinks, and hidden files and files not ending in `.trak` aren't served.
- It listens on `127.0.0.1:8765` unless `--addr` says otherwise. It has no
  authentication: to share it, put it behind something that does.
- `--prefix /trak` serves everything under `/trak/`, for a reverse proxy that
  forwards one path.

| Path | Serves |
|---|---|
| `viewer/` | The viewer |
| `viewer/api/files?dir=sub/dir` | A JSON listing: `{"root", "dirs", "files": [{"name", "size", "modified"}]}` |
| `viewer/files/PATH` | A file's bytes, with `HEAD` and `Range` support |

## Exit status

| Status | Meaning |
|---|---|
| 0 | Success |
| 1 | The file can't be read, or `check` found a problem |
| 2 | Usage error |
