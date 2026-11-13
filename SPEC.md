# trak 1.2

trak is an append-only, human-readable text file holding one versioned tree of structured data. Every append creates a complete new version of the tree, and any field of any version is reached in O(log n) reads.

| Guarantee | How |
|---|---|
| Unambiguous | Boundaries come from lengths in footers. Content can't forge structure. |
| Strings untouched | Values are copied, never escaped or inspected. |
| Versioned | Every append is a full version. Old versions stay readable forever. |
| Fast | Any field, list item or version in O(log n) reads. No scans. |
| Greppable | Plain text. Each footer names the path it writes. |

## 1. Model

- A file describes one root object, defined by its schema.
- An object has named fields. A field is a leaf, a nested object, or an event list.
- Leaves are `str`, `num`, `enum(a, b, …)` and `path`. `|str` is a `str` laid out as a block.
- An event list (`event T`) is an append-only list of T, an object or a leaf. Items are numbered from 0. An item's leaves never change once written, but its own event lists can grow.
- Plain fields (leaves and nested objects) are set when their object is written and never change. Anything that changes over time is an event list, and its current value is its latest item.
- A version is the state of the root after one append. Version 0 is the empty root. Version 1 writes the root's own leaves. Every later append adds one item to one event list.
- A path names any value: `sessions.12.turns.4999.reply`. Stored paths use item numbers from 0. In lookups, negative numbers count back from the latest item (-1).
- Order is file order, across the whole tree. Each append's timestamp is its commit time and never decreases.

## 2. Schema language

| Syntax | Meaning |
|---|---|
| `object name { … }` | A reusable object type |
| `root name { … }` | The file's root. Exactly one. |
| `field: T,` | A member. Trailing commas are allowed. |
| `str`, `num`, `enum(a, b)`, `path` | Leaves. A `path` holds the path of an earlier value. |
| `\|str` | A block leaf |
| `T?` | Optional, for leaves and nested objects |
| `{ … }` | An inline object |
| `event T` | An append-only list of T |
| `# text` | A description: on lines before an object or a field, or after a field on its first line |

- Names match `[A-Za-z_][A-Za-z0-9_]*`. `num` values match `-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?`. Enum words are names.
- Types may be defined in any order. No type may contain itself, directly or through event lists, so the tree's depth is fixed by the schema. An unknown type name is an error.
- Descriptions are part of the schema, and APIs and viewers expose them.

```
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
```

## 3. File layout

```
trak 1.2                               magic line
##[ schema ]##<VLQs>                   schema header, then the schema text
<appends>                              each a body, then ==[ <path> <version> <timestamp> ]==<VLQs>
```

Lines end with LF. Lengths and offsets are in bytes.

| Marker | Line |
|---|---|
| `##[ schema ]##` | Schema header |
| `==[ … ]==` | Append footer |
| `--[ path ]--` | Block start, inside a body |
| `--[ /path ]--` | Block end, inside a body |

**Schema.** The header line is `##[ schema ]##` followed by VLQs: the schema text's length, the root append's length (version 1, body and footer), then an extension count E and E VLQs. The schema text follows it and ends with LF, and the root append follows that, so a reader finds the root's leaves in one read. It is the only schema: a file's schema never changes.

**Append.** A body, then a footer line. The body holds the item's leaves, in schema order, depth first:

| Leaf | Written as |
|---|---|
| Inline | `path: value` LF |
| Block | `--[ path ]--` LF, value, LF, `--[ /path ]--` LF |
| Absent | Nothing |

The path is relative to the item, such as `context.used`. An item of a leaf list is written with the list's name as its path. The root append writes the root's leaves.

**Footer.** `==[ <path> <version> <timestamp> ]==` followed by VLQs (§5). The path is the new item's path, such as `sessions.12.turns.4999`, or the root's name for version 1. The version is decimal. The timestamp is UTC, `YYYYMMDDTHHMMSS.sssZ`.

## 4. Encoding

Framing is ASCII except paths' item segments, which are decimal. Values are raw bytes, normally UTF-8.

**Letter VLQ.** Every machine number is an unsigned base-26 number written in letters, most significant digit first. a–z are 0–25 with more digits following; A–Z are 0–25 and end the number. A VLQ never starts with a. Examples: 0 = `A`, 25 = `Z`, 26 = `bA`, 1520 = `cgM`.

**References.** An append is referred to by one VLQ d: 0 means none, and d > 0 is the append whose footer line ends d − 1 bytes before this append's body starts. So d = 1 is the append just before.

**Pointers.** A pointer to a node is two VLQs, d and i. d = 0, i = 0 is none. d = 0, i > 0 is node i of this footer. d > 0, i > 0 is node i of append d.

