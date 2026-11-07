# trak 1.0

**trak is a log format that programs write and read, and people can read too.** A person can scroll, scan and grep a trak file. A program can reach any event, row or field in a few reads, and never mistakes content for structure.

| Guarantee | How |
|---|---|
| Unambiguous | Every boundary comes from lengths in footers. Logged content cannot forge structure. |
| Strings untouched | `str` values are copied: never escaped, scanned or validated. |
| Tabular | Each event type is a table: one row per event, one column per field. |
| Fast | Any row is reached in O(log n) footer reads. |
| Greppable | Plain text, with footers that are easy to spot or exclude. |

## 1. Model

- A file holds **schemas** and **events**. Each event has a **type**, and each type has exactly one schema per file.
- Order is meaningful only within a type. Events of different types may interleave, and no relation between them is implied.
- Within a type, timestamps never decrease.

## 2. Versions

trak uses semantic versioning, `MAJOR.MINOR.PATCH`.

| Part | Changes | Reader rule |
|---|---|---|
| MAJOR | framing | refuse a file with an unknown major |
| MINOR | additions older readers can skip | read using the rules in §9 |
| PATCH | wording only, never bytes | none |

The magic line carries the writer's `MAJOR.MINOR`. Each schema footer carries the `MAJOR.MINOR` its definition uses.

## 3. File layout

```
trak 1.0     magic line
<objects>    schemas and events
```

Every object is a **body** followed by one **footer line**. Lines end with LF.

| Marker | Line |
|---|---|
| `##[ … ]##` | schema footer |
| `==[ … ]==` | event footer |
| `--[ … ]--` | block label, inside an event body |

## 4. Encoding

| Item | Rule |
|---|---|
| Framing (footers, paths, schemas) | ASCII |
| Values | raw bytes, normally UTF-8 |
| Lengths and offsets | bytes |

### Letter VLQ

Every machine number is an unsigned variable-length quantity written in letters: base 26, most significant digit first.

| Digit | Meaning |
|---|---|
| `a`–`z` | value 0–25, more digits follow |
| `A`–`Z` | value 0–25, last digit |

VLQs are concatenated with no separators.

| Value | 0 | 25 | 26 | 148 | 1520 | 52310 |
|---|---|---|---|---|---|---|
| VLQ | `A` | `Z` | `bA` | `fS` | `cgM` | `czjY` |

### Pointers

A pointer stores `(this object's start − target object's end) + 1`. `0` means none.

## 5. Schemas

### Types

| Syntax | Meaning |
|---|---|
| `str` | string, never inspected |
| `num` | decimal number matching `-?(0\|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?` |
| `enum(a, b, …)` | one of the listed words |
| `\|T` | leaf `T`, laid out as a block |
| `{ a: A, b: B, … }` | object with fixed keys |
| `{} T` | map: any keys, every value `T` |
| `[] T` | list: any length, every element `T` |
| `T?` | optional: `T` or absent |
| `T @name`, `T @name(text)` | annotation |

- `str`, `num` and `enum` are **leaves**. Writers validate `num` and `enum` values. Readers may.
- An event's top-level type is an object. Keys and enum words match `[A-Za-z_][A-Za-z0-9_]*`. Trailing commas are allowed.
- An **annotation** name matches `[A-Za-z_][A-Za-z0-9_.-]*`, and its text contains no `)` or LF. trak passes annotations to applications unchanged, and trak 1.0 defines none. A name defined by a later minor version has that meaning only in schemas that declare that version or later.
- `//` starts a comment. A comment ending a field's line is that field's **description**. Comment lines before the opening `{` are the type's description. APIs expose both.

### Schema objects

A schema's body is its definition, ending with LF. Its footer:

```
##[ <name> <index> <version> ]##<VLQs>
```

| Field | Meaning |
|---|---|
| `name` | type name, `[A-Za-z_][A-Za-z0-9_.]*`, unique in the file. Prefix `trak.` is reserved. |
| `index` | type index, decimal: 1, 2, … in file order |
| `version` | `MAJOR.MINOR` the definition uses |

| # | VLQ | Meaning |
|---|---|---|
| 1 | body length | bytes from schema start to footer start |
| 2 | previous schema | pointer to the preceding schema, `0` for the first |
| 3 | extension count `E` | `0` in trak 1.0 |
| 4… | extensions | `E` VLQs |

- A type's schema appears anywhere before its first event.
- The schema is node 0 of its type's chain. Events are nodes 1, 2, …
- Schemas link through *previous schema*. Type index 0 refers to this chain.

## 6. Events

### Body

Leaves are written in schema order, depth first.

| Leaf | Written as |
|---|---|
| inline | `path: value` LF |
| block | value, LF, `--[ path ]--` LF |
| absent, or in an empty list or map | nothing |

`path` joins keys and list indexes with `.`, e.g. `tags.1` or `env.region`. Map keys are copied raw. Inline values should be single-line; readers are unaffected either way.

### Footer

```
==[ <type> <n> <timestamp> ]==<VLQs>
```

| Field | Meaning |
|---|---|
| `type` | type name |
| `n` | per-type event number, decimal, from 1 |
| `timestamp` | UTC, `YYYYMMDDTHHMMSS.sssZ` |

