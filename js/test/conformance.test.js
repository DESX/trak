// The JS reader against the conformance vectors written by the Go tests.

import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { BytesSource, TList, TLeaf, TObject, TrakFile } from "../src/index.js";

const dir = fileURLToPath(new URL("../../conformance", import.meta.url));

/** A value as plain data, like the Go Dump. */
export async function dump(x) {
  if (x instanceof TLeaf) return x.present ? x.text() : null;
  if (x instanceof TList) return Promise.all((await x.range(0, x.length)).map(dump));
  if (x instanceof TObject) {
    const out = {};
    for (const f of x.type.fields) out[f.name] = await dump(await x.field(f.name));
    return out;
  }
  throw new Error(`can't dump ${x}`);
}

const open = (path, opts) => TrakFile.open(new BytesSource(readFileSync(path)), opts);

for (const name of readdirSync(join(dir, "valid")).filter((n) => n.endsWith(".trak"))) {
  test(`valid/${name}`, async () => {
    const want = JSON.parse(readFileSync(join(dir, "valid", name.replace(/\.trak$/, ".json")), "utf8"));
    for (const blockSize of [1 << 14, 64]) {
      const f = await open(join(dir, "valid", name), { blockSize });
      assert.equal(f.version, want.version);
      assert.equal(f.size, want.size);
      for (const [v, tree] of Object.entries(want.dumps)) assert.deepEqual(await dump(await f.root(Number(v))), tree, `version ${v}, blocks of ${blockSize}`);
    }
  });
}

for (const name of readdirSync(join(dir, "invalid")).filter((n) => n.endsWith(".trak"))) {
  const reason = readFileSync(join(dir, "invalid", name.replace(/\.trak$/, ".txt")), "utf8").trim();
  if (!reason.startsWith("open: ")) continue; // the rest need a full check, which is Go's
  test(`invalid/${name}`, async () => {
    await assert.rejects(open(join(dir, "invalid", name)), (e) => e.message.toLowerCase().includes(reason.slice(6)));
  });
}
