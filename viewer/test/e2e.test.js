// End-to-end tests: `trak serve` and the viewer in headless Chromium, timing every
// responsiveness target on each file in the data directory.
//
//   npm ci && npx playwright install chromium   (once)
//   npm test
//
// Environment:
//   TRAK_DATA      a directory of .trak files to test; by default sophie-gen writes
//                  small ones, one per writer policy
//   TRAK_BIN       a built trak CLI; by default it is built from ../../go
//   TRAK_CHROMIUM  a Chromium binary to use instead of Playwright's
//   TRAK_PERF_OUT  where to write the measurements as JSON
//   TRAK_STRICT=0  report the targets without failing on them

import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, describe, test } from "node:test";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const here = fileURLToPath(new URL(".", import.meta.url));
const repo = join(here, "..", "..");
const work = mkdtempSync(join(tmpdir(), "trak-e2e-"));
const strict = process.env.TRAK_STRICT !== "0";
const TARGETS = { open: 200, jump: 100, scroll: 50, step: 50 };
const results = {};
let data = process.env.TRAK_DATA;
let server;
let browser;
let base;

const exe = (name) => join(work, process.platform === "win32" ? `${name}.exe` : name);

// The data comes first: the tests are made per file.
if (!data) {
  data = join(work, "data");
  mkdirSync(data);
  const gen = exe("sophie-gen");
  execFileSync("go", ["build", "-o", gen, "./cmd/sophie-gen"], { cwd: join(repo, "go"), stdio: "inherit" });
  for (const policy of ["reference", "delta"]) {
    execFileSync(gen, ["-q", "--size", "40MB", "--seed", "1", "--policy", policy, join(data, `sophie-${policy}.trak`)], { stdio: "inherit" });
  }
}

before(async () => {
  let bin = process.env.TRAK_BIN;
  if (!bin) {
    bin = exe("trak");
    execFileSync("go", ["build", "-o", bin, "./cmd/trak"], { cwd: join(repo, "go"), stdio: "inherit" });
  }
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
  if (process.env.TRAK_PERF_OUT) writeFileSync(process.env.TRAK_PERF_OUT, JSON.stringify(results, null, 2));
  console.log(JSON.stringify(results, null, 2));
});

async function page() {
  const p = await browser.newPage({ viewport: { width: 1400, height: 860 } });
  p.errors = [];
  p.on("pageerror", (e) => p.errors.push(String(e)));
  p.on("console", (m) => m.type() === "error" && p.errors.push(m.text()));
  return p;
}

/** Run `action` in the page and time it until a view of `kind` finishes drawing. */
function timed(p, kind, action, arg) {
  return p.evaluate(
    ([kind, action, arg]) =>
      new Promise((ok, fail) => {
        const t0 = performance.now();
        const done = (e) => {
          if (!e.detail.view.startsWith(kind)) return;
          document.removeEventListener("trak:rendered", done);
          const view = document.querySelector(".view:not([hidden])");
          ok({ ms: performance.now() - t0, rows: view.querySelectorAll(".row").length });
        };
        document.addEventListener("trak:rendered", done);
        setTimeout(() => fail(new Error(`no ${kind} render within 10 s`)), 10_000);
        new Function("arg", action)(arg);
      }),
    [kind, action, arg],
  );
}

const view = `document.querySelector(".view:not([hidden])")`;
const press = (key) => `${view}.dispatchEvent(new KeyboardEvent("keydown", { key: ${JSON.stringify(key)}, bubbles: true }))`;
const jumpTo = `const v = ${view}; const i = v.querySelector(".row-input, .version-input"); i.value = String(arg); i.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));`;

function stats(xs) {
  const s = [...xs].sort((a, b) => a - b);
  return { n: s.length, p50: +s[Math.floor(s.length / 2)].toFixed(1), p95: +s[Math.floor(s.length * 0.95)].toFixed(1), max: +s[s.length - 1].toFixed(1) };
}

