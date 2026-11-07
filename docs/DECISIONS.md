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
