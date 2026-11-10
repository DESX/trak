# Decisions

Spec clarifications, and choices the spec leaves to the reference
implementations.

Each entry records the question, the answer and where it was applied.
1.0.0 was never released. Changes made before the 1.1.0 release are folded
into 1.1.0.

## Spec clarifications

### D1. Prefix and suffix binding in types (PATCH)

**Question.** §5 doesn't say how prefixes (`|`, `{}`, `[]`) combine with
suffixes (`?`, `@name`). Does `[] str?` mean an optional list or a list of
optional strings?

**Answer.** A type is `prefix* base suffix*`. Suffixes apply to the whole type
before them, prefixes included:

| Text | Means |
|---|---|
| `[] str?` | `([] str)?`, an optional list |
| `[] str @x` | `([] str) @x`, an annotation on the list |
| `\|str @log` | `(\|str) @log`, an annotation on the block leaf |
| `{} num? @u` | `(({} num)?) @u` |

A list of optional elements can't be written in 1.0.

**Applied.** A sentence in SPEC.md §5 (Types).

### D2. Map keys in paths (PATCH)

**Question.** §4 says paths are ASCII, but §6 copies map keys raw into paths,
and the conformance suite needs UTF-8 map keys.

**Answer.** Framing is ASCII except for the map-key segments of paths, which
are raw bytes like values. No bytes change.

**Applied.** The SPEC.md §4 encoding table.

### D3. Map entries with no leaves (PATCH)

**Question.** A map key appears only in the paths of its entry's leaves. When
an entry writes no leaves (an empty list or map, an absent optional, an empty
object), only the key's byte length is stored.

**Answer.** That's allowed. Readers report such a key as unknown (`null`) and
still give its length. In native trees and `expect.json`, a map is an ordered
list of `[key, value]` pairs, so unknown keys and file order are both kept.

**Applied.** A sentence in SPEC.md §6 (Body).

### D4. Repeated map keys (PATCH)

**Answer.** Writers reject a map with a repeated key, comparing key bytes. Map
keys aren't `str` values, so "strings untouched" still holds. Readers and
`check` don't enforce it.

**Applied.** SPEC.md §10.

### D5. Leading zeros in VLQs (PATCH)

**Answer.** A VLQ never starts with `a` (a leading zero digit). Readers and
`check` reject one as a format error, so every number has exactly one encoding.

**Applied.** SPEC.md §4 (Letter VLQ).

### D6. `{}` as empty object or map prefix (PATCH)

**Answer.** `{}` (whitespace and comments allowed inside) followed by
something that starts a type (`|`, `{`, `[` or a word) is the map prefix.
Otherwise it's an empty object, which has no leaves and no VLQs.

**Applied.** SPEC.md §5 (Types).

### D7. Event families (MINOR, trak 1.1)

**Question.** Related event types, such as a build and its steps, are separate
chains. Nothing orders them against each other, and there is no table for the
whole group.

**Answer.** Joel's design, 2026-11-10:

- A type named `P.x` in a schema declaring `1.1` is a child of `P`. It lists
  only its own members; its object is `P`'s members followed by them.
- An event's extensions hold its position in each enclosing family, root
  first, one number per level. The number in the event's own family is
  written only once the type has a child, and is `n` otherwise.
- Any key that never decreases along a chain can be found by a jump walk in
  O(log n). Row `m` of a family is then a seek in each of its `k` types, so
  O(k log n), and a family's row count is its largest number.
- Timestamps never decrease within a family.

Files without children are byte-for-byte the same as in 1.0, apart from the
magic line. 1.0 was never released, so no 1.0 reader has to read 1.1 files.

**Applied.** SPEC.md §1, §5 (Schema objects), §6 (Family numbers), §7, §10
and §12. The magic line is now `trak 1.1`.

## Implementation choices

These choices are left to implementations by the spec. The reference libraries
make them the same way. They change no bytes, so Joel can revisit
any of them without a spec change.

| # | Topic | Choice |
|---|---|---|
| I1 | Field paths in schema APIs | List indexes are written `[]` and map keys `{}`, e.g. `jobs.[].env.{}`. |
| I2 | A leaf's description and annotations | A description that is empty after stripping `//` and whitespace counts as no description. Leaf descriptions and annotations come from the leaf's own field (the nearest enclosing object member). Annotations on the types between that member and the leaf are listed outermost first. Annotations on the top-level object are exposed on the schema. |
| I3 | A leaf's `optional` flag | True when any type from the root down to the leaf is optional, i.e. the column can be empty. |
| I4 | Unknown leaves when writing | `define` rejects them: a writer only writes the versions it knows. Readers accept them as `str` (§9). |
| I5 | Schema versions | A schema footer whose major isn't 1 makes that schema unparseable: the type still navigates, but its values are unavailable. `check` parses `1.0` and `1.1` schemas strictly and a newer minor leniently, and a child of an unparseable parent leniently. |
| I6 | `check` and torn tails | A torn tail is accepted only if it has no complete footer line. If the bytes ignored by recovery hold a complete but invalid footer line, `check` fails and reports why that footer is invalid. Without this, a corrupt last object would pass as a torn tail. |
| I7 | `Event.get(path)` with colliding paths | Map keys may contain `.`, so two leaves can share a path. The first one in file order wins. `to_native()` is always unambiguous. |
| I8 | `flush()` | Writers make one unbuffered `write` per object, so `flush()` calls fsync. |
| I9 | Schema text | `define` writes the definition exactly as given and adds a final LF if it's missing. |
| I10 | Python VLQ test range | The 0 to 10⁷ round trip runs in Python when `TRAK_SLOW=1`. Otherwise it runs to 10⁶. Go and C++ always cover 10⁷. |
| I11 | Lists of empty objects | A list whose element type writes nothing (a required empty object) costs no bytes per element, so a tiny file could claim billions. Readers refuse to decode one with more than 65,536 elements, as a format error. |
| I12 | Versions written | The magic line is always `trak 1.1`. A schema footer declares `1.1` only for a child type, and `1.0` otherwise, the oldest version its definition needs. |
| I13 | Dotted names when writing | `define("a.b", …)` fails unless `a` is defined: a writer never writes a dotted name that isn't a child, so a `.` in a name it writes always means a family. Readers still accept dots in `1.0` schemas as plain characters (§5). |
| I14 | Write positions | `write` returns `(n, offset)`, the offset being where the event's object starts. It is the same as `Event.offset` and is a reader cursor. |
| I15 | Cursors | `before(c, k)` returns events starting before `c`, newest first; `after(c, k)` those starting after `c`, oldest first. A cursor is valid if it is the magic line's end, the reader's end, or the end of an object that passes the recovery test (§11). `after` seeks each type by offset (§7, Seek) and walks back from the earliest end of any type's `k`-th event after `c`. |
| I16 | Directory sync | With `sync`, `create` fsyncs the file's directory after writing the magic line, so the new file survives a crash. It does nothing on Windows. |
| I17 | Large VLQs in JavaScript | The JavaScript reader rejects a VLQ above 2^53 as a format error, since a JavaScript number can't hold it exactly. No real file has lengths or offsets that large. |
| I18 | Extensions after the family numbers | Readers and `check` skip them (§9). `check` requires the family numbers themselves to be exact. |
