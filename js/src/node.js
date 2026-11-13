// A source over a local file, for Node.

import { open } from "node:fs/promises";

export class NodeFileSource {
  static async open(path) {
    const fh = await open(path, "r");
    const s = new NodeFileSource(fh, (await fh.stat()).size);
    return s;
  }
  constructor(fh, size) {
    this.fh = fh;
    this.size = size;
  }
  async readRanges(ranges) {
    return Promise.all(
      ranges.map(async ([o, n]) => {
        const buf = new Uint8Array(n);
        const { bytesRead } = await this.fh.read(buf, 0, n, o);
        return buf.subarray(0, bytesRead);
      }),
    );
  }
  async refresh() {
    this.size = (await this.fh.stat()).size;
    return this.size;
  }
  close() {
    return this.fh.close();
  }
}
