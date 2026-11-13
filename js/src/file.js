// Reading a trak file (SPEC §6, §8). A port of go/file.go and go/object.go: every read
// is async and goes through the block cache.

import { FormatError, NotFoundError, UnsupportedVersionError } from "./errors.js";
import { NodeKind, NONE, isNone, parseFooter, written } from "./format.js";
import { Kind, parseSchema } from "./schema.js";
import { Blocks } from "./source.js";
import { decodeVLQs, depth, jump } from "./vlq.js";

const dec = new TextDecoder();

export class TrakFile {
  /** Open a file over a source. Options: { blockSize, maxBytes, log }. */
  static async open(source, opts = {}) {
    const f = new TrakFile(new Blocks(source, opts));
    await f.readHeader();
    await f.findLatest(source.size);
    return f;
  }

  constructor(blocks) {
    this.blocks = blocks;
    this.schema = null;
    this.schemaEnd = 0;
    this.rootEnd = 0;
    this.size = 0;
    this.latest = null;
    this.magic = "";
    this.cache = new Map(); // end → Promise<footer>
  }

  get version() {
    return this.latest ? this.latest.version : 0;
  }

  async readHeader() {
    const head = await this.blocks.read(0, Math.min(this.blocks.size, 1 << 16));
    const nl = head.indexOf(10);
    if (nl < 0 || dec.decode(head.subarray(0, 5)) !== "trak ") throw new FormatError("not a trak file", 0);
    const ver = dec.decode(head.subarray(5, nl));
    const m = /^(\d+)\.(\d+)$/.exec(ver);
    if (!m) throw new FormatError(`bad version ${JSON.stringify(ver)}`, 5);
    if (m[1] !== "1" || Number(m[2]) < 2) throw new UnsupportedVersionError(ver);
    this.magic = `trak ${ver}`;
    const tag = "##[ schema ]##";
    const end = head.indexOf(10, nl + 1);
    if (end < 0 || dec.decode(head.subarray(nl + 1, nl + 1 + tag.length)) !== tag) throw new FormatError("expected the schema header", nl + 1);
    const vals = decodeVLQs(head, nl + 1 + tag.length, end, 0);
    if (vals.length < 3 || vals[2] !== vals.length - 3) throw new FormatError("the schema header holds two lengths, then an extension count and its VLQs", nl + 1);
    const textStart = end + 1;
    if (vals[0] === 0 || vals[0] > this.blocks.size - textStart) throw new FormatError(`schema length ${vals[0]} runs past the end of the file`, nl + 1);
    const text = await this.blocks.read(textStart, vals[0]);
    if (text[text.length - 1] !== 10) throw new FormatError("the schema text must end with LF", textStart + text.length - 1);
    this.schema = parseSchema(dec.decode(text));
    this.schemaEnd = textStart + vals[0];
    if (vals[1] === 0 || vals[1] > this.blocks.size - this.schemaEnd) throw new FormatError(`root append length ${vals[1]} runs past the end of the file`, nl + 1);
    this.rootEnd = this.schemaEnd + vals[1];
  }

  /** Find the last valid footer at or before `size` (SPEC §8). */
  async findLatest(size) {
    this.latest = null;
    this.size = this.schemaEnd;
    let end = size;
    while (end > this.schemaEnd) {
      const lo = Math.max(this.schemaEnd, end - (1 << 16));
      const data = await this.blocks.read(lo, end - lo);
      let e = data.length;
      let stop = false;
      while (e > 0) {
        if (data[e - 1] !== 10) {
          e = data.lastIndexOf(10, e - 1) + 1;
          continue;
        }
        const s = e >= 2 ? data.lastIndexOf(10, e - 2) + 1 : 0;
        if (s === 0 && lo > this.schemaEnd) break;
        if (data[s] === 61 && data[s + 1] === 61 && data[s + 2] === 91) {
          try {
            const ft = await this.validFooter(lo + e);
            this.latest = ft;
            this.size = ft.end;
            return;
          } catch {
            // not a valid footer: keep looking
          }
        }
        e = s;
        if (s === 0) stop = true;
      }
      const first = data.indexOf(10);
      if (stop || lo === this.schemaEnd || first < 0) break;
      end = lo + first + 1;
    }
  }

  /** Re-read the source's size and find the latest version again. Returns true if it changed. */
  async refresh() {
    const before = this.latest?.end ?? 0;
    const size = await this.blocks.source.refresh();
    if (size === this.blocks.size) return false;
    this.blocks.invalidateFrom(Math.min(size, this.blocks.size) - 1);
    this.blocks.size = size;
    await this.findLatest(size);
    return (this.latest?.end ?? 0) !== before;
  }

