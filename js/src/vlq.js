// Letter VLQs (SPEC §4) and the jump function (SPEC §6).

import { FormatError } from "./errors.js";

const MAX = 2 ** 53;

/** Decode the VLQs in bytes[from, to). `offset` is bytes[0]'s file offset, for errors. */
export function decodeVLQs(bytes, from, to, offset = 0) {
  const out = [];
  let v = 0;
  let started = false;
  for (let i = from; i < to; i++) {
    const c = bytes[i];
    let d;
    if (c >= 65 && c <= 90) d = c - 65;
    else if (c >= 97 && c <= 122) {
      d = c - 97;
      if (!started && d === 0) throw new FormatError("VLQ has a leading zero digit", offset + i);
    } else throw new FormatError(`bad VLQ letter ${JSON.stringify(String.fromCharCode(c))}`, offset + i);
    v = v * 26 + d;
    if (v >= MAX) throw new FormatError("VLQ is too large for this reader", offset + i);
    if (c <= 90) {
      out.push(v);
      v = 0;
      started = false;
    } else started = true;
  }
  if (started) throw new FormatError("VLQ ends without a final capital letter", offset + to);
  return out;
}

/** Encode one VLQ. */
export function encodeVLQ(v) {
  let s = String.fromCharCode(65 + (v % 26));
  v = Math.floor(v / 26);
  while (v > 0) {
    s = String.fromCharCode(97 + (v % 26)) + s;
    v = Math.floor(v / 26);
  }
  return s;
}

/** J(n): n − J(n) is the smallest term of n's greedy skew-binary decomposition. */
export function jump(n) {
  if (n === 0) return 0;
  let r = n;
  for (;;) {
    let t = 1;
    while (t * 2 + 1 <= r) t = t * 2 + 1;
    if (t === r) return n - t;
    r -= t;
  }
}

/** The trie depth of a list of c items: the smallest h ≥ 1 with 16^h ≥ c. */
export function depth(c) {
  let h = 1;
  for (let cap = 16; cap < c; cap *= 16) h++;
  return h;
}
