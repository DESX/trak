// The log view: the file as text, one append after another, from any version. Blocks are
// folded to their first line, footers are dimmed, and each footer's path opens that item.

import { Scroller } from "./scroll.js";
import { fmtInt, h, rendered } from "./util.js";

const SHOWN = 12; // appends drawn at once
const PREVIEW = 240;

export class LogView {
  constructor(app, file) {
    this.app = app;
    this.file = file;
    this.kind = "log";
    this.path = "";
    this.pos = 1;
    this.unfolded = new Set(); // "version:rel" of blocks shown in full
    this.el = h("div.view.log-view", { tabIndex: 0 });
    this.token = 0;
  }

  get spec() {
    return `log:@${this.pos}`;
  }

  get title() {
    return "log";
  }

  async init() {
    this.scroller = new Scroller({ label: "version", onChange: (p) => this.go(p + 1, false), name: (p) => `version ${fmtInt(p + 1)}` });
    this.input = h("input.version-input", { type: "text", inputMode: "numeric", "aria-label": "Version" });
    const go = () => {
      const n = parseInt(this.input.value.replace(/[, _]/g, ""), 10);
      if (Number.isFinite(n)) this.jump(n);
    };
    this.input.addEventListener("keydown", (e) => e.key === "Enter" && go());
    this.label = h("span.count");
    this.body = h("div.log");
    this.el.append(
      h("div.toolbar", {}, h("span.crumbs", {}, h("b", {}, "log")), this.label, h("span.spacer"), h("label.goto", {}, "version ", this.input, h("button", { onclick: go }, "Go"))),
      h("div.log-main", {}, h("div.log-wrap", {}, this.body), this.scroller.el),
    );
    this.scroller.attach(this.el);
    new ResizeObserver(() => this.scroller.draw()).observe(this.el);
  }

  async start(v) {
    this.scroller.setRange(this.file.version, 1);
    await this.jump(v || this.file.version);
  }

  async refresh() {
    this.scroller.setRange(this.file.version, 1);
  }

  async jump(v) {
    v = Math.max(1, Math.min(v, this.file.version));
    this.focus = v;
    this.scroller.set(v - 1, true);
    await this.go(v, true);
  }

  async go(v, fromJump) {
    const t0 = performance.now();
    this.pos = v;
    const token = ++this.token;
    const last = Math.min(this.file.version, v + SHOWN - 1);
    const fts = await Promise.all(Array.from({ length: last - v + 1 }, (_, i) => this.file.footerAt(v + i)));
    const blocks = await Promise.all(fts.map((ft) => this.append(ft)));
    if (token !== this.token) return;
    this.body.replaceChildren(...blocks);
    this.input.value = String(v);
    this.label.textContent = `versions ${fmtInt(v)}–${fmtInt(last)} of ${fmtInt(this.file.version)}`;
    rendered(fromJump ? "log-jump" : "log-scroll", t0);
    this.app.updateSpec(this);
  }

  async append(ft) {
    const blocks = this.file.blocks;
    const parts = await Promise.all(
      ft.leaves.map(async (p) => {
        if (!p.present) return null;
        const key = `${ft.version}:${p.rel}`;
        const full = this.unfolded.has(key);
        const n = full ? Math.min(p.n, 1 << 20) : Math.min(p.n, PREVIEW);
        const text = new TextDecoder().decode(await blocks.read(ft.body + p.off, n));
        if (!p.field.block) return h("div.ll", {}, `${p.rel}: `, h("span.lv", {}, text.split("\n")[0] + (p.n > n || text.includes("\n") ? " …" : "")));
        const lines = text.split("\n");
        const folded = !full && (p.n > n || lines.length > 1);
        const toggle = () => {
          this.unfolded.has(key) ? this.unfolded.delete(key) : this.unfolded.add(key);
          this.go(this.pos, true);
        };
        return [
          h("div.ll.marker", {}, `--[ ${p.rel} ]--`),
          full ? h("pre.lblock", {}, text) : h("div.ll", {}, lines[0]),
          folded || full ? h("div.ll.fold", { onclick: toggle, title: full ? "Fold" : "Unfold" }, full ? "▴ fold" : `▸ ${fmtInt(p.n)} bytes, ${fmtInt(lines.length)}+ lines`) : null,
          h("div.ll.marker", {}, `--[ /${p.rel} ]--`),
        ];
      }),
    );
    const line = await blocks.read(ft.start, ft.end - ft.start - 1);
    const text = new TextDecoder().decode(line);
    const close = text.indexOf(" ]==");
    const isItem = ft.version > 1;
    const head = h(
      "div.ll.footer",
      {},
      "==[ ",
      isItem ? h("a.path", { href: "#", onclick: (e) => (e.preventDefault(), this.app.openPath(ft.path)) }, ft.path) : ft.path,
      text.slice(4 + ft.path.length, close + 4),
      h("span.vlqs", {}, text.slice(close + 4)),
    );
    return h(`div.append${ft.version === this.focus ? ".focus" : ""}`, { "data-v": ft.version }, parts.flat().filter(Boolean), head);
  }

  show() {
    this.el.hidden = false;
    this.scroller.draw();
    this.el.focus({ preventScroll: true });
  }

  hide() {
    this.el.hidden = true;
  }

  async setPos(v) {
    if (v !== this.pos) await this.jump(v);
  }

  destroy() {}
}