  async validFooter(end) {
    const ft = await this.footer(end);
    await this.walkBody(ft);
    if (ft.version === 1) {
      if (ft.body !== this.schemaEnd || ft.end !== this.rootEnd) throw new FormatError("version 1 must follow the schema, with the length its header gives", ft.body);
      return ft;
    }
    if (ft.body < this.rootEnd) throw new FormatError(`version ${ft.version} has no append before it`, ft.body);
    const prev = await this.footer(ft.body);
    if (prev.version !== ft.version - 1) throw new FormatError(`version ${ft.version} follows version ${prev.version}`, ft.start);
    return ft;
  }

  /** Check a body's framing against its leaf lengths. */
  async walkBody(ft) {
    const body = await this.blocks.read(ft.body, ft.start - ft.body);
    const enc = new TextEncoder();
    const has = (pos, s) => {
      const b = enc.encode(s);
      for (let i = 0; i < b.length; i++) if (body[pos + i] !== b[i]) return false;
      return true;
    };
    let pos = 0;
    for (const p of ft.leaves) {
      if (!p.present) continue;
      if (p.field.block) {
        if (!has(pos, `--[ ${p.rel} ]--\n`)) throw new FormatError(`body: expected --[ ${p.rel} ]--`, ft.body + pos);
        pos = p.off + p.n;
        const close = `\n--[ /${p.rel} ]--\n`;
        if (!has(pos, close)) throw new FormatError(`body: expected the end of block ${p.rel}`, ft.body + pos);
        pos += close.length;
      } else {
        if (!has(pos, `${p.rel}: `)) throw new FormatError(`body: expected ${p.rel}:`, ft.body + pos);
        pos = p.off + p.n;
        if (body[pos] !== 10) throw new FormatError(`body: expected LF after ${p.rel}`, ft.body + pos);
        pos++;
      }
    }
    return body;
  }

  /** The parsed footer of the append ending at `end`. */
  footer(end) {
    let p = this.cache.get(end);
    if (p) return p;
    p = this.readFooter(end);
    this.cache.set(end, p);
    p.catch(() => this.cache.delete(end));
    if (this.cache.size > 100_000) {
      for (const k of [...this.cache.keys()].slice(0, 20_000)) this.cache.delete(k);
    }
    return p;
  }

  async readFooter(end) {
    if (end <= this.schemaEnd) throw new FormatError("no append ends here", end);
    const [start, line] = await this.blocks.lineBefore(end, this.schemaEnd);
    if (line[line.length - 1] !== 10) throw new FormatError("no append ends here", end);
    return parseFooter(line, 0, line.length, end, this.schema, this.schemaEnd);
  }

  async resolve(ref) {
    const ft = await this.footer(ref.end);
    if (ref.i > ft.nodes.length) throw new FormatError(`no node ${ref.i} in a footer of ${ft.nodes.length} nodes`, ft.start);
    return [ft.nodes[ref.i - 1], ft];
  }

  async base(n) {
    const [b] = await this.resolve(n.base);
    if (!b.full || b.kind !== n.kind || b.typ !== n.typ) throw new FormatError("a delta's base must be a full node of the same kind", n.base.end);
    return b;
  }

  /** Entry j of node n, following a delta to its base. */
  async entryAt(n, j) {
    const e = written(n, j);
    if (e || n.full) return e ?? { count: 0, ptr: NONE };
    return written(await this.base(n), j) ?? { count: 0, ptr: NONE };
  }

  /** Every entry of n, at least `min`. */
  async entriesOf(n, min = 0) {
    let out;
    if (n.full) out = [...n.entries];
    else {
      out = [...(await this.base(n)).entries];
      n.bits.forEach((j, k) => {
        while (out.length <= j) out.push({ count: 0, ptr: NONE });
        out[j] = n.entries[k];
      });
    }
    while (out.length < min) out.push({ count: 0, ptr: NONE });
    return out;
  }

  /** The footer of version v. */
  async footerAt(v) {
    if (!this.latest || v < 1 || v > this.latest.version) throw new NotFoundError(`version ${v} of ${this.version}`);
    if (v === 1) return this.footer(this.rootEnd);
    let cur = this.latest;
    while (cur.version > v) cur = await this.footer(jump(cur.version) >= v && cur.vjump ? cur.vjump : cur.body);
    return cur;
  }

  /** The first version written at or after ms, or 0. */
  async versionAt(ms) {
    if (!this.latest || this.latest.ms < ms) return 0;
    let cur = this.latest;
    while (cur.version > 1) {
      if (cur.vjump) {
        const j = await this.footer(cur.vjump);
        if (j.ms >= ms) {
          cur = j;
          continue;
        }
      }
      const prev = await this.footer(cur.body);
      if (prev.ms < ms) break;
      cur = prev;
    }
    return cur.version;
  }

