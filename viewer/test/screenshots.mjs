// Screenshots of the proof of work, on a running `trak serve`:
//
//   node screenshots.mjs http://127.0.0.1:8766/viewer/ FILE.trak OUT_DIR
//
// 1 opening the file, 2 turn 150,000 of the largest session, 3 columns reordered,
// 4 a block expanded, 5 a path link followed, 6 the defaults at two versions.
import { chromium } from "playwright";

const [base, file, out] = process.argv.slice(2);
const browser = await chromium.launch(process.env.TRAK_CHROMIUM ? { executablePath: process.env.TRAK_CHROMIUM } : {});
const p = await browser.newPage({ viewport: { width: 1400, height: 820 } });
const errors = [];
p.on("pageerror", (e) => errors.push(String(e)));
const shot = async (name) => {
  await p.waitForTimeout(250);
  await p.screenshot({ path: `${out}/${name}.png` });
  console.log(name);
};
const view = ".view:not([hidden])";

await p.goto(base);
await p.evaluate(() => localStorage.clear());
await p.goto(base);
await p.click("#add");
    await p.click(".menu >> text=On the server…");
await p.waitForSelector(`.picker-list .file >> text=${file}`);
await shot("1a-picker");
await p.click(`.picker-list .file >> text=${file}`);
await p.waitForSelector(`${view} .line`);
await shot("1b-opened");

const big = await p.evaluate(async () => {
  const f = window.trakApp.current.file;
  const sessions = await f.get("sessions");
  let best = null;
  for (const s of await sessions.range(0, sessions.length)) {
    const t = await s.list("turns");
    if (!best || t.length > best.length) best = t;
  }
  return best.path;
});
await p.evaluate((path) => window.trakApp.navigate(`table:${path}@0`), big);
await p.waitForSelector(`${view} .row`);
await p.fill(`${view} .row-input`, "150000");
await p.press(`${view} .row-input`, "Enter");
await p.waitForSelector(`${view} .row.hit[data-m="150000"]`);
await shot("2-turn-150000");

// Drag "latency" before "kind", then open the context group.
const from = p.locator(`${view} .h >> text=latency`).first();
const to = p.locator(`${view} .h >> text=kind`).first();
await from.dragTo(to);
await p.waitForTimeout(200);
await shot("3a-columns-reordered");
await p.click(`${view} .h.group >> text=context`);
await p.$eval(`${view} .grid-wrap`, (el) => (el.scrollLeft = el.scrollWidth));
await p.mouse.move(700, 790); // out from under the tooltip
await shot("3b-group-expanded");

await p.click(`${view} .row.hit .c.expand >> nth=0`);
await p.waitForSelector("dialog.detail[open] .detail-body");
await p.waitForFunction(() => !document.querySelector("dialog.detail[open] .detail-body").textContent.startsWith("Loading"));
await shot("4-block-expanded");
await p.keyboard.press("Escape");

// A path link: each quota block names the turn that hit it.
await p.evaluate(() => window.trakApp.navigate("table:quota@0"));
await p.waitForSelector(`${view} a.path`);
await shot("5a-path-links");
await p.click(`${view} a.path >> nth=0`);
await p.waitForSelector(`${view} .line.focus`);
await shot("5b-path-followed");

await p.evaluate(() => window.trakApp.navigate("tree:defaults@1500"));
await p.waitForSelector(`${view} .line`);
for (const name of ["watchdog", "prompts"]) await p.click(`${view} .line >> text=${name}`);
await p.click(`${view} .line >> text=model`);
await p.waitForTimeout(200);
await shot("6a-defaults-1500");
for (let i = 0; i < 25; i++) {
  await p.keyboard.press("ArrowDown");
  await p.waitForTimeout(40);
}
await shot("6b-defaults-1525");
await p.click("#debug-toggle");
await shot("7-byte-ranges");
console.log(errors.join("\n") || "no errors");
await browser.close();
