// Value walks: an event's VLQs over its schema give every leaf's offsets (SPEC §8).

import { FormatError } from "./errors.js";
import { isLeaf } from "./schema.js";

// Lists of empty objects cost no bytes in the file; bound them so a tiny file can't decode
// into a huge tree (DECISIONS I11).
export const MAX_EMPTY_ELEMENTS = 1 << 16;

function zeroSize(t) {
  return t.kind === "object" && !t.optional && t.members.every((m) => zeroSize(m.type));
}

function emptyValue(t) {
  return Object.fromEntries(t.members.map((m) => [m.key, emptyValue(m.type)]));
}

/**
 * The decoded tree mirrors the schema: leaves are indexes into `leaves`, absent values are
 * null, objects are `{ kind: "object", members: {key: node} }`, lists `{ kind: "list",
 * items }`, maps `{ kind: "map", plen, entries: [{ klen, first, value }] }` where `first`
 * is the index of the entry's first leaf, or null when it has none.
 *
 * Each leaf is `{ leaf, start, voff, length, plen, pathOff }`: `start` is where its bytes
 * begin, `voff` where its value begins, `pathOff` where its path is written.
 */
export class Walk {
  constructor(root, vals, start, end) {
    this.vals = vals;
    this.vi = 0;
    this.off = start;
    this.end = end;
    this.leaves = [];
    this.tree = this.object(root, -1);
    if (this.vi !== vals.length) throw new FormatError("event has more value VLQs than its schema uses", end);
    if (this.off !== end) throw new FormatError("value walk doesn't end at the footer", this.off);
  }

  next() {
    if (this.vi >= this.vals.length) throw new FormatError("event has fewer value VLQs than its schema needs", this.end);
    return this.vals[this.vi++];
  }

  object(t, plen) {
    const members = {};
    for (const m of t.members) members[m.key] = this.node(m.type, plen + 1 + m.key.length);
    return { kind: "object", members };
  }

  node(t, plen) {
    if (t.optional) {
      const flag = this.next();
      if (flag === 0) return null;
      if (flag !== 1) throw new FormatError("optional flag must be 0 or 1", this.end);
    }
    if (isLeaf(t)) {
      const length = this.next();
      const start = this.off;
      let voff;
      if (t.block) {
        voff = start;
        this.off = start + length + plen + 10;
      } else {
        voff = start + plen + 2;
        this.off = voff + length + 1;
      }
      if (this.off > this.end) throw new FormatError("value walk runs past the footer", start);
      const pathOff = t.block ? voff + length + 5 : start;
      this.leaves.push({ leaf: t, start, voff, length, plen, pathOff });
      return this.leaves.length - 1;
    }
    if (t.kind === "object") return this.object(t, plen);
    if (t.kind === "list") {
      const n = this.next();
      if (zeroSize(t.elem)) {
        if (n > MAX_EMPTY_ELEMENTS) throw new FormatError("list of empty objects is too long to decode", this.end);
        return { kind: "list", items: Array.from({ length: n }, () => ({ kind: "empty", value: emptyValue(t.elem) })) };
      }
      if (n > this.vals.length) throw new FormatError("list count exceeds the footer's VLQs", this.end);
      const items = [];
      for (let i = 0; i < n; i++) items.push(this.node(t.elem, plen + 1 + String(i).length));
      return { kind: "list", items };
    }
    const n = this.next();
    if (n > this.vals.length) throw new FormatError("map count exceeds the footer's VLQs", this.end);
    const entries = [];
    for (let i = 0; i < n; i++) {
      const klen = this.next();
      const first = this.leaves.length;
      const value = this.node(t.value, plen + 1 + klen);
      entries.push({ klen, first: this.leaves.length > first ? first : null, value });
    }
    return { kind: "map", plen, entries };
  }
}