  /** Describe the append ending at end: { version, path, ms, stamp, start, footer, end }. */
  async appendEndingAt(end) {
    return info(await this.footer(end));
  }

  async appendAt(v) {
    return info(await this.footerAt(v));
  }

  async root(v = this.version) {
    return this.rootOf(await this.footerAt(v));
  }

  rootOf(ft) {
    return new TObject(this, this.schema.root, "", { end: ft.end, i: 1 }, this.rootEnd, "", ft.version);
  }

  /** Resolve a path at version v: a TObject, TList or TLeaf. */
  async get(path, v = this.version) {
    return (await this.root(v)).get(path);
  }
}

function info(ft) {
  return { version: ft.version, path: ft.path, ms: ft.ms, stamp: ft.stamp, start: ft.body, footer: ft.start, end: ft.end };
}

/** One object of the tree at some version: the root, an item or a nested object. */
export class TObject {
  constructor(file, type, path, node, leaves, prefix, version) {
    this.file = file;
    this.type = type;
    this.path = path;
    this.node = node; // NONE for a nested object whose lists never changed
    this.leavesAt = leaves; // end of the append holding the leaves; 0: find the root's
    this.prefix = prefix;
    this.version = version;
  }

  child(name) {
    return this.path ? `${this.path}.${name}` : name;
  }

  describe() {
    return this.path || this.type.name;
  }

  /** The object's version count. */
  async k() {
    if (isNone(this.node)) return 0;
    const [n, ft] = await this.file.resolve(this.node);
    return n.kind === NodeKind.root ? ft.version - 1 : n.k;
  }

  /** The append that wrote this version of the object. */
  async written() {
    if (isNone(this.node)) return this.created();
    return this.file.appendEndingAt(this.node.end);
  }

  /** The append holding the object's leaves. */
  async created() {
    return this.file.appendEndingAt(await this.leavesEnd());
  }

  async leavesEnd() {
    if (!this.leavesAt) this.leavesAt = (await this.file.footerAt(1)).end;
    return this.leavesAt;
  }

  /** Version k of this object. */
  async at(k) {
    const cur = await this.k();
    if (k < 0 || k > cur) throw new NotFoundError(`version ${k} of ${this.describe()}, which has ${cur + 1}`);
    if (isNone(this.node)) return this;
    let [n, ft] = await this.file.resolve(this.node);
    if (n.kind === NodeKind.root) return this.file.root(k + 1);
    let r = this.node;
    while (n.k > k) {
      const next = n.k > 0 && jump(n.k) >= k && !isNone(n.jump) ? n.jump : n.prev;
      if (isNone(next)) throw new FormatError(`${this.describe()} has no version ${k}`, ft.start);
      r = next;
      [n, ft] = await this.file.resolve(r);
    }
    return new TObject(this.file, this.type, this.path, r, this.leavesAt, this.prefix, ft.version);
  }

  /** Resolve a path relative to this object. Negative item numbers count from the end. */
  async get(path) {
    let cur = this;
    for (const seg of path ? path.split(".") : []) {
      if (cur instanceof TObject) cur = await cur.field(seg);
      else if (cur instanceof TList) {
        if (!/^-?\d+$/.test(seg)) throw new NotFoundError(`${cur.path} needs an item number, not ${JSON.stringify(seg)}`);
        cur = await cur.item(Number(seg));
      } else throw new NotFoundError(`${path} goes past a leaf`);
    }
    return cur;
  }

  /** The named field: a TLeaf, TObject or TList. */
  async field(name) {
    const f = this.type.field(name);
    if (!f) throw new NotFoundError(`${this.describe()} has no field ${name}`);
    if (f.isLeaf) return this.leaf(name);
    if (f.kind === Kind.object) return this.nested(f);
    return this.list(name);
  }

  async nested(f) {
    let node = NONE;
    if (f.type.events && !isNone(this.node)) node = (await this.slotEntry(f.name)).ptr;
    return new TObject(this.file, f.type, this.child(f.name), node, this.leavesAt, `${this.prefix}${f.name}.`, this.version);
  }

  async slotEntry(name) {
    if (isNone(this.node)) return { count: 0, ptr: NONE };
    const [n] = await this.file.resolve(this.node);
    return this.file.entryAt(n, this.type.slot(name));
  }

  async list(name) {
    const f = this.type.field(name);
    if (!f || f.kind !== Kind.event) throw new NotFoundError(`${this.describe()} has no event list ${name}`);
    const e = await this.slotEntry(name);
    return new TList(this.file, f, this.child(name), e.count, e.ptr, this.version);
  }

  /** Every slot: event lists and nested objects with lists, in schema order. */
  async slots() {
    return Promise.all(this.type.slots.map((f) => (f.kind === Kind.event ? this.list(f.name) : this.nested(f))));
  }

