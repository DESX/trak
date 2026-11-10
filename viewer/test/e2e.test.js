// End-to-end tests: `trak serve` and the viewer in headless Chromium.
//
//   npm ci && npx playwright install chromium   (once)
//   npm test
//
// Builds the Go CLI unless TRAK_BIN names one, and writes test data with make_data.py
// (PYTHON, default python3). TRAK_CHROMIUM may name a Chromium binary to use instead of
// Playwright's own.

import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { appendFileSync, mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, test } from "node:test";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const here = fileURLToPath(new URL(".", import.meta.url));
const repo = join(here, "..", "..");
const work = mkdtempSync(join(tmpdir(), "trak-e2e-"));
const data = join(work, "data");
let server;
let browser;
let base;
let expect;

before(async () => {
  let bin = process.env.TRAK_BIN;
  if (!bin) {
    bin = join(work, process.platform === "win32" ? "trak.exe" : "trak");
    execFileSync("go", ["build", "-o", bin, "./cmd/trak"], { cwd: join(repo, "go"), stdio: "inherit" });
  }
  execFileSync(process.env.PYTHON ?? "python3", [join(here, "make_data.py"), data, "20000"], { stdio: "inherit" });
  expect = JSON.parse(readFileSync(join(data, "expect.json"), "utf8"));
  server = spawn(bin, ["serve", data, "--addr", "127.0.0.1:0", "--prefix", "/trak"], { stdio: ["ignore", "pipe", "inherit"] });
  base = await new Promise((ok, fail) => {
    let out = "";
    server.stdout.on("data", (b) => {
      out += b;
      const m = /(http:\/\/\S+)/.exec(out);
      if (m) ok(m[1]);
    });
    server.on("exit", (code) => fail(new Error(`trak serve exited with ${code}`)));
  });
  browser = await chromium.launch(process.env.TRAK_CHROMIUM ? { executablePath: process.env.TRAK_CHROMIUM } : {});
});

after(async () => {
  await browser?.close();
  server?.kill();
});

async function page() {
  const p = await browser.newPage({ viewport: { width: 1300, height: 850 } });
  p.errors = [];
  p.on("pageerror", (e) => p.errors.push(String(e)));
  p.on("console", (m) => m.type() === "error" && p.errors.push(m.text()));
  return p;
}

async function openDemo(p) {
  await p.goto(base);
  await p.click("text=demo.trak");
  await p.waitForSelector(".log-event");
}

const rowText = (p, sel) => p.$$eval(sel, (els) => els.map((e) => e.textContent));

test("lists files and opens one at its end", async () => {
  const p = await page();
  await p.goto(base);
  await p.waitForSelector(".files");
  const files = await rowText(p, ".files button");
  assert.ok(files.some((f) => f.startsWith("sub/")));
  assert.ok(files.some((f) => f.startsWith("demo.trak")));
  await p.click("text=demo.trak");
  await p.waitForSelector(".log-event");
  assert.match(await p.textContent("#status"), /trak 1\.1 · .* · 5 types/);
  // The newest event is at the bottom of the log.
  const last = await p.$$eval(".footer-line", (els) => els[els.length - 1].textContent);
  assert.match(last, new RegExp(`==\\[ ${expect.last.type.replace(/\./g, "\\.")} ${expect.last.n} `));
  // Type tree: children are nested under parents, with counts.
  const names = await rowText(p, "#types .name");
  assert.deepEqual(names, ["build", "step", "cmd", "test", "deploy"]);
  const counts = await rowText(p, "#types .type .n");
  assert.deepEqual(counts.map((c) => Number(c.replace(/,/g, ""))), Object.values(expect.counts));
  assert.deepEqual(p.errors, []);
  await p.close();
});

test("log view loads older events when scrolled up", async () => {
  const p = await page();
  await openDemo(p);
  const before = await p.$$eval(".log-event", (e) => e.length);
  await p.$eval(".log", (el) => { el.scrollTop = 0; el.dispatchEvent(new Event("scroll")); });
  await p.waitForFunction((n) => document.querySelectorAll(".log-event").length > n, before);
  // Blocks start collapsed and expand on demand.
  await p.click("details.block > summary >> nth=0");
  await p.waitForSelector("details.block[open] pre.block-value");
  assert.match(await p.textContent("details.block[open] pre.block-value"), /compiling module_0\.c/);
  assert.deepEqual(p.errors, []);
  await p.close();
});

