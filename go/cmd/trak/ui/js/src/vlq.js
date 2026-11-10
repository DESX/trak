// Letter VLQ: base-26 numbers written in letters (SPEC §4).

import { FormatError } from "./errors.js";

/**
 * Decode concatenated VLQs from `bytes[from, to)`. `offset` locates `bytes[0]` in the file
 * for error messages. Values above 2^53 can't be held exactly in a JavaScript number and
 * are rejected; no real file has lengths or offsets that large.
 */
export function decodeVLQs(bytes, from = 0, to = bytes.length, offset = 0) {
  const out = [];
  let v = 0;
  let started = false;
  for (let i = from; i < to; i++) {
    const c = bytes[i];
    if (c >= 65 && c <= 90) {
      v = v * 26 + (c - 65);
      if (v > Number.MAX_SAFE_INTEGER) throw new FormatError("VLQ is too large", offset + i);
      out.push(v);
      v = 0;
      started = false;
    } else if (c >= 97 && c <= 122) {
      if (!started && c === 97) throw new FormatError("VLQ has a leading zero digit", offset + i);
      v = v * 26 + (c - 97);
      if (v > Number.MAX_SAFE_INTEGER) throw new FormatError("VLQ is too large", offset + i);
      started = true;
    } else {
      throw new FormatError(`bad VLQ letter '${String.fromCharCode(c)}'`, offset + i);
    }
  }
  if (started) throw new FormatError("VLQ ends without a final capital letter", offset + to);
  return out;
}

/** Encode one value. */
export function encodeVLQ(n) {
  let s = String.fromCharCode(65 + (n % 26));
  n = Math.floor(n / 26);
  while (n > 0) {
    s = String.fromCharCode(97 + (n % 26)) + s;
    n = Math.floor(n / 26);
  }
  return s;
}
