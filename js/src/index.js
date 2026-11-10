// trak: a log format that programs write and read, and people can read too.
// This is the JavaScript reader. It reads through byte sources, so the same code works on a
// browser File, a file served over HTTP, or bytes in memory (see ./node.js for local files).

export { check } from "./check.js";
export { FormatError, SchemaError, TrakError, UnsupportedVersion, ValueError } from "./errors.js";
import { Reader } from "./reader.js";

export { Event, Family, Reader, Table } from "./reader.js";
export { buildSchema, flatten } from "./schema.js";
export { BlobSource, Blocks, BytesSource, HttpSource } from "./source.js";
export { formatStamp, parseStamp, toMillis } from "./time.js";

/** Open a trak file from a byte source or from bytes. */
export function open(source, options) {
  return Reader.open(source, options);
}
