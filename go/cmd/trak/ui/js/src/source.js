// Byte sources and the block cache. A source has `size` and
// `readRanges([[offset, length], …])`, returning one Uint8Array per range. The cache
// batches every block wanted in the same turn of the event loop into one readRanges
// call, so a page of rows costs a few requests, not hundreds.

/** Bytes in memory. */
export class BytesSource {
  constructor(bytes) {
    this.bytes = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
    this.size = this.bytes.length;
  }
  async readRanges(ranges) {
    return ranges.map(([o, n]) => this.bytes.subarray(o, o + n));
  }
  async refresh() {
    return this.size;
  }
}

/** A browser File or Blob. */
export class BlobSource {
  constructor(blob) {
    this.blob = blob;
    this.size = blob.size;
  }
  async readRanges(ranges) {
    return Promise.all(ranges.map(async ([o, n]) => new Uint8Array(await this.blob.slice(o, o + n).arrayBuffer())));
  }
  async refresh() {
    return this.size;
  }
}

/** A file on any HTTP server with Range support: one request per range. */
export class HttpSource {
  constructor(url, size, init = {}) {
    this.url = url;
    this.size = size;
    this.init = init;
  }
  static async open(url, init = {}) {
    const s = new HttpSource(url, 0, init);
    await s.refresh();
    return s;
  }
  async refresh() {
    const res = await fetch(this.url, { ...this.init, method: "HEAD", cache: "no-store" });
    if (!res.ok) throw new Error(`HEAD ${this.url}: ${res.status} ${res.statusText}`);
    this.size = Number(res.headers.get("content-length"));
    return this.size;
  }
  async readRanges(ranges) {
    return Promise.all(
      ranges.map(async ([o, n]) => {
        const headers = { ...(this.init.headers ?? {}), Range: `bytes=${o}-${o + n - 1}` };
        const res = await fetch(this.url, { ...this.init, headers, cache: "no-store" });
        if (res.status !== 206 && res.status !== 200) throw new Error(`GET ${this.url}: ${res.status}`);
        const buf = new Uint8Array(await res.arrayBuffer());
        return res.status === 200 ? buf.subarray(o, o + n) : buf;
      }),
    );
  }
}

/**
 * A file served by `trak serve`: many ranges in one request to its read endpoint,
 * `GET {api}?path=P&r=off+len,off+len…`, which answers with the ranges concatenated.
 */
export class ServeSource {
  constructor(api, path, size) {
    this.api = api;
    this.path = path;
    this.size = size;
  }
  static async open(api, path) {
    const s = new ServeSource(api, path, 0);
    await s.refresh();
    return s;
  }
  async refresh() {
    const res = await fetch(`${this.api}?path=${encodeURIComponent(this.path)}&r=`, { cache: "no-store" });
    if (!res.ok) throw new Error(`${this.path}: ${res.status} ${await res.text()}`);
    this.size = Number(res.headers.get("x-trak-size"));
    return this.size;
  }
  async readRanges(ranges) {
    const out = [];
    // Keep URLs short enough for any proxy.
    for (let i = 0; i < ranges.length; i += 200) {
      const part = ranges.slice(i, i + 200);
      const q = part.map(([o, n]) => `${o}+${n}`).join(",");
      const res = await fetch(`${this.api}?path=${encodeURIComponent(this.path)}&r=${q}`, { cache: "no-store" });
      if (!res.ok) throw new Error(`${this.path}: ${res.status} ${await res.text()}`);
      const buf = new Uint8Array(await res.arrayBuffer());
      let pos = 0;
      for (const [, n] of part) {
        out.push(buf.subarray(pos, pos + n));
        pos += n;
      }
      if (pos !== buf.length) throw new Error(`${this.path}: the server sent ${buf.length} bytes, not ${pos}`);
    }
    return out;
  }
}

// A fast macrotask: after every microtask of this turn, so all of them batch.
const later =
  typeof setImmediate === "undefined" && typeof MessageChannel !== "undefined"
    ? (() => {
        const ch = new MessageChannel();
        const queue = [];
        ch.port1.onmessage = () => queue.shift()();
        return (fn) => {
          queue.push(fn);
          ch.port2.postMessage(0);
        };
      })()
    : (fn) => setImmediate(fn);

