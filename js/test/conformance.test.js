// The shared conformance vectors in ../conformance. The JavaScript library is a reader, so
// it runs the valid and invalid vectors, and reads back the write vectors' files.

import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { test } from "node:test";
import { check, FormatError, Reader } from "../src/index.js";
import { decode } from "./decode.js";

const root = new URL("../../conformance/", import.meta.url);
const names = (sub) =>
  readdirSync(new URL(sub, root)).filter((f) => f.endsWith(".trak")).map((f) => f.slice(0, -5)).sort();
const bytes = (sub, name, ext = ".trak") => readFileSync(new URL(`${sub}/${name}${ext}`, root));

test("minimum vector counts", () => {
  assert.ok(names("valid").length >= 25);
  assert.ok(names("invalid").length >= 10);
  assert.ok(names("write").length >= 10);
});

for (const name of names("valid")) {
  test(`valid/${name}`, async () => {
    // Small blocks, so reads cross block boundaries.
    const r = await Reader.open(new Uint8Array(bytes("valid", name)), { blockSize: 97 });
    const want = JSON.parse(bytes("valid", name, ".expect.json").toString("utf8"));
    assert.deepEqual(await decode(r), want);
    await check(r);
  });
}

for (const name of names("invalid")) {
  test(`invalid/${name}`, async () => {
    const reason = bytes("invalid", name, ".reason.txt").toString("utf8");
    const m = /^offset: (\d+)$/m.exec(reason);
    await assert.rejects(
      async () => check(await Reader.open(new Uint8Array(bytes("invalid", name)))),
      (e) => {
        assert.ok(e instanceof FormatError, `${e}`);
        if (m) assert.equal(e.offset, Number(m[1]), `${e.message}`);
        return true;
      },
    );
  });
}

for (const name of names("write")) {
  test(`write/${name} reads like valid/${name}`, async () => {
    const r = await Reader.open(new Uint8Array(bytes("write", name)));
    const want = JSON.parse(bytes("valid", name, ".expect.json").toString("utf8"));
    assert.deepEqual(await decode(r), want);
  });
}
