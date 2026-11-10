# Library API

The Python, Go and C++ libraries share one API, named in each language's own
style. The JavaScript library is a reader with the same API, `async` and in
camelCase ([js/README.md](../js/README.md)). Event numbers are always the spec's
1-based `n`. Timestamps are UTC, with millisecond resolution.

## Writer

| Operation | Python | Go | C++ |
|---|---|---|---|
| Create a file | `trak.create(path, overwrite=False, sync=False)` | `trak.Create(path, trak.Options{})` | `trak::Writer::create(path, {overwrite, sync})` |
| Append to a file | `trak.open_append(path, sync=False)` | `trak.OpenAppend(path, opts)` | `trak::Writer::open_append(path, opts)` |
| Define a type | `w.define(name, text) -> Schema` | `w.Define(name, text) (*Schema, error)` | `w.define(name, text) -> const Schema&` |
| Write an event now | `w.write(type, value) -> Pos` | `w.Write(type, value) (Pos, error)` | `w.write(type, value) -> Pos` |
| Write with a timestamp | `w.write(type, value, ts)` (`str` or aware `datetime`) | `w.WriteAt(type, value, time.Time)` | `w.write(type, value, millis)` |
| Make durable (fsync) | `w.flush()` | `w.Flush()` | `w.flush()` |
| Close | `w.close()`, or a `with` block | `w.Close()` | `w.close()`, or the destructor |

- `write` returns a `Pos`: the event's `n` and `offset`, where its object starts.
  The offset is a reader cursor (see Cursors below).
- `create` fails if the file exists, unless `overwrite` is set. `open_append`
  truncates a torn tail first (SPEC §11).
- `define` parses and validates the definition, then writes the schema object. A
  final LF is added if the definition lacks one. Writers reject unknown leaf
  types (DECISIONS I4).
- `define("build.step", text)` defines a child of `build`, which must be defined
  first. `text` lists only the child's own fields; a key `build` already has is
  an error. Values of a child include its parent's fields.
- `sync` calls fsync after every object, and `create` also syncs the file's
  directory (not on Windows).
- A writer holds an exclusive advisory lock on its file until it is closed: flock
  on POSIX, and LockFileEx on Windows (on one byte far past the data, so readers
  are never blocked). A second writer fails with `TrakError` (Python),
  `ErrLocked` (Go) or `LockedError` (C++). Readers take no lock.

### Values

A value is a native tree whose shape follows the schema:

| Schema | Python | Go | C++ (`trak::Value`) |
|---|---|---|---|
| object | `dict`, or a dataclass instance | `map[string]any`, or a struct (field name, or the `trak:"name"` tag; `trak:"-"` skips a field) | `Value::dict({...})` |
| `[] T` | `list` or `tuple` | any slice or array except `[]byte` | `Value::list({...})` |
| `{} T` | `dict`, or a list of `(key, value)` pairs | `trak.Pairs`, `map[string]T` (keys sorted bytewise), or `[]any` of `[key, value]` | `Value::dict({...})`, in order |
| `str` | `str` or `bytes` | `string` or `[]byte` | string |
| `num` | `int`, `float`, `Decimal` or a decimal `str` | any integer or float type, `json.Number`, or a decimal string | any integer, `double`, or a decimal string |
| `enum` | `str` | `string` | string |
| absent (`T?`) | `None`, or a missing key | `nil`, a nil pointer, or a missing key | default `Value()`, or a missing key |

- Integers are written in plain decimal. Floats use the shortest form that
  round-trips. NaN and infinities are rejected.
- A decimal string given for `num` is checked against the grammar and copied.
- `str` values are copied as given. They are never escaped, scanned or
  validated.
- Map keys may be any bytes. A map with a repeated key is rejected.
- C++ converts your own types through `trak::ValueTraits<T>`. The library
  specializes it for `std::optional`, `std::vector` and `std::map<std::string, T>`.

Errors when writing:

| Problem | Python | Go | C++ |
|---|---|---|---|
| Bad definition | `trak.SchemaError` (`.line`, `.column`) | `*trak.SchemaError` | `trak::SchemaError` |
| Value doesn't fit, bad `num` or `enum`, timestamp earlier than the type's last | `trak.ValueError` | `*trak.ValueError` | `trak::ValueError` |

### Reference writer behavior

The spec leaves these choices to writers. The three reference writers make them
the same way, so their output is byte-identical.