function check(name, st, target) {
  const ok = st.max <= target;
  if (!ok) console.log(`  ${name}: max ${st.max} ms over the ${target} ms target`);
  if (strict) assert.ok(ok, `${name}: max ${st.max} ms, target ${target} ms`);
}

// A small deterministic random sequence.
function rng(seed) {
  let x = seed >>> 0 || 1;
  return () => ((x ^= x << 13), (x ^= x >>> 17), (x ^= x << 5), (x >>> 0) / 2 ** 32);
}

const files = () => readdirSync(data).filter((f) => f.endsWith(".trak")).sort();

describe("viewer", () => {
  test("serves the viewer under a prefix and lists the files", async () => {
    const p = await page();
    await p.goto(base);
    await p.click("#add");
    await p.click(".menu >> text=On the server…");
    await p.waitForSelector(".picker-list .file");
    const names = await p.$$eval(".picker-list .file .pick-name > span:first-child", (els) => els.map((e) => e.textContent));
    assert.deepEqual(names, files());
    // Each file shows its schema's description, and the picker its absolute path.
    assert.equal(await p.$$eval(".picker-list .file .pick-desc:not(.error)", (els) => els.length), names.length);
    assert.ok((await p.textContent(".picker-where")).startsWith("/"));
    assert.deepEqual(p.errors, []);
    await p.close();
  });

  for (const name of files()) {
    test(`targets on ${name}`, async () => {
      const r = (results[name] = { size: statSync(join(data, name)).size });
      const p = await page();
      await p.goto(base);
      await p.evaluate(() => localStorage.clear());
      await p.goto(base);

      // Open the file and show the schema: from the click to the root tree.
      await p.click("#add");
    await p.click(".menu >> text=On the server…");
      await p.waitForSelector(`.picker-list .file >> text=${name}`);
      const open = await timed(p, "tree", `[...document.querySelectorAll(".picker-list .file")].find((b) => b.textContent.startsWith(${JSON.stringify(name)})).click()`);
      r.open = +open.ms.toFixed(1);
      check("open", { max: r.open }, TARGETS.open);
      assert.ok(await p.isVisible(".line >> text=sessions"));

      // The largest list of turns.
      const big = await p.evaluate(async () => {
        const f = window.trakApp.current.file;
        const sessions = await f.get("sessions");
        let best = null;
        for (const s of await sessions.range(0, sessions.length)) {
          const t = await s.list("turns");
          if (!best || t.length > best.length) best = t;
        }
        return { path: best.path, count: best.length };
      });
      r.largest = big;

      // Jump to rows across the largest list and a few others.
      await p.evaluate((path) => window.trakApp.navigate(`table:${path}@0`), big.path);
      await p.waitForSelector(".view:not([hidden]) .row");
      const rand = rng(7);
      const jumps = [];
      for (let i = 0; i < 25; i++) {
        const row = Math.floor(rand() * big.count);
        const t = await timed(p, "table", jumpTo, row);
        jumps.push(t.ms);
        const first = await p.$eval(".view:not([hidden]) .row", (e) => Number(e.dataset.m));
        assert.equal(first, Math.min(row, big.count - t.rows), `jump to ${row}`);
      }
      for (const list of ["logins", "quota", "sessions", "defaults.model", "defaults.prompts.base", `${big.path.split(".turns")[0]}.messages`]) {
        await p.evaluate((path) => window.trakApp.navigate(`table:${path}@0`), list);
        await p.waitForSelector(".view:not([hidden]) .row");
        const n = await p.evaluate((path) => window.trakApp.current.file.get(path).then((l) => l.length), list);
        for (let i = 0; i < 4; i++) jumps.push((await timed(p, "table", jumpTo, Math.floor(rand() * n))).ms);
      }
      r.jump = stats(jumps);
      check("jump", r.jump, TARGETS.jump);

      // Scroll page by page and row by row in the largest list, from a cold spot.
      await p.evaluate((path) => window.trakApp.navigate(`table:${path}@0`), big.path);
      await p.waitForSelector(".view:not([hidden]) .row");
      await timed(p, "table", jumpTo, Math.floor(big.count * 0.37));
      const scrolls = [];
      let blank = 0;
      for (let i = 0; i < 30; i++) {
        const t = await timed(p, "table", press(i % 3 === 2 ? "ArrowDown" : "PageDown"));
        scrolls.push(t.ms);
        if (t.rows < 10) blank++;
      }
      r.scroll = stats(scrolls);
      r.blankFrames = blank;
      check("scroll", r.scroll, TARGETS.scroll);
      assert.equal(blank, 0, "every drawn page is full");

      // Drag the scrollbar to the middle: the rows there, and few bytes read.
      const before = await p.evaluate(() => window.trakApp.reqBytes);
      const track = await p.$(".view:not([hidden]) .track");
      const box = await track.boundingBox();
      await p.mouse.move(box.x + box.width / 2, box.y + 5);
      await p.mouse.down();
      for (let i = 1; i <= 10; i++) await p.mouse.move(box.x + box.width / 2, box.y + (box.height / 2) * (i / 10), { steps: 2 });
      await p.mouse.up();
      await p.waitForTimeout(300);
      const mid = await p.$eval(".view:not([hidden]) .row", (e) => Number(e.dataset.m));
      r.dragMiddle = { row: mid, of: big.count, bytes: (await p.evaluate(() => window.trakApp.reqBytes)) - before };
      assert.ok(Math.abs(mid / big.count - 0.5) < 0.05, `dragged to row ${mid} of ${big.count}`);

      // Expand a group: a spanning header row appears.
      await p.click(".view:not([hidden]) .h.group >> text=context");
      await p.waitForSelector(".view:not([hidden]) .h.group:not(.collapsed) >> text=context");
      assert.ok(await p.isVisible(".view:not([hidden]) .h >> text=used"));

      // Step the defaults through their versions.
      await p.evaluate(() => window.trakApp.navigate("tree:defaults@0"));
      await p.waitForSelector(".view:not([hidden]) .line");
      const steps = [];
      for (let i = 0; i < 30; i++) steps.push((await timed(p, "tree", press("ArrowDown"))).ms);
      await timed(p, "tree", jumpTo, Math.floor((await p.evaluate(() => window.trakApp.current.active.view.count)) / 2));
      for (let i = 0; i < 20; i++) steps.push((await timed(p, "tree", press(i % 2 ? "ArrowUp" : "ArrowDown"))).ms);
      r.step = stats(steps);
      check("step", r.step, TARGETS.step);

      // A turn's tree view at its version, and the log at a version.
      await p.evaluate((spec) => window.trakApp.navigate(spec), `items:${big.path}@${Math.floor(big.count / 3)}`);
      await p.waitForSelector(".view:not([hidden]) .line.focus");
      await p.evaluate(() => window.trakApp.navigate("log:@1000"));
      await p.waitForSelector(".view:not([hidden]) .append.focus");

      // Memory and bytes: bounded, whatever the file's size.
      const client = await p.context().newCDPSession(p);
      await client.send("Performance.enable");
      await client.send("HeapProfiler.collectGarbage");
      const { metrics } = await client.send("Performance.getMetrics");
      const heap = metrics.find((m) => m.name === "JSHeapUsedSize")?.value ?? 0;
      r.heapMB = +(heap / 2 ** 20).toFixed(1);
      const io = await p.evaluate(() => ({ requests: window.trakApp.reqCount, bytes: window.trakApp.reqBytes }));
      r.requests = io.requests;
      r.bytesRead = io.bytes;
      r.bytesReadShare = +(io.bytes / r.size).toFixed(5);
      assert.ok(r.heapMB < 200, `heap ${r.heapMB} MB`);
      assert.ok(io.bytes < Math.max(400e6, r.size / 4), `read ${io.bytes} bytes of ${r.size}`);
      assert.deepEqual(p.errors, []);
      await p.close();
    });
  }
});
