// A byte source over a local file, for Node.js. Browsers use BlobSource or HttpSource.

import { open } from "node:fs/promises";
import { Reader } from "./reader.js";

export class FileSource {
  constructor(handle, size) {
    this.handle = handle;
    this.size = size;
  }

  static async open(path) {
    const handle = await open(path, "r");
    return new FileSource(handle, (await handle.stat()).size);
  }

  async refresh() {
    this.size = (await this.handle.stat()).size;
    return this.size;
  }

  async read(offset, length) {
    const buf = new Uint8Array(length);
    const { bytesRead } = await this.handle.read(buf, 0, length, offset);
    return buf.subarray(0, bytesRead);
  }

  async close() {
    await this.handle.close();
  }
}

/** Open a local trak file. Close it with `reader.close()`. */
export async function openFile(path, options) {
  const source = await FileSource.open(path);
  try {
    return await Reader.open(source, options);
  } catch (e) {
    await source.close();
    throw e;
  }
}
