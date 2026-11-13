// Footers and nodes (SPEC §3–5). A port of go/format.go.

import { FormatError } from "./errors.js";
import { Kind } from "./schema.js";
import { decodeVLQs, depth } from "./vlq.js";

export const NodeKind = Object.freeze({ root: "root", item: "item", nested: "nested", leafItem: "leafItem", trie: "trie" });

/** A pointer to node i (from 1) of the append ending at `end`; NONE is the empty pointer. */
export const NONE = Object.freeze({ end: 0, i: 0 });
export const isNone = (r) => r.i === 0;
export const sameRef = (a, b) => a.end === b.end && a.i === b.i;

const TAG = [61, 61, 91, 32]; // "==[ "

const ascii = (bytes, from, to) => {
  let s = "";
  for (let i = from; i < to; i++) s += String.fromCharCode(bytes[i]);
  return s;
};

function isIndex(s) {
  return /^(0|[1-9][0-9]{0,18})$/.test(s);
}

/** The field of the item a footer path appends. Throws on a bad path. */
export function itemField(schema, segs) {
  let typ = schema.root;
  let item = null;
  for (let i = 0; i < segs.length; i++) {
    if (item) {
      if (item.kind !== Kind.object) throw new Error(`path ${segs.join(".")} goes past a leaf`);
      typ = item.type;
      item = null;
    }
    const f = typ.field(segs[i]);
    if (!f) throw new Error(`${segs.slice(0, i).join(".") || schema.root.name} has no field ${segs[i]}`);
    if (f.kind === Kind.object) {
      if (!f.type.events) throw new Error(`${segs.slice(0, i + 1).join(".")} holds no event lists`);
      typ = f.type;
    } else if (f.kind === Kind.event) {
      i++;
      if (i === segs.length || !isIndex(segs[i])) throw new Error(`path ${segs.join(".")} needs an item number after ${f.name}`);
      item = f.item;
    } else throw new Error(`${segs.slice(0, i + 1).join(".")} is a leaf, not an event list`);
  }
  if (!item) throw new Error(`path ${segs.join(".")} doesn't end at an item`);
  return item;
}

/**
 * Parse the footer line in bytes[from, to), where `to` is the file offset `end` (after the LF).
 * Returns { start, end, body, path, segs, version, ms, stamp, vjump, ext, item, leaves, nodes }.
 */
