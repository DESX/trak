# Decisions

Spec clarifications, and choices the spec leaves to the reference
implementations.

Each entry records the question, the answer and where it was applied.
Changes made before the 1.0.0 release are folded into 1.0.0.

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
