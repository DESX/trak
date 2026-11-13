# Decisions

Where the trak 1.2 spec was ambiguous or wrong, what was decided, and choices the spec leaves to the reference implementations. The spec handed over on 2026-11-12 was the starting point; SPEC.md now says what these entries decided.

**Design changes** are marked: they change what the format can do, not only how it's written down.

## Format

### D1. The version is 1.2

The handover called the format "trak 1.1", but v1.1.0 is a tag of the old per-type format. Joel named this one 1.2.0. The magic line is `trak 1.2`. Readers refuse a minor below 2, since 1.0 and 1.1 are a different format, and read newer minors.

### D2. Nodes are numbered from 1, root first

The spec numbered nodes from 0 with the item first, and gave d = 0, i = 0 the meaning "none". Then a trie node couldn't point at the new item in its own footer, since that pointer is (0, 0).

There was also an ordering problem. A footer's nodes carry no type tags, so a reader lays them out from the path. Each list's trie depth follows from its count, and the count sits in the parent's slot. With the item first, the reader meets trie nodes before it knows how many there are.

So nodes are numbered from 1, root first:

| Pointer | Means |
|---|---|
| (0, 0) | none |
| (0, i) | node i of this footer |
| (d, i) | node i of an earlier append |

A pointer within a footer always points forward.

### D3. Nested objects with event lists have nodes (design change)

The spec's object node had one slot per event field, which left no way to reach `defaults.watchdog.w1`: `watchdog` is a plain nested object.

A nested object that contains event lists now gets its own object node and a slot in its parent that points to it. That slot is none until one of its lists first changes. It also gives such an object a version history of its own. Without one, scrubbing `defaults` through its versions would step through every append in the file.

Nested objects without event lists, such as `setup` or `context`, have no node; their leaves are part of their item's.

An optional object can't contain event lists, since a slot can't be absent.

### D4. Version 1 writes the root's leaves

The spec never appended the root, so leaves of the root, such as Sophie's `version`, had nowhere to be written. Version 1 is now the root append: its path is the root's name, and its only node is the root.

### D5. The schema is one header at the start (design change)

The spec put the schema in a body followed by a footer, and allowed later schemas that add fields. But nothing pointed at the schema: a reader would have had to scan forward for `##[`, and a `#` description can contain that text.

The schema is now a header line followed by the text:

```
##[ schema ]##<text length><root append length><E>…
```

A file has one schema, which never changes. The root append's length is in the header too, so a reader reaches the root's leaves in one read, where a jump walk to version 1 took about 22 reads on the 5 GB file.

Adding fields to a schema later is left out of 1.2. It needs a per-append schema reference, about 8 bytes on every append, and nothing in the Sophie demo uses it.

### D6. A delta's base is a full node (design change)

The spec allowed delta chains up to ⌈log₂(v + 1)⌉ + 1 long. That makes reads grow with the file's age, and a 5 GB file has 3.3 million versions.

A delta's base must now be a full node of the same kind, so any node is at most two reads. check enforces it.

### D7. Full forms carry their entry count

A full trie node at the end of a list is partial. A delta read later may use it as a base, by which time the list has grown, so the reader can't work out the node's size from the count it sees. Every full form is now `0, n, n entries`.

Slots take the same two forms, with the same form marker. The spec mentioned slot deltas but gave them no marker.

### D8. Trie depth is exact

The spec said "at most ⌈log₁₆ count⌉ + 1 deep", but readers compute the depth from the count, so a writer can't choose it. A list of c items has depth h, the smallest h ≥ 1 with 16^h ≥ c.

### D9. The item of a leaf list has an empty node

A trie entry must point at a node. An item of a leaf list, such as `defaults.model`, has an empty node; its value is its append's body.

### D10. Append references are one VLQ

The version jump and an item's leaves name a whole append, not a node, so they are one VLQ d: 0 is none. d = 1 is the append just before: d counts from the end of the footer to the start of the body that refers to it.

### D11. The root node has only slots

The root's version count, previous version and jump would repeat the footer's own version, the append before it, and the footer's version jump. They are left out, which saves about 20 bytes on every append.

### D12. check verifies every version as a path copy

The spec's bounds ("a footer holds only the nodes on the changed path") can't be checked node by node. check instead re-plans each append from the version before and requires every entry it didn't change to be equal, and every count, version and jump to be what the writer must write. By induction, every version of the tree is right. It runs at about 30,000 appends a second.

### D13. Leaf types and path values

- `path` is a leaf kind. A stored path must name a value that exists before the append: an item, a list, an object or a present leaf. Paths with negative numbers are lookups only.
- An unknown type name is a schema error. The spec said to read it as `str`, but with one schema per file there are no newer leaf types to meet, and the error catches typos.
- Event list items can't be optional, and an event list can't hold event lists directly.
- Descriptions are the comment lines before a type or field, or a comment on the field's first line, joined with spaces.

### D14. Recovery

The last valid footer is the last line that parses, whose body walks, and whose previous footer has the version before it. Its version must also fit in the bytes before it (at least 30 bytes per append): fuzzing found that a forged version number could otherwise make check allocate without bound.

Values can't forge a valid footer anywhere but in a torn tail, where a crafted value could look like a footer.

## Writer policies

### P1. Two policies: reference and delta

- **Reference** writes every node in full.
- **Delta** writes a trie node or slots as a delta against the full base of its previous version, when that delta is less than half the full form's size. Otherwise it writes the node in full, and that becomes the next base.

The handover suggested a "generations" policy too. It was left out to keep the work on the demo, and because delta already brings footers down to about 260 bytes. See bench/README.md for the comparison.

### P2. sophie-gen sizes by content

`--size` is the size of the file under the reference policy. The generator stops once the bodies reach 73% of it, measured on generated files. So the same seed writes the same content under every policy, and the policies' sizes can be compared directly.

## Viewer

### V1. Joel's layout

- A pane of open files on the left. "+" browses the directory `trak serve` serves; a local file can be read too.
- Tabs on the right. The first tab is the root as a tree.
- Right-clicking an event list opens it in a table view or a tree view.
- The tree view and the table view share one scrollbar, which maps to the whole range.
- Nested objects start collapsed. Expanding one in a table adds a header row that spans its leaves.

### V2. What a tree view's position means

| View | Position | Shows |
|---|---|---|
| `items:LIST@n` | item n | The object that owns the list, as it was right after item n was appended, with item n open |
| `tree:PATH@k` | the object's own version k | That object at version k. For the root, these are the file's versions. |

Highlighted fields are the ones that changed since the last position. Every leaf links to the append that wrote it.

### V3. One request per page

The JS reader batches every block wanted in one turn of the event loop into one request to `trak serve`'s read endpoint: `GET api/read?path=P&r=off+len,…`, answered with the ranges concatenated.

Standard multi-range requests would do the same, but proxies such as a Cloudflare tunnel may not pass them through, and could answer a multi-range request with the whole 5 GB. A plain Range source is still there for any static server.

### V4. URLs

Every view has a URL: `#f=FILE&v=SPEC`.

- Opening a view adds a history entry.
- Moving within a view replaces the current entry, so back and forward step between views.

## Limits

- JS readers refuse VLQs of 2^53 or more: offsets are JavaScript numbers.
- One read request is at most 256 ranges and 64 MB.
- An object has at most 64 slots.
