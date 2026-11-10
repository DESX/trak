// Full validation of a trak file, beyond what readers need (SPEC everywhere; DECISIONS I5, I6).

import { FormatError, SchemaError } from "./errors.js";
import { jump } from "./jump.js";
import { Reader } from "./reader.js";
import { buildSchema, isLeaf, parentName } from "./schema.js";
import { BytesSource } from "./source.js";
import { parseStamp } from "./time.js";
import { Walk } from "./values.js";

const NUM = /^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$/;
const TYPE_NAME = /^[A-Za-z_][A-Za-z0-9_.]*$/;
const latin1 = new TextDecoder("latin1");
const enc = new TextEncoder();

/**
 * Validate a whole file and return its number of objects. Throws FormatError on the first
 * problem, with its byte offset. A torn tail is allowed only when it holds no complete
 * footer line. Takes a Reader, a byte source, or bytes.
 */
export async function check(input) {
  const r = input instanceof Reader ? input : await Reader.open(input instanceof Uint8Array ? new BytesSource(input) : input);
  if (r.tailError != null) {
    const e = r.tailError;
    if (e instanceof FormatError) throw new FormatError(`ignored tail has an invalid footer: ${e.message0}`, e.offset);
    throw new FormatError(`ignored tail has an invalid footer: ${e.message}`, r.end);
  }
  return new Checker(r).run();
}

class Checker {
  constructor(reader) {
    this.r = reader;
    this.types = new Map();
    this.order = [];
    this.lastSchemaEnd = 0;
  }

  async run() {
    const footers = [];
    for (let f = await this.r.last(); f != null; f = await this.r.prev(f)) footers.push(f);
    footers.reverse();
    for (const f of footers) {
      if (f.isSchema) await this.schema(f);
      else await this.event(f);
    }
    return footers.length;
  }

  pointer(f, value, targetEnd, what) {
    const want = f.start - targetEnd + 1;
    if (value !== want) throw new FormatError(`${what} pointer is ${value}, expected ${want}`, f.fstart);
  }

  async schema(f) {
    if (!TYPE_NAME.test(f.name)) throw new FormatError(`bad type name '${f.name}'`, f.fstart);
    if (this.types.has(f.name)) throw new FormatError(`duplicate schema name '${f.name}'`, f.fstart);
    if (f.num !== this.order.length + 1) throw new FormatError(`schema index ${f.num}, expected ${this.order.length + 1}`, f.fstart);
    if (this.order.length === 0) {
      if (f.vlqs[1] !== 0) throw new FormatError("first schema's previous-schema pointer must be 0", f.fstart);
    } else {
      this.pointer(f, f.vlqs[1], this.lastSchemaEnd, "previous-schema");
    }
    const body = await this.r.read(f.start, f.fstart - f.start);
    if (body[body.length - 1] !== 10) throw new FormatError("schema body must end with LF", f.fstart);
    const [major, minor] = f.stamp.split(".");
    if (major !== "1") throw new FormatError(`unsupported schema version ${f.stamp}`, f.fstart);
    // 1.0 and 1.1 are parsed strictly; newer minors leniently (DECISIONS I5).
    let strict = minor === "0" || minor === "1";
    const parent = parentName(f.name, f.stamp);
    if (parent != null) {
      const p = this.types.get(parent);
      if (p == null) throw new FormatError(`schema ${f.name} comes before its parent ${parent}`, f.fstart);
      p.hasChild = true;
      if (p.schema.root == null) strict = false;
    }
    const bad = body.findIndex((b) => b > 127);
    if (bad >= 0) throw new FormatError("schema is not ASCII", f.start + bad);
    const types = new Map([...this.types].map(([n, t]) => [n, t.schema]));
    let schema;
    try {
      schema = buildSchema(f.name, f.num, f.stamp, latin1.decode(body), { strict, types });
    } catch (e) {
      if (e instanceof SchemaError) throw new FormatError(`schema ${f.name}: ${e.message}`, f.start);
      throw e;
    }
    const state = { schema, ends: [f.end], lastMs: -Infinity, familyCount: 0, hasChild: false };
    this.types.set(f.name, state);
    this.order.push(state);
    this.lastSchemaEnd = f.end;
  }