| Topic | Rule |
|---|---|
| Directory policy | Keep `cov[i]`, the sequence number at which type index `i` was last covered. It starts at −1 for index 0, and for each type when its schema is written. For each event of type `t` at sequence `s`: set `cov[t] = s`, choose the index `i ≠ t` with the smallest `(cov[i], i)` among type 0 and all defined types, then set `cov[i] = s`. Schema objects don't change `cov`. |
| Dir pointer target | The latest node of the chosen type. For index 0, that's the latest schema object. |
| Jump | `J(n)` in O(log n): `n − J(n)` is the smallest term of the greedy skew-binary decomposition of `n` into numbers `2^k − 1`. It is tested against the SPEC §7 recurrence for every `n ≤ 10⁶`. |
| Jump targets | The writer keeps, per type, the end offsets of nodes `n, J(n), J(J(n)), …, 0`. Every future jump target is on that list. |
| Map entry order | The order the caller supplies. A Go `map` has no order, so its keys are sorted bytewise. |
| Timestamps | The default is the current time to the millisecond. If that's earlier than the last timestamp in the type's family, that timestamp is reused. A caller-supplied timestamp earlier than it is an error. |
| Extensions | Exactly the family numbers (SPEC §6): one per ancestor, then the type's own once it has a child. |
| Versions | The magic line is `trak 1.1`. Schema footers say `1.1` for child types and `1.0` otherwise. |
| Writes | Each object is built in memory and appended with one `write` call. |

When `open_append` reopens a file, it recovers the last valid footer and
truncates anything after it. Then it rebuilds its state:
- the schemas, through the schema chain
- each type's latest node and `n`, through the directory
- each type's jump targets, by following jump pointers from the latest node
- each family's event count, from its types' latest events
- `cov`, approximately, from the last 2K + 2 events

Byte-identical output across implementations is only guaranteed within a single
writing session.

## Reader

| Operation | Python | Go | C++ |
|---|---|---|---|
| Open | `trak.open(path)` | `trak.Open(path)` | `trak::Reader::open(path)` |
| Pick up appended objects | `r.refresh() -> bool` | `r.Refresh() (bool, error)` | `r.refresh() -> bool` |
| Types, in index order | `r.types()` | `r.Types()` | `r.types()` |
| One type's schema | `r.schema(name)` | `r.Schema(name)` | `r.schema(name)` |
| A table | `r.table(name)` | `r.Table(name)` | `r.table(name)` |
| A family table | `r.family(name)` | `r.Family(name)` | `r.family(name)` |
| All events in file order | `for e in r` | `r.Events(fn)` | `r.events(fn)` |
| All events, newest first | | `r.EventsBackward(fn)` | `r.events_backward(fn)` |
| Up to `k` events before a cursor, newest first | `r.before(cursor, k)` | `r.Before(cursor, k)` | `r.before(cursor, k)` |
| Up to `k` events after a cursor, oldest first | `r.after(cursor, k)` | `r.After(cursor, k)` | `r.after(cursor, k)` |

`open` memory-maps the file, checks the magic line, and finds the last valid
footer (SPEC §11). Bytes after it are ignored. `r.end` is the offset just past
the last valid object, and `r.size` is the file size.

### Schemas

A `Schema` has `name`, `index`, `version`, `text` (the definition as written),
`description`, `annotations` (on the top-level object) and `fields`.

A child type also has `parent`, its parent's name, and every schema has
`lineage`: its families, root first and itself last (`["build", "build.step"]`).
A child's fields start with its parent's.

It also has the parsed type tree: `root` in Python and C++, `Root` in Go. The
tree is missing (`None`, `nil` or null) when the definition can't be parsed, for
example a newer minor version's syntax. `error` (`Err` in Go) then says why. The
type still navigates, but its values are unavailable (SPEC §9).

Each field is one leaf column:

| Attribute | Meaning |
|---|---|
| `path` | Leaf path, with list indexes as `[]` and map keys as `{}`, e.g. `jobs.[].env.{}` |
| `type` | `str`, `num` or `enum` |
| `enum` | The enum words (`enum_words` in C++) |
| `unknown` | The original text of an unknown leaf type, which reads as `str` |
| `block` | Whether the leaf is laid out as a block (`\|T`) |
| `optional` | Whether any type from the root to the leaf is optional |
| `annotations` | `(name, text)` pairs, outermost first |
| `description` | From the field's trailing comment |

### Tables

| Operation | Python | Go | C++ |
|---|---|---|---|
| Row count | `t.count()` | `t.Count()` | `t.count()` |
| Event `n` | `t.get(n)` or `t[n]` | `t.Get(n)` | `t.get(n)` |
| Latest event | `t.latest()` | `t.Latest()` | `t.latest()` |
| Events `a` to `b`, inclusive (descending if `a > b`) | `t.range(a, b)` | `t.Range(a, b, fn)` | `t.range(a, b, fn)` |
| First event with timestamp ≥ `ts` | `t.at_time(ts)` | `t.AtTime(time.Time)` | `t.at_time(millis)` |

- `count()` and `latest()` read about K footers.
- `get(n)` reads at most 3·log₂(n) + 4 footers.
- `at_time` is a seek (SPEC §7): at most 2·(3·log₂(n) + 4) footers.
- An `n` outside the table raises `IndexError` (Python), returns an error
  wrapping `ErrNotFound` (Go), or throws `std::out_of_range` (C++).

### Families

A family is a type and its descendants, in file order (SPEC §1). `r.family(name)`
has the same operations as a table, numbered by family row:

| Operation | Python | Go | C++ |
|---|---|---|---|
| Its types, the family's own first | `f.types` | `f.Types()` | `f.types()` |
| Row count | `f.count()` | `f.Count()` | `f.count()` |
| Row `m` | `f.get(m)` | `f.Get(m)` | `f.get(m)` |
| Latest row | `f.latest()` | `f.Latest()` | `f.latest()` |
| Rows `a` to `b` | `f.range(a, b)` | `f.Range(a, b, fn)` | `f.range(a, b, fn)` |
| First row with timestamp ≥ `ts` | `f.at_time(ts)` | `f.AtTime(time.Time)` | `f.at_time(millis)` |

- `get(m)` seeks each of the family's `k` types for row `m`: at most
  k·2·(3·log₂(n) + 4) footers.
- `range` merges one range per type, so scrolling costs about a footer per row.
- A family of a type with no children is just that type's table.

### Cursors

`before` and `after` page through the events of every type in file order. A
cursor is an event's `offset` (or `Pos.offset` from a writer). To get the next
page, pass the last event returned. Python and JavaScript use `None`/`null`, Go a
negative cursor, and C++ `std::nullopt` for the end (`before`) or the start
(`after`). An empty page means there are no more.

- `before` reads one footer per object.
- `after` can't scan forward, because logged content can look like a footer.
  It seeks each type for its first event after the cursor and walks back from a
  point that has at least `k` events before it: K seeks plus at most K·`k`
  footers, and usually about `k`.
- A cursor is checked like a recovered footer (SPEC §11): it must be the magic
  line's end, `r.end`, or the end of a valid object. Otherwise the call fails
  with `trak.ValueError` (Python), an error wrapping `ErrBadCursor` (Go) or
  `trak::ValueError` (C++). Appends never move a cursor, so it survives
  `refresh`; a cursor past a truncated end is rejected.

### Events

| Attribute | Python | Go | C++ |
|---|---|---|---|
| Type, number, timestamp, offset | `e.type`, `e.n`, `e.timestamp`, `e.offset` | `e.Type()`, `e.N()`, `e.Timestamp()`, `e.Offset()` | `e.type()`, `e.n()`, `e.timestamp()`, `e.offset()` |
| Timestamp as a time | `e.time` (`datetime`) | `e.Time()` | `e.millis()` |
| A leaf's bytes, zero-copy | `e.raw(path)` (`memoryview`) | `e.Raw(path)` (`[]byte`) | `e.raw(path)` (`string_view`) |
| A leaf as text | `e.get(path)` | `e.Get(path)` | `e.get(path)` |
| A `num` leaf | `e.as_int(path)`, `e.as_float(path)` | `e.Int(path)`, `e.Float(path)` | `e.as_int(path)`, `e.as_float(path)` |
| Every present leaf | `e.items()` | `e.Leaves()` | `e.leaves()` |
| The whole value | `e.to_native()` | `e.Native()` | `e.native()` |
| The previous event of its type, in one read | `e.prev()` | `e.Prev()` | `e.prev()` |
| Its row in a family (default: the root) | `e.family_number(name)` | `e.FamilyNumber(name)` | `e.family_number(name)` |

- Paths are as written in the file, e.g. `tags.1` or `env.region`. An absent
  leaf returns `None` (Python), `ok == false` (Go) or `std::nullopt` (C++).
- `num` values come back as their decimal text.
- Map keys may contain `.`, so two leaves can share a path. `get` then returns
  the first one in file order. The full tree is never ambiguous.
- Zero-copy views point into the mapped file. They stay valid until the reader
  is closed or refreshed.

The full tree has strings for leaves and an absent marker for absent values.
Objects keep schema order. Maps are ordered `(key, value)` pairs. A key is
unknown (`None`, `Known == false`, or `std::nullopt`) when its entry has no
leaves, because the key bytes then appear nowhere in the file (SPEC §6).

### Errors when reading

| Problem | Python | Go | C++ |
|---|---|---|---|
| Bytes don't follow the format | `trak.FormatError` (`.offset`) | `*trak.FormatError` (`.Offset`) | `trak::FormatError` (`offset()`) |
| Major version isn't 1 | `trak.UnsupportedVersion` (a `FormatError`) | `*trak.UnsupportedVersionError` (unwraps to `*FormatError`) | `trak::UnsupportedVersion` (a `FormatError`) |
| Values of an unparseable schema | `trak.SchemaError` | `*trak.SchemaError` | `trak::SchemaError` |

## Validation

`check(path)` (`trak.check` in Python, `trak.Check` in Go and `trak::check` in
C++) validates the whole file and returns its number of objects. It reports the
first problem as a format error with its byte offset. It checks:

- every footer, pointer, event number and timestamp
- every family number, and that timestamps never decrease within a family
- every schema, and that a child's parent comes first
- every value walk, and the bytes around each value (paths, `: `, block labels)
- every `num` and `enum` value

A torn tail is allowed only if it contains no complete footer line (DECISIONS I6).

## Thread safety

Readers and writers are not safe for concurrent use. Use one per thread, or add
your own locking.
