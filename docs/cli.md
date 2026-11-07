# The `trak` command

`trak` inspects trak files. It is part of the Go module:

```sh
go install github.com/buk/trak/go/cmd/trak@latest
```

Release builds for Linux, macOS and Windows are attached to each
[GitHub release](https://github.com/buk/trak/releases).

| Command | Does |
|---|---|
| `trak types FILE` | Lists types with their index, version, event count and the first line of their description |
| `trak schema FILE TYPE` | Prints a type's definition, then a table of its columns |
| `trak cat FILE TYPE [--from n] [--to n] [--last k] [--since ts]` | Prints events as an aligned table |
| `trak export FILE [TYPE] [--format jsonl]` | Prints one JSON object per event |
| `trak tail [-f] FILE [TYPE] [--format jsonl]` | Prints the last 10 events. With `-f`, keeps following the file. |
| `trak check FILE` | Validates the whole file |

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

## `trak export`

Each line is `{"type", "n", "timestamp", "value"}`, using the same value shapes
as the conformance suite's `expect.json`:

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

## Exit status

| Status | Meaning |
|---|---|
| 0 | Success |
| 1 | The file can't be read, or `check` found a problem |
| 2 | Usage error |