export function parseFooter(bytes, from, to, end, schema, schemaEnd) {
  const start = end - (to - from);
  if (to - from < 2 || bytes[to - 1] !== 10 || TAG.some((c, k) => bytes[from + k] !== c)) throw new FormatError("not a footer", start);
  let close = -1;
  for (let i = from + 4; i + 3 < to; i++) {
    if (bytes[i] === 32 && bytes[i + 1] === 93 && bytes[i + 2] === 61 && bytes[i + 3] === 61) {
      close = i;
      break;
    }
  }
  if (close < 0) throw new FormatError("footer has no ]==", start);
  const head = ascii(bytes, from + 4, close).split(" ");
  if (head.length !== 3) throw new FormatError("footer must hold a path, a version and a timestamp", start);
  const [path, vs, stamp] = head;
  if (!/^[1-9][0-9]*$/.test(vs)) throw new FormatError(`bad version ${JSON.stringify(vs)}`, start);
  const ms = parseStamp(stamp);
  if (ms == null) throw new FormatError(`bad timestamp ${JSON.stringify(stamp)}`, start);
  const vstart = start + (close + 4 - from);
  const vals = decodeVLQs(bytes, close + 4, to - 1, vstart - (close + 4));
  let pos = 0;
  const next = () => {
    if (pos >= vals.length) throw new FormatError("footer ends early", vstart);
    return vals[pos++];
  };
  const f = { start, end, path, segs: path.split("."), version: Number(vs), ms, stamp, body: 0, vjump: 0, ext: [], item: null, leaves: [], nodes: [] };
  const bodyLen = next();
  if (bodyLen > start - schemaEnd) throw new FormatError(`body length ${bodyLen} reaches before the first append`, vstart);
  f.body = start - bodyLen;
  const reference = () => {
    const d = next();
    if (d === 0) return 0;
    if (d - 1 > f.body - schemaEnd) throw new FormatError("reference reaches before the first append", vstart);
    return f.body - (d - 1);
  };
  const pointer = () => {
    const d = next();
    const i = next();
    if (d === 0 && i === 0) return NONE;
    if (i === 0) throw new FormatError("pointer to node 0", vstart);
    if (d === 0) return { end, i };
    if (d - 1 > f.body - schemaEnd) throw new FormatError("pointer reaches before the first append", vstart);
    return { end: f.body - (d - 1), i };
  };
  f.vjump = reference();
  let ne = next();
  if (ne > vals.length) throw new FormatError(`extension count ${ne} is too large`, vstart);
  for (; ne > 0; ne--) f.ext.push(next());

  // Leaf lengths, laid out as body positions.
  const readLeaves = () => {
    let off = 0;
    const add = (field, rel, present, n) => {
      const p = { field, rel, present, off: 0, n };
      if (present) {
        if (field.block) {
          p.off = off + rel.length + 9;
          off += 2 * rel.length + 20 + n;
        } else {
          p.off = off + rel.length + 2;
          off += rel.length + 3 + n;
        }
      }
      f.leaves.push(p);
    };
    const flag = (rel) => {
      const x = next();
      if (x > 1) throw new FormatError(`optional flag ${x} for ${rel}`, vstart);
      return x === 1;
    };
    const leaf = (field, rel) => {
      const present = field.optional ? flag(rel) : true;
      add(field, rel, present, present ? next() : 0);
    };
    const walk = (typ, prefix) => {
      for (const fld of typ.fields) {
        const rel = prefix + fld.name;
        if (fld.isLeaf) leaf(fld, rel);
        else if (fld.kind === Kind.object) {
          if (fld.optional && !flag(rel)) continue;
          walk(fld.type, rel + ".");
        }
      }
    };
    if (f.item.kind === Kind.object) walk(f.item.type, "");
    else leaf(f.item, f.item.name);
    if (off !== start - f.body) throw new FormatError(`leaf lengths give a body of ${off} bytes, not ${start - f.body}`, start);
  };

  const entries = (n, max, one) => {
    const form = next();
    if (form === 0) {
      n.full = true;
      const c = next();
      if (c > max) throw new FormatError(`${c} entries, at most ${max}`, vstart);
      n.entries = [];
      for (let j = 0; j < c; j++) n.entries.push(one(j));
    } else if (form === 1) {
      n.full = false;
      n.base = pointer();
      if (isNone(n.base) || n.base.end === end) throw new FormatError("a delta needs an earlier base", vstart);
      n.bitmap = next();
      if (n.bitmap >= 2 ** max) throw new FormatError(`bitmap names entries past ${max}`, vstart);
      n.entries = [];
      n.bits = [];
      for (let j = 0, b = n.bitmap; b > 0; j++, b = Math.floor(b / 2)) {
        if (b % 2 === 1) {
          n.bits.push(j);
          n.entries.push(one(j));
        }
      }
    } else throw new FormatError(`unknown node form ${form}`, vstart);
  };
  const objectNode = (kind, typ) => {
    const n = { kind, typ, k: 0, prev: NONE, jump: NONE, leaves: 0, full: true, base: NONE, bitmap: 0, entries: [], bits: null };
    if (kind !== NodeKind.root) {
      n.k = next();
      n.prev = pointer();
      n.jump = pointer();
    }
    if (kind === NodeKind.item) {
      const d = next();
      if (d === 0) n.leaves = end;
      else {
        if (d - 1 > f.body - schemaEnd) throw new FormatError("leaves reference reaches before the first append", vstart);
        n.leaves = f.body - (d - 1);
      }
    }
    entries(n, typ.slots.length, (j) => {
      const count = typ.slots[j].kind === Kind.event ? next() : 0;
      return { count, ptr: pointer() };
    });
    if (n.full && n.entries.length !== typ.slots.length) throw new FormatError(`a full slots form has ${n.entries.length} slots, not ${typ.slots.length}`, vstart);
    return n;
  };
  const trieNode = () => {
    const n = { kind: NodeKind.trie, typ: null, full: true, base: NONE, bitmap: 0, entries: [], bits: null };
    entries(n, 16, () => ({ count: 0, ptr: pointer() }));
    if (n.full && n.entries.length === 0) throw new FormatError("an empty trie node", vstart);
    return n;
  };

  if (f.version === 1) {
    if (f.segs.length !== 1 || f.segs[0] !== schema.root.name) throw new FormatError(`version 1 must write the root ${schema.root.name}`, start);
    f.item = { name: schema.root.name, kind: Kind.object, type: schema.root, isLeaf: false };
    readLeaves();
    f.nodes.push(objectNode(NodeKind.root, schema.root));
  } else {
    try {
      f.item = itemField(schema, f.segs);
    } catch (e) {
      throw new FormatError(e.message, start);
    }
    readLeaves();
    const root = objectNode(NodeKind.root, schema.root);
    f.nodes.push(root);
    let typ = schema.root;
    let parent = root;
    const segs = f.segs;
    for (let i = 0; i < segs.length; ) {
      const fld = typ.field(segs[i]);
      const e = written(parent, typ.slot(segs[i]));
      if (!e) throw new FormatError(`the slot for ${segs[i]} isn't written`, start);
      i++;
      if (fld.kind === Kind.object) {
        const n = objectNode(NodeKind.nested, fld.type);
        f.nodes.push(n);
        typ = fld.type;
        parent = n;
        continue;
      }
      const m = Number(segs[i]);
      if (m >= e.count) throw new FormatError(`item ${m} of ${segs.slice(0, i).join(".")} is past its count ${e.count}`, start);
      for (let h = depth(e.count); h > 0; h--) f.nodes.push(trieNode());
      i++;
      const last = i === segs.length;
      if (last && m !== e.count - 1) throw new FormatError(`a new item must be the last of ${segs.slice(0, i - 1).join(".")}`, start);
      if (last && fld.item.kind !== Kind.object) f.nodes.push({ kind: NodeKind.leafItem });
      else {
        const n = objectNode(NodeKind.item, fld.item.type);
        f.nodes.push(n);
        typ = fld.item.type;
        parent = n;
      }
    }
  }
  if (pos !== vals.length) throw new FormatError(`footer has ${vals.length - pos} VLQs after its nodes`, vstart);
  return f;
}

/** Entry j if node n writes it, else null. */
export function written(n, j) {
  if (n.full) return j < n.entries.length ? n.entries[j] : null;
  const k = n.bits.indexOf(j);
  return k < 0 ? null : n.entries[k];
}

/** Milliseconds since the epoch for a trak timestamp, or null. */
export function parseStamp(s) {
  const m = /^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})\.(\d{3})Z$/.exec(s);
  if (!m) return null;
  const [y, mo, d, h, mi, se, ms] = m.slice(1).map(Number);
  const t = Date.UTC(y, mo - 1, d, h, mi, se, ms);
  const back = new Date(t);
  if (back.getUTCFullYear() !== y || back.getUTCMonth() !== mo - 1 || back.getUTCDate() !== d || h > 23 || mi > 59 || se > 59) return null;
  return t;
}