/**
 * An LRU cache of fixed-size blocks over a source. `log` receives every request:
 * { ranges: [[offset, length], …], bytes, ms }.
 */
export class Blocks {
  constructor(source, { blockSize = 1 << 14, maxBytes = 64 << 20, log = null } = {}) {
    this.source = source;
    this.blockSize = blockSize;
    this.maxBlocks = Math.max(16, Math.floor(maxBytes / blockSize));
    this.cache = new Map(); // block number → Uint8Array
    this.waiting = new Map(); // block number → [resolve, reject][]
    this.queued = false;
    this.size = source.size;
    this.log = log;
    this.requests = 0;
    this.bytesRead = 0;
  }

  /** Forget blocks at or past offset, which may have grown. */
  invalidateFrom(offset) {
    for (const k of [...this.cache.keys()]) if ((k + 1) * this.blockSize > offset) this.cache.delete(k);
  }

  block(k) {
    const b = this.cache.get(k);
    if (b) {
      this.cache.delete(k);
      this.cache.set(k, b);
      return b;
    }
    return new Promise((ok, fail) => {
      let w = this.waiting.get(k);
      if (!w) this.waiting.set(k, (w = []));
      w.push([ok, fail]);
      if (!this.queued) {
        this.queued = true;
        later(() => this.flush());
      }
    });
  }

  async flush() {
    this.queued = false;
    const want = [...this.waiting.keys()].sort((a, b) => a - b);
    const waiting = this.waiting;
    this.waiting = new Map();
    // Contiguous blocks become one range.
    const runs = [];
    for (const k of want) {
      const r = runs[runs.length - 1];
      if (r && r[1] === k) r[1] = k + 1;
      else runs.push([k, k + 1]);
    }
    const ranges = runs.map(([a, b]) => {
      const off = a * this.blockSize;
      return [off, Math.min(b * this.blockSize, this.size) - off];
    });
    const t0 = performance.now();
    let parts;
    try {
      parts = await this.source.readRanges(ranges);
    } catch (e) {
      for (const w of waiting.values()) for (const [, fail] of w) fail(e);
      return;
    }
    const bytes = ranges.reduce((s, r) => s + r[1], 0);
    this.requests++;
    this.bytesRead += bytes;
    this.log?.({ ranges, bytes, ms: performance.now() - t0 });
    runs.forEach(([a, b], i) => {
      for (let k = a; k < b; k++) {
        const blk = parts[i].subarray((k - a) * this.blockSize, (k - a + 1) * this.blockSize);
        this.cache.set(k, blk);
        for (const [ok] of waiting.get(k) ?? []) ok(blk);
      }
    });
    while (this.cache.size > this.maxBlocks) this.cache.delete(this.cache.keys().next().value);
  }

  /** Bytes [offset, offset + length), clipped to the source. */
  async read(offset, length) {
    const end = Math.min(this.size, offset + length);
    if (end <= offset) return new Uint8Array(0);
    const bs = this.blockSize;
    const first = Math.floor(offset / bs);
    const last = Math.floor((end - 1) / bs);
    if (first === last) {
      const b = await this.block(first);
      return b.subarray(offset - first * bs, end - first * bs);
    }
    const blocks = await Promise.all(Array.from({ length: last - first + 1 }, (_, i) => this.block(first + i)));
    const out = new Uint8Array(end - offset);
    blocks.forEach((b, i) => {
      const bstart = (first + i) * bs;
      const from = Math.max(offset, bstart);
      const to = Math.min(end, bstart + b.length);
      out.set(b.subarray(from - bstart, to - bstart), from - offset);
    });
    return out;
  }

  /** The line ending at `end` (after its LF), at most `max` bytes, not before `lo`: [start, bytes]. */
  async lineBefore(end, lo, max = 1 << 20) {
    let n = Math.min(this.blockSize, end - lo);
    for (;;) {
      const from = end - n;
      const b = await this.read(from, n);
      for (let i = b.length - 2; i >= 0; i--) if (b[i] === 10) return [from + i + 1, b.subarray(i + 1)];
      if (from <= lo) return [from, b];
      if (n >= max) throw new Error("line too long");
      n = Math.min(n * 4, end - lo);
    }
  }
}
