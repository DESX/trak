// Byte sources: where a reader gets its bytes. Every source has `size` and an async
// `read(offset, length)` returning a Uint8Array. Readers cache what they read in blocks, so a
// source only ever sees block-sized reads.

/** Bytes already in memory. */
export class BytesSource {
  constructor(bytes) {
    this.bytes = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
    this.size = this.bytes.length;
  }

  async read(offset, length) {
    return this.bytes.subarray(offset, offset + length);
  }

  async refresh() {
    return this.size;
  }
}

/** A browser File or Blob, such as one picked or dropped by the user. */
export class BlobSource {
  constructor(blob) {
    this.blob = blob;
    this.size = blob.size;
  }

  async read(offset, length) {
    return new Uint8Array(await this.blob.slice(offset, offset + length).arrayBuffer());
  }

  async refresh() {
    return this.size;
  }
}

/** A file served over HTTP with Range requests, such as by `trak serve`. */
export class HttpSource {
  /** Use `await HttpSource.open(url)`. */
  constructor(url, size, init) {
    this.url = url;
    this.size = size;
    this.init = init;
  }

  static async open(url, init = {}) {
    const s = new HttpSource(url, 0, init);
    await s.refresh();
    return s;
  }

  /** Re-read the file's size, to pick up appended objects. */
  async refresh() {
    const res = await fetch(this.url, { ...this.init, method: "HEAD", cache: "no-store" });
    if (!res.ok) throw new Error(`HEAD ${this.url}: ${res.status} ${res.statusText}`);
    this.size = Number(res.headers.get("content-length"));
    return this.size;
  }

  async read(offset, length) {
    if (length <= 0) return new Uint8Array(0);
    const headers = { ...(this.init.headers ?? {}), Range: `bytes=${offset}-${offset + length - 1}` };
    const res = await fetch(this.url, { ...this.init, headers, cache: "no-store" });
    if (res.status !== 206 && res.status !== 200) throw new Error(`GET ${this.url}: ${res.status} ${res.statusText}`);
    const buf = new Uint8Array(await res.arrayBuffer());
    return res.status === 200 ? buf.subarray(offset, offset + length) : buf;
  }
}

/** An LRU cache of fixed-size blocks over a source. */
export class Blocks {
  constructor(source, { blockSize = 1 << 16, maxBlocks = 512 } = {}) {
    this.source = source;
    this.blockSize = blockSize;
    this.maxBlocks = maxBlocks;
    this.cache = new Map();
    this.size = source.size;
    /** Number of block reads from the source, for tests and the viewer's status line. */
    this.fetches = 0;
  }

  /** Forget blocks at or past `offset`: they may have grown. */
  invalidateFrom(offset) {
    for (const k of [...this.cache.keys()]) {
      if ((k + 1) * this.blockSize > offset) this.cache.delete(k);
    }
  }

  async block(k) {
    let b = this.cache.get(k);
    if (b != null) {
      this.cache.delete(k);
      this.cache.set(k, b);
      return b;
    }
    const off = k * this.blockSize;
    const len = Math.min(this.blockSize, this.size - off);
    this.fetches++;
    b = this.source.read(off, len);
    this.cache.set(k, b);
    if (this.cache.size > this.maxBlocks) this.cache.delete(this.cache.keys().next().value);
    try {
      const bytes = await b;
      this.cache.set(k, Promise.resolve(bytes));
      return bytes;
    } catch (e) {
      this.cache.delete(k);
      throw e;
    }
  }

  /** Bytes `[offset, offset + length)`, clipped to the source. */
  async read(offset, length) {
    const end = Math.min(this.size, offset + length);
    if (end <= offset) return new Uint8Array(0);
    const first = Math.floor(offset / this.blockSize);
    const last = Math.floor((end - 1) / this.blockSize);
    if (first === last) {
      const b = await this.block(first);
      return b.subarray(offset - first * this.blockSize, end - first * this.blockSize);
    }
    const out = new Uint8Array(end - offset);
    const blocks = await Promise.all(Array.from({ length: last - first + 1 }, (_, i) => this.block(first + i)));
    blocks.forEach((b, i) => {
      const bstart = (first + i) * this.blockSize;
      const from = Math.max(offset, bstart);
      const to = Math.min(end, bstart + b.length);
      out.set(b.subarray(from - bstart, to - bstart), from - offset);
    });
    return out;
  }

  /** The offset of the last LF in `[lo, hi)`, or -1. */
  async lastLF(lo, hi) {
    let pos = hi;
    while (pos > lo) {
      const k = Math.floor((pos - 1) / this.blockSize);
      const bstart = Math.max(lo, k * this.blockSize);
      const b = await this.block(k);
      for (let i = pos - 1; i >= bstart; i--) {
        if (b[i - k * this.blockSize] === 10) return i;
      }
      pos = bstart;
    }
    return -1;
  }
}
