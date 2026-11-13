import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { Blocks, BytesSource, NotFoundError, TrakFile, decodeVLQs, depth, encodeVLQ, jump, parseSchema } from "../src/index.js";

const vec = (name) => readFileSync(fileURLToPath(new URL(`../../conformance/valid/${name}.trak`, import.meta.url)));

test("VLQs, jumps and depths", () => {
  for (const [n, s] of [[0, "A"], [25, "Z"], [26, "bA"], [1520, "cgM"]]) {
    assert.equal(encodeVLQ(n), s);
    assert.deepEqual(decodeVLQs(new TextEncoder().encode(s), 0, s.length), [n]);
  }
  for (let n = 0; n < 5000; n += 7) assert.deepEqual(decodeVLQs(new TextEncoder().encode(encodeVLQ(n)), 0, encodeVLQ(n).length), [n]);
  assert.throws(() => decodeVLQs(new TextEncoder().encode("aB"), 0, 2), /leading zero/);
  assert.throws(() => decodeVLQs(new TextEncoder().encode("b"), 0, 1), /final capital/);
  assert.deepEqual([1, 2, 3, 4, 5, 6, 7].map(jump), [0, 1, 0, 3, 4, 3, 0]);
  // The jump walk reaches any earlier number in O(log n) steps.
  for (const [n, m] of [[1000, 1], [99999, 4321], [7, 6]]) {
    let cur = n;
    let steps = 0;
    while (cur > m) {
      cur = jump(cur) >= m ? jump(cur) : cur - 1;
      steps++;
    }
    assert.equal(cur, m);
    assert.ok(steps < 4 * Math.log2(n) + 4, `${steps} steps from ${n} to ${m}`);
  }
  assert.deepEqual([1, 16, 17, 256, 257, 4096, 4097].map(depth), [1, 1, 2, 2, 3, 3, 4]);
});

test("schema errors carry positions, descriptions are kept", () => {
  const s = parseSchema("# The root.\nroot r {\n  a: str,   # an a\n  b: event { c: num },\n}\n");
  assert.equal(s.root.desc, "The root.");
  assert.equal(s.root.field("a").desc, "an a");
  assert.equal(s.root.slots.length, 1);
  assert.throws(() => parseSchema("root r { a: strr }"), /unknown type strr/);
  assert.throws(() => parseSchema("root r { a: str, a: num }"), /defined twice \(line 1, column 18\)/);
  assert.throws(() => parseSchema("object o { x: o }\nroot r { o: o }"), /contains itself/);
  assert.throws(() => parseSchema("object o { x: num }"), /no root/);
});

test("versions, items and lookups by time", async () => {
  const f = await TrakFile.open(new BytesSource(vec("005-trie-growth")));
  assert.equal(f.version, 310);
  const counts = await f.get("counts");
  assert.equal(counts.length, 300);
  assert.equal(await (await counts.item(299)).text(), "299");
  assert.equal(await (await counts.item(-1)).text(), "299");
  assert.equal(await (await f.get("counts.-300")).text(), "0");
  await assert.rejects(counts.item(300), NotFoundError);
  await assert.rejects(f.get("nope"), NotFoundError);
  const items = await counts.range(10, 50);
  assert.deepEqual(await Promise.all(items.map((x) => x.text())), Array.from({ length: 40 }, (_, i) => String(10 + i)));
  // Old versions: each append is one.
  for (const v of [1, 2, 3, 100, 310]) {
    const c = await f.get("counts", v);
    const a = await f.appendAt(v);
    assert.equal(a.version, v);
    assert.ok(c.length <= Math.max(0, v - 1));
  }
  // Every version's timestamp is one second after the last.
  const a = await f.appendAt(200);
  assert.equal(await f.versionAt(a.ms), 200);
  assert.equal(await f.versionAt(a.ms - 1), 200);
  assert.equal(await f.versionAt(0), 1);
  assert.equal(await f.versionAt(a.ms + 1e9), 0);
});

test("object versions step back through prev and jump", async () => {
  const f = await TrakFile.open(new BytesSource(vec("004-nested")));
  const limits = await f.get("config.limits");
  assert.equal(await limits.k(), 2);
  const v0 = await limits.at(0);
  assert.deepEqual([(await v0.list("soft")).length, (await v0.list("hard")).length], [1, 0]);
  const root = await f.get("");
  assert.equal(await root.k(), f.version - 1);
  assert.equal((await root.at(0)).version, 1);
  const item = await f.get("items.0");
  assert.equal(await item.k(), 2);
  assert.equal((await (await item.at(1)).get("sub.log")).length, 1);
  assert.equal(await (await f.get("items.1.extra.why")).text(), "because\nof this");
  assert.equal((await f.get("items.0.extra.why")).present, false);
  assert.equal((await f.get("items.0.meta.size")).present, false);
});

test("a growing file: refresh finds new appends and ignores a torn tail", async () => {
  const full = vec("005-trie-growth");
  let size = 2000;
  const src = {
    get size() {
      return size;
    },
    async readRanges(ranges) {
      return ranges.map(([o, n]) => full.subarray(o, Math.min(o + n, size)));
    },
    async refresh() {
      return size;
    },
  };
  const f = await TrakFile.open(src);
  const v1 = f.version;
  assert.ok(v1 > 1 && f.size <= 2000);
  size = full.length - 5; // the last append torn
  assert.equal(await f.refresh(), true);
  assert.equal(f.version, 309);
  size = full.length;
  assert.equal(await f.refresh(), true);
  assert.equal(f.version, 310);
  assert.equal(await f.refresh(), false);
});

test("reads in one turn of the event loop go out as one request", async () => {
  const bytes = vec("005-trie-growth");
  const log = [];
  const blocks = new Blocks(new BytesSource(bytes), { blockSize: 256, log: (r) => log.push(r) });
  await Promise.all([blocks.read(0, 10), blocks.read(5000, 10), blocks.read(300, 600), blocks.read(9000, 1)]);
  assert.equal(log.length, 1);
  assert.deepEqual(log[0].ranges, [[0, 1024], [4864, 256], [8960, 256]]);
  await blocks.read(0, 100); // cached
  assert.equal(log.length, 1);
  const [start, line] = await blocks.lineBefore(bytes.length, 0);
  assert.equal(new TextDecoder().decode(line).slice(0, 4), "==[ ");
  assert.equal(start + line.length, bytes.length);
});
