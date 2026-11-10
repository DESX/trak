// Footer lines: parsing (SPEC §5, §6, §8).

import { FormatError } from "./errors.js";
import { decodeVLQs } from "./vlq.js";

const DECIMAL = /^[1-9][0-9]*$/;
const TIMESTAMP = /^[0-9]{8}T[0-9]{6}\.[0-9]{3}Z$/;
const VERSION = /^[0-9]+\.[0-9]+$/;
const ascii = new TextDecoder("latin1");

/** "##[ " or "==[ " as bytes, for spotting footer lines. */
export function isFooterStart(b, i) {
  return (b[i] === 35 || b[i] === 61) && b[i + 1] === b[i] && b[i + 2] === 91 && b[i + 3] === 32;
}

/**
 * A parsed footer. `start` is the object's first byte, `fstart` the footer's first byte and
 * `end` the byte after the footer's LF. `num` is the type index (schema) or n (event);
 * `stamp` the version (schema) or timestamp (event).
 */
export class Footer {
  constructor(isSchema, start, fstart, end, name, num, stamp, vlqs) {
    Object.assign(this, { isSchema, start, fstart, end, name, num, stamp, vlqs });
  }

  /** Node number in its type's chain: 0 for the schema, n for an event. */
  get node() {
    return this.isSchema ? 0 : this.num;
  }

  /** Event value VLQs, after the extensions. */
  get values() {
    return this.vlqs.slice(6 + this.vlqs[5]);
  }

  /** Resolve a pointer stored in this footer to the target object's end offset. */
  target(pointer) {
    return this.start - (pointer - 1);
  }
}

/** Parse a footer line. `line` holds its bytes without the LF; it starts at file offset `fstart`. */
export function parseFooter(line, fstart, minStart) {
  const isSchema = line[0] === 35;
  if (!isFooterStart(line, 0)) throw new FormatError("line is not a footer", fstart);
  const closeByte = isSchema ? 35 : 61;
  let k = -1;
  for (let i = 4; i + 3 < line.length; i++) {
    if (line[i] === 32 && line[i + 1] === 93 && line[i + 2] === closeByte && line[i + 3] === closeByte) {
      k = i;
      break;
    }
  }
  if (k < 0) throw new FormatError("footer has no closing marker", fstart);
  const parts = ascii.decode(line.subarray(4, k)).split(" ");
  if (parts.length !== 3 || parts.some((p) => p === "")) throw new FormatError("footer needs exactly three fields", fstart + 4);
  if (k + 4 >= line.length) throw new FormatError("footer has no VLQs", fstart + k + 4);
  const vlqs = decodeVLQs(line, k + 4, line.length, fstart);
  const [name, numText, stamp] = parts;
  for (let i = 4; i < 4 + name.length; i++) {
    if (line[i] > 127) throw new FormatError("type name is not ASCII", fstart + 4);
  }
  if (!DECIMAL.test(numText)) throw new FormatError("bad decimal number in footer", fstart + 5 + name.length);
  const num = Number(numText);
  const stampAt = fstart + 6 + name.length + numText.length;
  if (isSchema) {
    if (!VERSION.test(stamp)) throw new FormatError("bad version in schema footer", stampAt);
    if (vlqs.length < 3 || vlqs.length !== 3 + vlqs[2]) throw new FormatError("schema footer has the wrong number of VLQs", fstart + k + 4);
  } else {
    if (!TIMESTAMP.test(stamp)) throw new FormatError("bad timestamp in event footer", stampAt);
    if (vlqs.length < 6 || vlqs.length < 6 + vlqs[5]) throw new FormatError("event footer has too few VLQs", fstart + k + 4);
  }
  const start = fstart - vlqs[0];
  if (start < minStart) throw new FormatError("body length reaches past the first object", fstart + k + 4);
  return new Footer(isSchema, start, fstart, fstart + line.length + 1, name, num, stamp, vlqs);
}