  async event(f) {
    const t = this.types.get(f.name);
    if (t == null) throw new FormatError(`event of type '${f.name}' before its schema`, f.fstart);
    const n = t.ends.length;
    if (f.num !== n) throw new FormatError(`${f.name} event number ${f.num}, expected ${n}`, f.fstart);
    const ms = parseStamp(f.stamp);
    if (ms == null) throw new FormatError(`invalid timestamp ${f.stamp}`, f.fstart);
    const lineage = t.schema.lineage.map((a) => this.types.get(a));
    const family = lineage[0];
    if (ms < family.lastMs) {
      throw new FormatError(`${family.hasChild ? `family ${family.schema.name}` : f.name} timestamp goes backwards`, f.fstart);
    }
    const levels = t.hasChild ? lineage : lineage.slice(0, -1);
    const want = levels.map((a) => a.familyCount + 1);
    const got = f.vlqs.slice(6, 6 + Math.min(f.vlqs[5], want.length));
    if (got.length !== want.length || got.some((v, i) => v !== want[i])) {
      throw new FormatError(`${f.name} family numbers are [${got.join(", ")}], expected [${want.join(", ")}]`, f.fstart);
    }
    this.pointer(f, f.vlqs[1], t.ends[n - 1], "step");
    this.pointer(f, f.vlqs[2], t.ends[jump(n)], "jump");
    const dt = f.vlqs[4];
    if (dt > this.order.length) throw new FormatError(`dir type ${dt} is not defined yet`, f.fstart);
    const dirEnd = dt === 0 ? this.lastSchemaEnd : this.order[dt - 1].ends[this.order[dt - 1].ends.length - 1];
    this.pointer(f, f.vlqs[3], dirEnd, "dir");
    const root = t.schema.root;
    if (root != null) {
      const walk = new Walk(root, f.values, f.start, f.fstart);
      const body = await this.r.read(f.start, f.fstart - f.start);
      this.framing(root, walk.tree, new Uint8Array(0), walk, body, f.start);
    }
    t.ends.push(f.end);
    family.lastMs = ms;
    for (const a of lineage) a.familyCount++;
  }

  /** Check the bytes around each value: paths, separators and block labels. */
  framing(t, node, path, walk, body, base) {
    if (node == null) return;
    const join = (a, b) => {
      const out = new Uint8Array(a.length + (a.length ? 1 : 0) + b.length);
      out.set(a);
      if (a.length) out[a.length] = 46;
      out.set(b, a.length + (a.length ? 1 : 0));
      return out;
    };
    if (isLeaf(t)) {
      this.leaf(walk.leaves[node], path, body, base);
    } else if (t.kind === "object") {
      for (const m of t.members) this.framing(m.type, node.members[m.key], join(path, enc.encode(m.key)), walk, body, base);
    } else if (t.kind === "list") {
      node.items.forEach((item, i) => {
        if (item.kind !== "empty") this.framing(t.elem, item, join(path, enc.encode(String(i))), walk, body, base);
      });
    } else {
      for (const { klen, first, value } of node.entries) {
        if (first == null) continue;
        const off = walk.leaves[first].pathOff + path.length + 1 - base;
        this.framing(t.value, value, join(path, body.subarray(off, off + klen)), walk, body, base);
      }
    }
  }

  leaf(rec, path, body, base) {
    const at = (o) => body[o - base];
    const same = (o, bytes) => bytes.every((b, i) => at(o + i) === b);
    const end = rec.voff + rec.length;
    const show = latin1.decode(path);
    if (rec.leaf.block) {
      const label = new Uint8Array([...enc.encode("\n--[ "), ...path, ...enc.encode(" ]--\n")]);
      if (!same(end, label)) throw new FormatError(`block label for '${show}' doesn't match`, end);
    } else {
      const head = new Uint8Array([...path, 58, 32]);
      if (!same(rec.start, head)) throw new FormatError(`inline leaf doesn't start with '${show}: '`, rec.start);
      if (at(end) !== 10) throw new FormatError("inline leaf doesn't end with LF", end);
    }
    const value = latin1.decode(body.subarray(rec.voff - base, end - base));
    const leaf = rec.leaf;
    if (leaf.unknown == null && leaf.kind === "num" && !NUM.test(value)) throw new FormatError(`bad num value '${value}'`, rec.voff);
    if (leaf.kind === "enum" && !leaf.words.includes(value)) throw new FormatError(`bad enum value '${value}'`, rec.voff);
  }
}