  async leafFooter() {
    return this.file.footer(await this.leavesEnd());
  }

  /** A plain leaf; name may be dotted through nested objects. */
  async leaf(name) {
    const ft = await this.leafFooter();
    const rel = this.prefix + name;
    const p = ft.leaves.find((x) => x.rel === rel);
    if (p) return new TLeaf(this.file, p.field, this.child(name), p.present, ft.body + p.off, p.n, ft);
    if (this.type.field(name.split(".")[0])) return new TLeaf(this.file, null, this.child(name), false, 0, 0, ft);
    throw new NotFoundError(`${this.describe()} has no leaf ${name}`);
  }

  /** Every plain leaf, nested objects' included, in schema order. */
  async leaves() {
    const ft = await this.leafFooter();
    return ft.leaves
      .filter((p) => p.rel.startsWith(this.prefix))
      .map((p) => new TLeaf(this.file, p.field, this.child(p.rel.slice(this.prefix.length)), p.present, ft.body + p.off, p.n, ft));
  }
}

/** One event list at some version. */
export class TList {
  constructor(file, field, path, count, root, version) {
    this.file = file;
    this.field = field;
    this.path = path;
    this.count = count;
    this.root = root;
    this.version = version;
  }

  get length() {
    return this.count;
  }

  /** Item m: a TObject for object lists, a TLeaf for leaf lists. Negative m counts back. */
  async item(m) {
    if (m < 0) m += this.count;
    if (m < 0 || m >= this.count) throw new NotFoundError(`item ${m} of ${this.path}, which has ${this.count}`);
    return this.itemAt(m, await this.itemRef(m));
  }

  async itemAt(m, ref) {
    const path = `${this.path}.${m}`;
    const [n, ft] = await this.file.resolve(ref);
    if (this.field.item.kind !== Kind.object) {
      if (n.kind !== NodeKind.leafItem) throw new FormatError(`${path} should be a leaf item`, ft.start);
      const p = ft.leaves[0];
      return new TLeaf(this.file, p.field, path, true, ft.body + p.off, p.n, ft);
    }
    if (n.kind !== NodeKind.item) throw new FormatError(`${path} should be an item`, ft.start);
    return new TObject(this.file, this.field.item.type, path, ref, n.leaves, "", this.version);
  }

  /** The trie node at level h (0 = bottom) holding item m. */
  async trieRef(m, h) {
    let r = this.root;
    for (let lvl = depth(this.count) - 1; lvl > h; lvl--) {
      const [n, ft] = await this.file.resolve(r);
      if (n.kind !== NodeKind.trie) throw new FormatError(`expected a trie node for ${this.path}`, ft.start);
      r = (await this.file.entryAt(n, Math.floor(m / 16 ** lvl) % 16)).ptr;
      if (isNone(r)) throw new FormatError(`item ${m} of ${this.path} is missing`, ft.start);
    }
    return r;
  }

  async itemRef(m) {
    const r = await this.trieRef(m, 0);
    const [n, ft] = await this.file.resolve(r);
    const e = await this.file.entryAt(n, m % 16);
    if (isNone(e.ptr)) throw new FormatError(`item ${m} of ${this.path} is missing`, ft.start);
    return e.ptr;
  }

  /** Items lo ≤ m < hi, read in parallel: one bottom trie node per 16 items. */
  async range(lo, hi) {
    hi = Math.min(hi, this.count);
    if (hi <= lo) return [];
    const chunks = [];
    for (let c = Math.floor(lo / 16); c * 16 < hi; c++) chunks.push(c);
    const parts = await Promise.all(
      chunks.map(async (c) => {
        const [n] = await this.file.resolve(await this.trieRef(c * 16, 0));
        const es = await this.file.entriesOf(n, 16);
        const ms = [];
        for (let m = Math.max(lo, c * 16); m < Math.min(hi, c * 16 + 16); m++) ms.push(m);
        return Promise.all(ms.map((m) => this.itemAt(m, es[m % 16].ptr)));
      }),
    );
    return parts.flat();
  }
}

/** One leaf value in the file. */
export class TLeaf {
  constructor(file, field, path, present, off, len, footer) {
    this.file = file;
    this.field = field;
    this.path = path;
    this.present = present;
    this.off = off;
    this.len = len;
    this.footer = footer; // the append that wrote it
  }

  /** The value's bytes, at most max of them. */
  async bytes(max = Infinity) {
    if (!this.present) return null;
    return this.file.blocks.read(this.off, Math.min(this.len, max));
  }

  async text(max = Infinity) {
    const b = await this.bytes(max);
    return b == null ? null : dec.decode(b);
  }
}
