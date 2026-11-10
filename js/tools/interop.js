// The JavaScript side of the cross-implementation tests (conformance/crosscheck.py):
//
//   node tools/interop.js read FILE.trak   print the decoded file and check result as JSON
//
// The library has no writer, so it only reads.

import { check, TrakError } from "../src/index.js";
import { openFile } from "../src/node.js";
import { decode } from "../test/decode.js";

const [cmd, path] = process.argv.slice(2);
if (cmd !== "read" || path == null) {
  process.stderr.write("usage: interop.js read FILE\n");
  process.exit(2);
}
let out;
const r = await openFile(path);
try {
  out = await decode(r);
} catch (e) {
  if (!(e instanceof TrakError) && e.name !== "AssertionError") throw e;
  out = { error: e.message };
}
try {
  await check(r);
  out.check = null;
} catch (e) {
  if (!(e instanceof TrakError)) throw e;
  out.check = e.message;
}
await r.close();
process.stdout.write(JSON.stringify(out) + "\n");