test("family table rows match the Python reader", async () => {
  const p = await page();
  await openDemo(p);
  await p.click('#types [data-key="build*"]');
  await p.waitForSelector(".row .c.type");
  assert.equal(await p.textContent(".toolbar .count"), `${expect.family_rows.toLocaleString("en-US")} rows`);
  for (const [m, want] of Object.entries(expect.samples)) {
    await p.fill(".row-input", m);
    await p.click("text=Go");
    await p.waitForFunction((n) => {
      const hit = document.querySelector(".row.hit");
      return hit && hit.querySelector(".c.n").textContent.replace(/,/g, "") === n && hit.querySelector(".c.type");
    }, m);
    const type = await p.textContent(".row.hit .c.type");
    const user = await p.textContent(".row.hit .c:nth-child(4)");
    assert.deepEqual([type, user], [want.type, want.user], `row ${m}`);
  }
  // The grouped header: host.name sits under a "host" group, which collapses.
  assert.ok((await rowText(p, ".h.group")).some((t) => t.includes("host")));
  await p.click(".h.group >> text=host");
  await p.waitForSelector(".h.collapsed");
  assert.match(await p.textContent(".h.collapsed"), /host \(4\)/);
  // A list cell opens its items.
  await p.click(".row .c.expand >> nth=0");
  await p.waitForSelector("dialog[open] .detail-body");
  assert.deepEqual(p.errors, []);
  await p.close();
});

test("time search jumps to the first event at or after it", async () => {
  const p = await page();
  await openDemo(p);
  await p.click('#types [data-key="deploy"]');
  await p.waitForSelector(".row .c.time");
  await p.fill(".time-input", "2020-01-01T00:00:00Z"); // before the first event
  await p.click("text=Go");
  await p.waitForFunction(() => document.querySelector(".row.hit .c.n")?.textContent === "1");
  await p.close();
});

test("an uploaded file is read in the browser", async () => {
  const p = await page();
  await p.goto(base);
  await p.setInputFiles("#pick", join(repo, "conformance", "valid", "001-spec-example.trak"));
  await p.waitForSelector(".log-event");
  assert.equal(await p.textContent("#file-name"), "001-spec-example.trak");
  await p.click('#types [data-key="build"]');
  await p.waitForSelector(".row .c.time");
  assert.equal(await p.textContent(".toolbar .count"), "2 rows");
  await p.click("#validate");
  await p.waitForFunction(() => /Valid: 4 objects/.test(document.querySelector("#status").textContent));
  assert.deepEqual(p.errors, []);
  await p.close();
});

test("follow picks up appended events", async () => {
  const p = await page();
  await p.goto(`${base}viewer/#file=sub/027-family-deep.trak`);
  await p.waitForSelector(".log-event");
  await p.click('#types [data-key="a*"]');
  await p.waitForSelector(".row .c.type");
  assert.equal(await p.textContent(".toolbar .count"), "13 rows");
  await p.click("#follow");
  // Append one more `a` event, numbered by the Python writer.
  execFileSync(process.env.PYTHON ?? "python3", ["-c", `
import sys; sys.path.insert(0, ${JSON.stringify(join(repo, "python", "src"))})
import trak
with trak.open_append(${JSON.stringify(join(data, "sub", "027-family-deep.trak"))}) as w:
    w.write("a.d", {"x": 99, "d": None}, "20261106T212001.000Z")
`]);
  await p.waitForFunction(() => document.querySelector(".toolbar .count").textContent === "14 rows", null, { timeout: 10000 });
  appendFileSync(join(data, "sub", "027-family-deep.trak"), "x: 1\n==[ a 9"); // a torn tail is ignored
  await p.waitForTimeout(2500);
  assert.equal(await p.textContent(".toolbar .count"), "14 rows");
  assert.match(await p.textContent("#status"), /torn tail ignored/);
  assert.deepEqual(p.errors, []);
  await p.close();
});
