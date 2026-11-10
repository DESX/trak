// Decode a file to the shape of the conformance suite's expect.json, checking on the way
// that navigation agrees with a file-order scan (like python/tests/interop.py).

import assert from "node:assert/strict";

const annots = (items) => items.map((a) => ({ name: a.name, text: a.text }));

export async function decode(r) {
  const types = r.types().map((s) => ({
    name: s.name,
    index: s.index,
    version: s.version,
    parent: s.parent,
    description: s.description,
    parsed: s.root != null,
    annotations: annots(s.annotations),
    fields: s.fields.map((f) => ({
      path: f.path,
      type: f.type,
      enum: [...f.enum],
      unknown: f.unknown,
      block: f.block,
      optional: f.optional,
      annotations: annots(f.annotations),
      description: f.description,
    })),
  }));
  const events = [];
  for await (const e of r.events()) {
    events.push({
      type: e.type,
      n: e.n,
      timestamp: e.timestamp,
      families: Object.fromEntries(e.schema.lineage.map((a) => [a, e.familyNumber(a)])),
      value: e.schema.root != null ? await e.native() : null,
    });
  }
  for (const s of r.types()) {
    const rows = events.filter((e) => e.type === s.name);
    const table = r.table(s.name);
    assert.equal(await table.count(), rows.length);
    for (const row of rows) assert.equal((await table.get(row.n)).timestamp, row.timestamp);
    const family = r.family(s.name);
    const members = events.filter((e) => s.name in e.families);
    assert.equal(await family.count(), members.length);
    for (let m = 1; m <= members.length; m++) {
      assert.equal(members[m - 1].families[s.name], m);
      const e = await family.get(m);
      assert.deepEqual([e.type, e.n], [members[m - 1].type, members[m - 1].n]);
    }
    const scanned = [];
    if (members.length) for await (const e of family.range(1, members.length)) scanned.push([e.type, e.n]);
    assert.deepEqual(scanned, members.map((e) => [e.type, e.n]));
  }
  const everything = events.map((e) => [e.type, e.n]);
  for (const [forward, limit] of [[true, 3], [false, 4]]) {
    const pages = [];
    let cursor = null;
    for (;;) {
      const page = forward ? await r.after(cursor, limit) : await r.before(cursor, limit);
      if (page.length === 0) break;
      pages.push(...page.map((e) => [e.type, e.n]));
      cursor = page[page.length - 1].offset;
    }
    assert.deepEqual(pages, forward ? everything : [...everything].reverse());
  }
  return { types, events };
}