| # | VLQ | Meaning |
|---|---|---|
| 1 | body length | bytes from event start to footer start |
| 2 | step | pointer to node `n − 1` of this type |
| 3 | jump | pointer to node `J(n)` of this type (§7) |
| 4 | dir pointer | pointer to the latest node of type `dir type` |
| 5 | dir type | type index, 0 for the schema chain |
| 6 | extension count `E` | `0` in trak 1.0 |
| 7… | extensions, then values | `E` VLQs, then the values below in schema order, depth first |

| Shape | VLQs |
|---|---|
| leaf | value byte length |
| `T?` | `0` if absent, else `1` then `T`'s VLQs |
| object | each member's VLQs in order |
| `[] T` | count, then each element's VLQs |
| `{} T` | count, then per entry: key byte length, value's VLQs |

## 7. Navigation

`K` = number of types. Navigation uses only VLQs 1–6, so it works without parsing any schema.

| Need | Method | Cost |
|---|---|---|
| Previous object | start = footer start − body length | free |
| Event `m` of type T | step and jump from any node of T | O(log n) |
| Schema of T | step and jump down to node 0 | O(log n) |
| Latest event of T | walk back to an event of T, or one with dir type T | ≈ K events |
| All schemas | dir type 0, then *previous schema* | ≈ 2K reads |
| Row count of T | latest `n` | ≈ K reads |

**Jump.** `J(0) = 0`. For `n ≥ 1`, with `p = n − 1`: `J(n) = J(J(p))` if `p > J(p)` and `p − J(p) = J(p) − J(J(p))`, otherwise `J(n) = p`. To reach `m < n`, take the jump when its target is `≥ m`, otherwise the step.

**Directory.** The dir pointer targets the latest node of its type, which is the schema if the type has no events yet. Writers should pick the type covered longest ago, where an event covers its own type and its dir type. Readers must not depend on that policy.

## 8. Reading an event

1. The footer is the object's last line, and contains no LF.
2. The marker gives the object kind. The VLQs are the letters after `]==` or `]##`.
3. Start = footer start − body length.
4. Walk forward from the start using the schema and value lengths. An inline leaf takes `len(path) + 2 + L + 1` bytes and a block leaf `L + 1 + len(path) + 9`. No value byte is read.
5. The walk ends exactly at the footer start.

## 9. Compatibility

| Reader meets | Reader does |
|---|---|
| unknown major in the magic line | refuses the file |
| schema with a newer minor | reads it using the rules below |
| unknown leaf, `word` or `word(…)` | treats it as `str` |
| unknown annotation | passes it through |
| `E > 0` | skips the `E` extension VLQs |
| schema it cannot parse | still navigates the type; its values are unavailable |

## 10. Writer rules

- Write the magic line first.
- Write each type's schema once, before its first event.
- Keep timestamps non-decreasing within each type.
- Validate `num` and `enum` values. Never inspect `str` values.
- Append each object with a single write.

## 11. Recovery

A crash can leave an incomplete object at the end of a file. A reader uses the last line that is a valid footer: it parses, its type is known, its VLQ count matches the schema, and its walk ends at its footer start. Bytes after it are ignored, and a writer reopening the file truncates them.

## 12. Tables and viewers

| View | Source |
|---|---|
| Table list | all schemas, with type descriptions |
| Columns | leaves in schema order, named by path |
| Column tooltips | field descriptions |
| Row count | latest `n` |
| Scroll to row `m` | step and jump, O(log n) |
| Visible rows | footer plus one contiguous read per row |
| Block cells | collapsed, expanded on demand by slicing the file |
| Lists and maps | expandable cells |
| Sorting and formatting | by leaf type and known annotations |
| Time scrubbing | binary search on per-type timestamps |

## 13. Example

```
trak 1.0
// One CI build run.
{
  user: str,                  // who triggered the build
  status: enum(ok, failed),
  duration: num @ms,          // wall-clock time
  tags: [] str,
  stdout: |str @log,          // compiler output
  error: str?,                // set when the build failed
}
##[ build 1 1.0 ]##kXAA
// A release pushed to an environment.
{
  target: str,
  version: str @semver,
}
##[ deploy 2 1.0 ]##dEBA
user: ana
status: ok
duration: 910
tags.0: ci
Compiling core...
Done.
--[ stdout ]--
==[ build 1 20261107T140210.120Z ]==dHeEeEBAADCDBCXA
user: joel
status: failed
duration: 1520
tags.0: ci
tags.1: nightly
Compiling core...
ld: undefined symbol main
--[ stdout ]--
error: link failed
==[ build 2 20261107T140322.415Z ]==fQBBfJCAEGECCHbRBL
```

Last footer, `fQBBfJCAEGECCHbRBL`:

| VLQ | Value | Meaning |
|---|---|---|
| `fQ` | 146 | body length |
| `B` `B` | 1, 1 | step, jump |
| `fJ` | 139 | dir pointer, to the `deploy` schema |
| `C` | 2 | dir type `deploy`, which has no events yet |
| `A` | 0 | no extensions |
| `E` `G` `E` | 4, 6, 4 | `user`, `status`, `duration` |
| `C` `C` `H` | 2, 2, 7 | `tags` count, `tags.0`, `tags.1` |
| `bR` | 43 | `stdout` |
| `B` `L` | 1, 11 | `error` present, `error` |

As a table:

| n | user | status | duration | tags | stdout | error |
|---|---|---|---|---|---|---|
| 1 | ana | ok | 910 | ci | ▸ 2 lines | |
| 2 | joel | failed | 1520 | ci, nightly | ▸ 2 lines | link failed |
