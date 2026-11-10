// Reading trak files: navigation, tables, families and events (SPEC §7, §8, §9, §11).
//
// Every read goes through a byte source, so the same code reads a dropped File, a file
// served over HTTP, or bytes in memory. Nothing loads the whole file: opening reads about
// 2K footers, and a row costs its footer plus one contiguous read (SPEC §12).

import { FormatError, SchemaError, TrakError, UnsupportedVersion, ValueError } from "./errors.js";
import { Footer, isFooterStart, parseFooter } from "./footer.js";
import { jump } from "./jump.js";
import { buildSchema } from "./schema.js";
import { BytesSource, Blocks } from "./source.js";
import { parseStamp, toMillis } from "./time.js";
import { Walk } from "./values.js";

const MAGIC = /^trak (0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/;
const CACHE_SIZE = 4096;
const BELOW = -Infinity;
const latin1 = new TextDecoder("latin1");
const utf8 = new TextDecoder("utf-8");

export const start = (f) => (f.isSchema ? BELOW : f.start);
export const stampMillis = (f) => (f.isSchema ? BELOW : parseStamp(f.stamp));

export class Reader {
  /** Use `await Reader.open(source)`. */
  constructor(source, options = {}) {
    this.source = source;
    this.blocks = new Blocks(source, options);
    this.cache = new Map();
    this.latest = new Map();
    /** Number of footer lookups so far, for navigation bound tests. */
    this.footerReads = 0;
  }

  /** Open a file from a byte source, or from bytes (a Uint8Array or ArrayBuffer). */
  static async open(source, options) {
    if (source instanceof Uint8Array || source instanceof ArrayBuffer) source = new BytesSource(source);
    const r = new Reader(source, options);
    await r.load();
    return r;
  }

  async load() {
    const size = this.source.size;
    this.blocks.size = size;
    if (size === 0) throw new FormatError("empty file: missing magic line", 0);
    const head = await this.blocks.read(0, 64);
    const nl = head.indexOf(10);
    const m = nl >= 0 ? MAGIC.exec(latin1.decode(head.subarray(0, nl))) : null;
    if (m == null) throw new FormatError("bad magic line, expected 'trak MAJOR.MINOR'", 0);
    if (m[1] !== "1") throw new UnsupportedVersion(`unsupported major version ${m[1]}`, 5);
    this.version = `${m[1]}.${m[2]}`;
    this.magicEnd = nl + 1;
    this.size = size;
    // Footers are near the end: fetch the tail in one go.
    await this.blocks.read(Math.max(0, size - (1 << 16)), 1 << 16);
    const [end, chain] = await this.recover(size);
    this.end = end;
    this.schemaFooters = chain;
    this.latest = new Map();
    this.schemas = await this.buildSchemas(chain);
    this.byName = new Map(this.schemas.map((s) => [s.name, s]));
    // Where each type's first child schema ends: from there on, its events carry their
    // number in its own family (SPEC §6).
    this.childFrom = new Map();
    this.schemas.forEach((s, i) => {
      if (s.parent != null && !this.childFrom.has(s.parent)) this.childFrom.set(s.parent, chain[i].end);
    });
  }

  /** Pick up appended objects. Returns true when anything changed. */
  async refresh() {
    const size = (await this.source.refresh?.()) ?? this.source.size;
    if (size === this.size) return false;
    const oldEnd = this.end;
    for (const k of [...this.cache.keys()]) if (k > oldEnd) this.cache.delete(k);
    this.blocks.invalidateFrom(Math.min(this.size, oldEnd));
    await this.load();
    return this.end !== oldEnd;
  }

  async close() {
    await this.source.close?.();
  }

  // --- public API -------------------------------------------------------------------

  /** Every schema, in type index order. */
  types() {
    return [...this.schemas];
  }

  schema(name) {
    const s = this.byName.get(name);
    if (s == null) throw new TrakError(`no type named '${name}'`);
    return s;
  }

  table(name) {
    return new Table(this, this.schema(name));
  }

  /** The events of a type and all its descendants, in file order (SPEC §1). */
  family(name) {
    return new Family(this, this.schema(name));
  }

  /** Every event in file order. Reads every footer first. */
  async *events() {
    const footers = [];
    for (let f = await this.last(); f != null; f = await this.prev(f)) footers.push(f);
    for (let i = footers.length - 1; i >= 0; i--) {
      if (!footers[i].isSchema) yield this.event(footers[i]);
    }
  }

  /** Every event, newest first, one footer read each. */
  async *eventsBackward() {
    for (let f = await this.last(); f != null; f = await this.prev(f)) {
      if (!f.isSchema) yield this.event(f);
    }
  }

  /**
   * Up to `limit` events that start before `cursor`, newest first. A cursor is an event's
   * `offset`; null means the end of the file. Pass the last event returned for the next page.
   */
  async before(cursor = null, limit = 100) {
    const c = cursor == null ? this.end : await this.cursor(cursor);
    const out = [];
    let f = c === this.magicEnd ? null : await this.footer(c);
    while (f != null && out.length < limit) {
      if (!f.isSchema) out.push(this.event(f));
      f = await this.prev(f);
    }
    return out;
  }

  /**
   * Up to `limit` events that start after `cursor`, oldest first. A cursor is an event's
   * `offset`; null means the start of the file. Pass the last event returned for the next page.
   */
  async after(cursor = null, limit = 100) {
    const c = cursor == null ? -1 : await this.cursor(cursor);
    if (limit <= 0) return [];
    // Logged content can look like footers, so the objects after c can't be found by
    // scanning forward. Seek each type for its first event after c, then walk back from a
    // point with at least `limit` events before it.
    let bound = this.end;
    await this.latestAll();
    for (const s of this.schemas) {
      const top = await this.latestFooter(s);
      if (top.isSchema) continue;
      const first = await this.seek(s, start, c + 1);
      if (first != null && first.node + limit - 1 <= top.node) {
        bound = Math.min(bound, (await this.navigate(top, first.node + limit - 1)).end);
      }
    }
    const found = [];
    let f = bound === this.magicEnd ? null : await this.footer(bound);
    while (f != null && f.start > c) {
      if (!f.isSchema) found.push(f);
      f = await this.prev(f);
    }
    return found.slice(Math.max(0, found.length - limit)).reverse().map((g) => this.event(g));
  }

  /** Check that `c` is where an object starts (SPEC §11's footer test). */
  async cursor(c) {
    if (c === this.magicEnd || c === this.end) return c;
    if (!(Number.isInteger(c) && c > this.magicEnd && c < this.end)) {
      throw new ValueError(`cursor ${c} is outside the file's valid objects`);
    }
    try {
      const f = await this.footer(c);
      const schema = this.byName.get(f.name);
      if (schema == null) throw new FormatError(`unknown type '${f.name}'`, f.fstart);
      if (!f.isSchema && schema.root != null) new Walk(schema.root, f.values, f.start, f.fstart);
    } catch (e) {
      if (e instanceof FormatError) throw new ValueError(`cursor ${c} is not the start of an object`);
      throw e;
    }
    return c;
  }

  event(f) {
    const schema = this.byName.get(f.name);
    if (schema == null) throw new FormatError(`event of type '${f.name}' has no schema`, f.fstart);
    return new Event(this, schema, f);
  }

  /** Bytes `[offset, offset + length)` of the file. */
  read(offset, length) {
    return this.blocks.read(offset, length);
  }

  // --- footers and navigation -------------------------------------------------------

  async footer(end) {
    this.footerReads++;
    let f = this.cache.get(end);
    if (f != null) {
      this.cache.delete(end);
      this.cache.set(end, f);
      return f;
    }
    if (end <= this.magicEnd || end > this.size) throw new FormatError("pointer doesn't land on the end of an object", end);
    const last = await this.blocks.read(end - 1, 1);
    if (last[0] !== 10) throw new FormatError("pointer doesn't land on the end of an object", end);
    const fstart = (await this.blocks.lastLF(this.magicEnd - 1, end - 1)) + 1;
    const line = await this.blocks.read(fstart, end - 1 - fstart);
    f = parseFooter(line, fstart, this.magicEnd);
    this.cache.set(end, f);
    if (this.cache.size > CACHE_SIZE) this.cache.delete(this.cache.keys().next().value);
    return f;
  }

  last() {
    return this.end === this.magicEnd ? null : this.footer(this.end);
  }

  prev(f) {
    return f.start === this.magicEnd ? null : this.footer(f.start);
  }

  follow(f, pointer) {
    if (pointer === 0) throw new FormatError("required pointer is 0", f.fstart);
    return this.footer(f.target(pointer));
  }

  async recover(size) {
    let pos = size;
    /** Why the last complete footer line in the ignored tail is invalid, if there is one. */
    this.tailError = null;
    if (pos > this.magicEnd && (await this.blocks.read(pos - 1, 1))[0] !== 10) {
      pos = (await this.blocks.lastLF(this.magicEnd - 1, pos)) + 1;
    }
    while (pos > this.magicEnd) {
      const fstart = (await this.blocks.lastLF(this.magicEnd - 1, pos - 1)) + 1;
      const head = await this.blocks.read(fstart, 4);
      if (isFooterStart(head, 0)) {
        try {
          return [pos, await this.validateTail(pos)];
        } catch (e) {
          if (!(e instanceof FormatError || e instanceof SchemaError)) throw e;
          this.cache.delete(pos);
          this.tailError ??= e;
        }
      }
      pos = fstart;
    }
    return [this.magicEnd, []];
  }

  /** A footer is valid by SPEC §11 if it parses, its type is known, and its VLQ count and
   * walk match its schema. Returns the schema chain it sees. */
  async validateTail(end) {
    const f = await this.footer(end);
    const chain = await this.schemaChain(f);
    if (!f.isSchema) {
      if (!chain.some((c) => c.name === f.name)) throw new FormatError(`event of type '${f.name}' has no schema`, f.fstart);
      const schema = (await this.buildSchemas(chain)).find((s) => s.name === f.name);
      if (schema.root != null) new Walk(schema.root, f.values, f.start, f.fstart);
    }
    return chain;
  }

  async schemaText(f) {
    return latin1.decode(await this.blocks.read(f.start, f.fstart - f.start));
  }

  async buildSchemas(footers) {
    const out = [];
    const byName = new Map();
    for (const f of footers) {
      const s = buildSchema(f.name, f.num, f.stamp, await this.schemaText(f), { types: byName });
      out.push(s);
      byName.set(s.name, s);
    }
    return out;
  }

  /** Every schema footer visible from `f`, in index order: walk back to a schema or an event
   * with dir type 0, then follow previous-schema pointers (SPEC §7). */
  async schemaChain(f) {
    let g = f;
    while (g != null && !g.isSchema) {
      if (g.vlqs[4] === 0) {
        g = await this.follow(g, g.vlqs[3]);
        if (!g.isSchema) throw new FormatError("directory pointer for the schema chain misses a schema", g.end);
        break;
      }
      g = await this.prev(g);
    }
    if (g == null) throw new FormatError("event with no schema before it", f.fstart);
    const chain = [g];
    while (chain[chain.length - 1].vlqs[1]) {
      const top = chain[chain.length - 1];
      g = await this.follow(top, top.vlqs[1]);
      if (!g.isSchema) throw new FormatError("previous-schema pointer misses a schema", g.end);
      chain.push(g);
    }
    chain.reverse();
    const names = new Set();
    chain.forEach((s, i) => {
      if (s.num !== i + 1) throw new FormatError(`schema ${s.name} has index ${s.num}, expected ${i + 1}`, s.fstart);
      if (names.has(s.name)) throw new FormatError(`duplicate schema name ${s.name}`, s.fstart);
      names.add(s.name);
    });
    return chain;
  }

  /** The latest node of a type: its last event, or its schema if it has none. */
  async latestFooter(schema) {
    let f = this.latest.get(schema.index);
    if (f != null) return f;
    for (f = await this.last(); f != null; f = await this.prev(f)) {
      if (f.name === schema.name) break;
      if (!f.isSchema && f.vlqs[4] === schema.index) {
        f = await this.follow(f, f.vlqs[3]);
        if (f.name !== schema.name) throw new FormatError("directory pointer leads to the wrong type", f.end);
        break;
      }
    }
    if (f == null) throw new FormatError(`type ${schema.name} not found`, this.end);
    this.latest.set(schema.index, f);
    return f;
  }

  /** The latest node of every type in one walk back: each event names its type and points
   * at its dir type's latest node, so about 2K events cover every type (SPEC §7). */
  async latestAll() {
    const latest = this.latest;
    const want = this.schemas.length;
    for (let f = await this.last(); f != null && latest.size < want; f = await this.prev(f)) {
      const s = this.byName.get(f.name);
      if (s != null && !latest.has(s.index)) latest.set(s.index, f);
      if (!f.isSchema) {
        const dt = f.vlqs[4];
        if (dt && !latest.has(dt) && dt <= want) {
          const g = await this.follow(f, f.vlqs[3]);
          if (g.name !== this.schemas[dt - 1].name) throw new FormatError("directory pointer leads to the wrong type", g.end);
          latest.set(dt, g);
        }
      }
    }
    return latest;
  }

  /** Follow `f`'s jump or step pointer, checking that it lands on the right node. */
  async hop(f, useJump) {
    const want = useJump ? jump(f.node) : f.node - 1;
    const g = await this.follow(f, useJump ? f.vlqs[2] : f.vlqs[1]);
    if (g.name !== f.name || g.node !== want) throw new FormatError(`pointer leads to the wrong node (wanted ${f.name} ${want})`, f.fstart);
    return g;
  }

  /** From node `f` walk down to node `m` of the same type (SPEC §7). */
  async navigate(f, m) {
    while (f.node > m) f = await this.hop(f, jump(f.node) >= m);
    return f;
  }

  /** The first event of a type whose key is `>= m`, for a key that never decreases along the
   * chain (SPEC §7, Seek); null when there is none. */
  async seek(schema, key, m) {
    let f = await this.latestFooter(schema);
    if (f.isSchema || key(f) < m) return null;
    while (f.node > 1) {
      if (jump(f.node) > 0) {
        const g = await this.hop(f, true);
        if (key(g) >= m) {
          f = g;
          continue;
        }
      }
      const g = await this.hop(f, false);
      if (key(g) < m) break;
      f = g;
    }
    return f;
  }

  /** An event's number in one of its families (SPEC §6, Family numbers). */
  familyNumber(f, family) {
    const schema = this.byName.get(f.name);
    const level = schema.lineage.indexOf(family);
    if (level < 0) throw new TrakError(`${f.name} is not in family '${family}'`);
    if (level === schema.lineage.length - 1) {
      const from = this.childFrom.get(f.name);
      if (from == null || from > f.start) return f.num;
    }
    if (level >= f.vlqs[5]) throw new FormatError(`${f.name} event has no number in family ${family}`, f.fstart);
    return f.vlqs[6 + level];
  }
}

/** The events of one type, numbered from 1. */
export class Table {
  constructor(reader, schema) {
    this.reader = reader;
    this.schema = schema;
  }

  get name() {
    return this.schema.name;
  }

  async count() {
    return (await this.reader.latestFooter(this.schema)).node;
  }

  async latest() {
    const f = await this.reader.latestFooter(this.schema);
    return f.isSchema ? null : new Event(this.reader, this.schema, f);
  }

  async footerAt(n) {
    const top = await this.reader.latestFooter(this.schema);
    if (!(Number.isInteger(n) && n >= 1 && n <= top.node)) throw new RangeError(`${this.name} has no event ${n}`);
    return this.reader.navigate(top, n);
  }

  async get(n) {
    return new Event(this.reader, this.schema, await this.footerAt(n));
  }

  /** Events `a` to `b` inclusive, descending when `a > b`. */
  async *range(a, b) {
    const count = await this.count();
    for (const x of [a, b]) if (!(x >= 1 && x <= count)) throw new RangeError(`${this.name} has no event ${x}`);
    const r = this.reader;
    if (a > b) {
      let f = await this.footerAt(a);
      while (f != null && f.node >= b) {
        yield new Event(r, this.schema, f);
        f = f.node > b ? await r.follow(f, f.vlqs[1]) : null;
      }
      return;
    }
    for (let lo = a; lo <= b; ) {
      const hi = Math.min(b, lo + 255);
      const chunk = [await this.footerAt(hi)];
      while (chunk[chunk.length - 1].node > lo) {
        const g = chunk[chunk.length - 1];
        chunk.push(await r.navigate(g, g.node - 1));
      }
      for (let i = chunk.length - 1; i >= 0; i--) yield new Event(r, this.schema, chunk[i]);
      lo = hi + 1;
    }
  }

  /** The first event with timestamp `>= ts` (a trak timestamp, RFC 3339 text, Date or
   * milliseconds), or null. O(log n). */
  async atTime(ts) {
    const f = await this.reader.seek(this.schema, stampMillis, toMillis(ts));
    return f == null ? null : new Event(this.reader, this.schema, f);
  }
}

/** The events of a type and its descendants, numbered from 1 in file order (SPEC §1). */
export class Family {
  constructor(reader, schema) {
    this.reader = reader;
    this.schema = schema;
    /** The family's types: the type itself first, then its descendants in index order. */
    this.types = reader.schemas.filter((s) => s.lineage.includes(schema.name));
    this.key = (f) => (f.isSchema ? BELOW : reader.familyNumber(f, schema.name));
  }

  get name() {
    return this.schema.name;
  }

  async tops() {
    if (this.types.length > 1) await this.reader.latestAll();
    const out = [];
    for (const s of this.types) {
      const f = await this.reader.latestFooter(s);
      if (!f.isSchema) out.push(f);
    }
    return out;
  }

  async count() {
    return Math.max(0, ...(await this.tops()).map(this.key));
  }

  async latest() {
    const tops = await this.tops();
    if (tops.length === 0) return null;
    return this.reader.event(tops.reduce((a, b) => (this.key(b) > this.key(a) ? b : a)));
  }

  /** Row `m`: a seek in each of the family's types, O(k log n). */
  async get(m) {
    if (m >= 1) {
      for (const s of this.types) {
        const f = await this.reader.seek(s, this.key, m);
        if (f != null && this.key(f) === m) return new Event(this.reader, s, f);
      }
    }
    throw new RangeError(`family ${this.name} has no row ${m}`);
  }

  /** Rows `a` to `b` inclusive, descending when `a > b`: one range per type, merged. */
  async *range(a, b) {
    const count = await this.count();
    for (const x of [a, b]) if (!(x >= 1 && x <= count)) throw new RangeError(`family ${this.name} has no row ${x}`);
    const lo = Math.min(a, b);
    const hi = Math.max(a, b);
    const runs = [];
    for (const s of this.types) {
      const first = await this.reader.seek(s, this.key, lo);
      if (first == null || this.key(first) > hi) continue;
      const after = await this.reader.seek(s, this.key, hi + 1);
      const t = new Table(this.reader, s);
      const last = (after != null ? after.node : (await t.count()) + 1) - 1;
      runs.push(a <= b ? t.range(first.node, last) : t.range(last, first.node));
    }
    // Merge the runs: keep each run's next event, and yield the smallest (or largest) key.
    const heads = [];
    for (const run of runs) {
      const x = await run.next();
      if (!x.done) heads.push({ run, e: x.value });
    }
    const sign = a <= b ? 1 : -1;
    while (heads.length > 0) {
      let best = 0;
      for (let i = 1; i < heads.length; i++) {
        if (sign * (this.key(heads[i].e.footer) - this.key(heads[best].e.footer)) < 0) best = i;
      }
      const h = heads[best];
      yield h.e;
      const x = await h.run.next();
      if (x.done) heads.splice(best, 1);
      else h.e = x.value;
    }
  }

  /** The first row with timestamp `>= ts`, or null. Timestamps never decrease within a
   * family, so it's the earliest of each type's first such event. */
  async atTime(ts) {
    const want = toMillis(ts);
    let best = null;
    for (const s of this.types) {
      const f = await this.reader.seek(s, stampMillis, want);
      if (f != null && (best == null || this.key(f) < this.key(best))) best = f;
    }
    return best == null ? null : this.reader.event(best);
  }
}

/** One event. Its body is read once, on the first call that needs it. */
export class Event {
  constructor(reader, schema, footer) {
    this.reader = reader;
    this.schema = schema;
    this.footer = footer;
    this._walk = null;
    this._body = null;
  }

  get type() { return this.footer.name; }
  get n() { return this.footer.num; }
  get timestamp() { return this.footer.stamp; }
  /** Where the event starts; usable as a cursor. */
  get offset() { return this.footer.start; }
  /** The byte after the event's footer. */
  get end() { return this.footer.end; }
  get millis() { return parseStamp(this.footer.stamp); }

  /** The previous event of the same type, in one read, or null for event 1. */
  async prev() {
    if (this.footer.num === 1) return null;
    return new Event(this.reader, this.schema, await this.reader.hop(this.footer, false));
  }

  /** This event's row number in `family`, its own type or an ancestor; the default is the
   * root family. */
  familyNumber(family = this.schema.lineage[0]) {
    return this.reader.familyNumber(this.footer, family);
  }

  /** Offsets of every leaf, from the footer alone: no body bytes are read. */
  walk() {
    if (this._walk == null) {
      const root = this.schema.root;
      if (root == null) throw new SchemaError(`values of ${this.type} are unavailable: ${this.schema.error?.message}`);
      this._walk = new Walk(root, this.footer.values, this.footer.start, this.footer.fstart);
    }
    return this._walk;
  }

  /** The event's body bytes, from its start to its footer, in one read. */
  async body() {
    this._body ??= await this.reader.read(this.footer.start, this.footer.fstart - this.footer.start);
    return this._body;
  }

  /** Every present leaf as `{ path, value, field }` (strings), in file order. Values are
   * decoded as UTF-8; use `raw()` for the bytes. */
  async leaves() {
    const w = this.walk();
    const body = await this.body();
    const base = this.footer.start;
    return w.leaves.map((rec) => ({
      path: utf8.decode(body.subarray(rec.pathOff - base, rec.pathOff - base + rec.plen)),
      value: utf8.decode(body.subarray(rec.voff - base, rec.voff - base + rec.length)),
      leaf: rec.leaf,
      length: rec.length,
    }));
  }

  /** A leaf's value as text, or null when absent. The first leaf wins if paths collide. */
  async get(path) {
    for (const l of await this.leaves()) if (l.path === path) return l.value;
    return null;
  }

  /** A leaf's value bytes, or null when absent. */
  async raw(path) {
    const w = this.walk();
    const body = await this.body();
    const base = this.footer.start;
    const want = new TextEncoder().encode(path);
    for (const rec of w.leaves) {
      const p = body.subarray(rec.pathOff - base, rec.pathOff - base + rec.plen);
      if (p.length === want.length && p.every((b, i) => b === want[i])) {
        return body.subarray(rec.voff - base, rec.voff - base + rec.length);
      }
    }
    return null;
  }

  /** The whole value as plain JSON: leaves are strings, absent values null, objects keep
   * schema order, and maps are `[key, value]` pairs with a null key when the entry has no
   * leaves (SPEC §6). */
  async native() {
    const w = this.walk();
    const body = await this.body();
    const base = this.footer.start;
    const text = (rec) => utf8.decode(body.subarray(rec.voff - base, rec.voff - base + rec.length));
    const conv = (node) => {
      if (node == null) return null;
      if (typeof node === "number") return text(w.leaves[node]);
      if (node.kind === "empty") return node.value;
      if (node.kind === "object") {
        const out = {};
        for (const [k, v] of Object.entries(node.members)) out[k] = conv(v);
        return out;
      }
      if (node.kind === "list") return node.items.map(conv);
      return node.entries.map(({ klen, first, value }) => {
        let key = null;
        if (first != null) {
          const off = w.leaves[first].pathOff + node.plen + 1 - base;
          key = utf8.decode(body.subarray(off, off + klen));
        }
        return [key, conv(value)];
      });
    };
    return conv(w.tree);
  }
}

export { Footer };
