// The reader API over each byte source: tables, families, seeks, cursors and refresh.

import assert from "node:assert/strict";
import { readFileSync, statSync } from "node:fs";
import { createServer } from "node:http";
import { test } from "node:test";
import { BlobSource, BytesSource, check, HttpSource, Reader, ValueError } from "../src/index.js";
import { openFile } from "../src/node.js";

const vector = (name) => new URL(`../../conformance/valid/${name}.trak`, import.meta.url);
const spec = readFileSync(vector("001-spec-example"));
const family = readFileSync(vector("027-family-deep"));

async function serve(files) {
  const server = createServer((req, res) => {
    const data = files[req.url];
    if (data == null) return res.writeHead(404).end();
    const m = /^bytes=(\d+)-(\d+)$/.exec(req.headers.range ?? "");
    if (req.method === "HEAD") return res.writeHead(200, { "content-length": data.length }).end();
    if (!m) return res.writeHead(200, { "content-length": data.length }).end(data);
    const [a, b] = [Number(m[1]), Math.min(Number(m[2]), data.length - 1)];
    res.writeHead(206, { "content-length": b - a + 1, "content-range": `bytes ${a}-${b}/${data.length}` });
    res.end(data.subarray(a, b + 1));
  });
  await new Promise((ok) => server.listen(0, "127.0.0.1", ok));
  return { server, base: `http://127.0.0.1:${server.address().port}` };
}

async function specExample(r) {
  assert.equal(r.version, "1.1");
  assert.deepEqual(r.types().map((s) => s.name), ["build", "deploy"]);
  const builds = r.table("build");
  assert.equal(await builds.count(), 2);
  const e = await builds.latest();
  assert.equal(await e.get("user"), "joel");
  assert.equal(await e.get("stdout"), "Compiling core...\nld: undefined symbol main");
  assert.equal(new TextDecoder().decode(await e.raw("error")), "link failed");
  assert.equal(await e.get("nope"), null);
  const first = await e.prev();
  assert.equal(first.n, 1);
  assert.equal(await first.prev(), null);
  assert.equal((await builds.atTime("2026-11-07T14:03:00Z")).n, 2);
  assert.equal((await builds.atTime("20261107T140210.120Z")).n, 1);
  assert.equal(await builds.atTime(Date.UTC(2027, 0, 1)), null);
  assert.equal(await r.table("deploy").latest(), null);
  assert.deepEqual((await r.before(null, 10)).map((x) => x.n), [2, 1]);
  assert.equal(await check(r), 4);
}

test("bytes source", async () => {
  await specExample(await Reader.open(new Uint8Array(spec)));
});

test("blob source", async () => {
  await specExample(await Reader.open(new BlobSource(new Blob([spec]))));
});

test("file source", async () => {
  const r = await openFile(vector("001-spec-example"));
  await specExample(r);
  await r.close();
});

test("http source with range requests", async () => {
  const { server, base } = await serve({ "/spec.trak": spec, "/family.trak": family });
  try {
    const src = await HttpSource.open(`${base}/spec.trak`);
    assert.equal(src.size, spec.length);
    await specExample(await Reader.open(src, { blockSize: 64 }));
    const r = await Reader.open(await HttpSource.open(`${base}/family.trak`), { blockSize: 128 });
    assert.equal(await r.family("a").count(), 13);
    await assert.rejects(HttpSource.open(`${base}/missing.trak`), /404/);
  } finally {
    server.close();
  }
});

test("families", async () => {
  const r = await Reader.open(new Uint8Array(family));
  const a = r.family("a");
  assert.deepEqual(a.types.map((s) => s.name), ["a", "a.b", "a.b.c", "a.d"]);
  assert.equal(r.schema("a.b.c").parent, "a.b");
  assert.deepEqual(r.schema("a.b.c").lineage, ["a", "a.b", "a.b.c"]);
  assert.deepEqual(r.schema("a.b.c").fields.map((f) => f.path), ["x", "y", "w.[]"]);
  assert.equal(await a.count(), 13);
  const row = await a.get(8);
  assert.deepEqual([row.type, row.n, row.familyNumber(), row.familyNumber("a.b")], ["a.b.c", 2, 8, 4]);
  const desc = [];
  for await (const e of a.range(6, 3)) desc.push(e.familyNumber());
  assert.deepEqual(desc, [6, 5, 4, 3]);
  assert.equal((await a.latest()).familyNumber(), 13);
  assert.equal((await a.atTime("20261106T212000.006Z")).type, "a.b.c");
  await assert.rejects(a.get(14), RangeError);
  await assert.rejects(a.get(0), RangeError);
  assert.throws(() => row.familyNumber("other"), /not in family/);
  assert.equal(await r.family("other").count(), 2);
});

test("cursors", async () => {
  const r = await Reader.open(new Uint8Array(family), { blockSize: 50 });
  const all = [];
  for await (const e of r.events()) all.push(e);
  const back = [];
  for await (const e of r.eventsBackward()) back.push(e.offset);
  assert.deepEqual(back, all.map((e) => e.offset).reverse());
  for (const limit of [1, 2, 7, 100]) {
    const got = [];
    let c = null;
    for (let page; (page = await r.after(c, limit)).length; c = page[page.length - 1].offset) got.push(...page);
    assert.deepEqual(got.map((e) => e.offset), all.map((e) => e.offset));
  }
  assert.deepEqual(await r.after(all[all.length - 1].offset), []);
  assert.deepEqual(await r.before(all[0].offset), []);
  for (const bad of [-3, 2, all[3].offset + 1, r.end + 1, 1.5]) {
    await assert.rejects(r.after(bad), ValueError);
    await assert.rejects(r.before(bad), ValueError);
  }
});

test("refresh picks up appended bytes", async () => {
  const src = new BytesSource(spec.subarray(0, spec.indexOf("==[ build 2")));
  const r = await Reader.open(src);
  assert.equal(await r.table("build").count(), 1); // the second event is cut off
  src.bytes = new Uint8Array(spec);
  src.size = spec.length;
  assert.equal(await r.refresh(), true);
  assert.equal(await r.table("build").count(), 2);
  assert.equal(await r.refresh(), false);
});

test("navigation reads stay bounded", async () => {
  const path = new URL("../../conformance/valid/014-chain-10000.trak", import.meta.url);
  const r = await openFile(path);
  const t = r.table("tick");
  for (const n of [1, 2, 4999, 10000]) {
    r.cache.clear();
    const before = r.footerReads;
    await t.get(n);
    assert.ok(r.footerReads - before <= 3 * Math.log2(10000) + 4);
  }
  assert.ok(r.blocks.fetches < statSync(path).size / r.blocks.blockSize);
  await r.close();
});