## 5. Nodes

Each footer holds only the nodes that changed: the root, then each node on the path down to the new item. This is path copying: every unchanged part of the tree is reached through pointers to earlier nodes. Nodes are numbered from 1, root first, item last.

Footer VLQs, in order:

1. Body length.
2. Version jump: a reference to the append of version J(v) (§6), 0 for version 0.
3. Extension count E, then E VLQs.
4. The item's leaf lengths, in schema order. An optional leaf is 0 if absent, else 1 then its length. An optional nested object is 0 if absent, else 1 then its leaves.
5. The nodes.

A footer's nodes follow from its path and the schema, so they carry no type tags. A node exists for the root, for each event list's trie levels, for each item, and for each nested object that contains an event list.

**Object node.** Items and nested objects. The root node has only slots.

| # | Field | Meaning |
|---|---|---|
| 1 | k | This object's version count: 0 when created, +1 each time it changes |
| 2 | prev | Pointer to its previous version, none at creation |
| 3 | jump | Pointer to its version J(k), none when k = 0 |
| 4 | leaves | Items only: a reference to the append holding its leaves, 0 for this one |
| 5 | slots | One slot per event field and per nested object that contains an event list, in schema order |

A slot for an event field is a list node: the item count, then a pointer to the root of the list's item trie, none if empty. A slot for a nested object is a pointer to its object node, none until one of its lists first changes; its first node has k = 0.

**Leaf item node.** An item of a leaf list has an empty node: no VLQs. Its value is its append's body.

**Item trie.** A persistent vector with fanout 16. A list of c items has a trie of depth h, the smallest h ≥ 1 with 16^h ≥ c. Item m is found by its base-16 digits, most significant first. Entries at the bottom level point to the latest version of each item; entries above point to trie nodes.

**Forms.** Trie nodes and slots have one of two forms:

| Form | VLQs |
|---|---|
| 0, full | 0, the entry count n, then n entries |
| 1, delta | 1, a base pointer, a bitmap of the entries written here, then those entries |

In a delta, bit j of the bitmap set means entry j is written here; other entries come from the base. The base must be a full node of the same kind. A full slots form always holds every slot, so its n is the slot count. A delta may write an entry past its base's count, and then every entry before it is in the base or written. Bitmaps are VLQs.

## 6. Navigation

**Jump.** J(0) = 0. For n ≥ 1, with p = n − 1: J(n) = J(J(p)) if p > J(p) and p − J(p) = J(p) − J(J(p)), otherwise J(n) = p. To reach m < n, take the jump when its target is ≥ m, otherwise the previous one. Version jumps use the footer's reference; object versions use the node's prev and jump.

| Need | Method | Cost |
|---|---|---|
| Latest version | The last valid footer | O(1) |
| Version v | Version jumps from the latest | O(log n) |
| Version at time t | The same walk on timestamps | O(log n) |
| Previous version | The append just before this one | 1 read |
| Item m of a list | Trie walk from its list node | O(log₁₆ n) |
| Value at a path | Descend: slot, trie, item, at each level | O(depth · log₁₆ n) |
| Version k of one object | Its prev and jump pointers | O(log n) |
| Item count | Its list node | Free |
| An item's leaves | Its leaves reference, then lengths from that footer | 1 read |

A page of rows reads one footer and one body slice per visible row, plus the trie nodes above them.

## 7. Writer policy and bounds

Readers only follow pointers, so how a writer lays out nodes is a policy. Any policy is valid if every append meets these bounds, which check enforces:

- A footer holds exactly the nodes on the changed path.
- Every entry and slot the append didn't change equals the previous version's.
- A delta's base is a full node, so any node is at most two reads.
- Each append is one write.

The reference policy writes every node in full form, and every reference writer produces identical bytes with it. Other policies need only pass check, and every reader can read them.

**Writer rules.** Write the magic line, then the schema, then the root append, then appends. Keep timestamps non-decreasing. Validate num, enum and path values: a path must name a value that exists before the append. Never inspect str values.

## 8. Recovery

A crash can leave a torn append at the end of the file. A reader uses the last line that is a valid footer: it parses, its version follows the previous footer's, and its body walk ends at its footer start. Bytes after it are ignored, and a writer reopening the file truncates them.

## 9. Compatibility

| Reader meets | Reader does |
|---|---|
| A major other than 1 in the magic line | Refuses the file |
| A minor below 2 | Refuses the file; 1.0 and 1.1 were a different format |
| A newer minor | Reads it with these rules |
| E greater than it knows | Skips the extra VLQs |
| A node layout from any valid policy | Reads it; layouts are policy, not format |
