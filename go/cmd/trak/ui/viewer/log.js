// The log view: the file as text, in file order, newest at the bottom. It pages with
// cursors (Reader.before and Reader.after), so it opens at the end of a multi-GB file
// without reading the rest. Blocks, and lists or maps with many leaves, start collapsed.

import { el, formatBytes, plural } from "./cells.js";

const PAGE = 50;
const INLINE_PREVIEW = 2000;
const MANY = 8;
const utf8 = new TextDecoder("utf-8");

export class LogView {
  constructor(container, { reader, onEvent = null }) {
    this.reader = reader;
    this.onEvent = onEvent;
    this.list = el("div", { class: "log-list" });
    this.top = el("div", { class: "log-edge" });
    this.bottom = el("div", { class: "log-edge" });
    this.root = el("div", { class: "log", onscroll: () => this.onScroll() }, this.top, this.list, this.bottom);
    container.replaceChildren(this.root);
    this.first = null; // offset of the oldest event shown
    this.last = null; // offset of the newest event shown
    this.busy = false;
    this.atStart = false;
  }

  async load() {
    const page = await this.reader.before(null, PAGE);
    page.reverse();
    this.atStart = page.length < PAGE;
    for (const e of page) this.list.append(await this.render(e));
    this.first = page[0]?.offset ?? null;
    this.last = page[page.length - 1]?.offset ?? null;
    this.edges();
    this.root.scrollTop = this.root.scrollHeight;
  }

  edges() {
    this.top.textContent = this.atStart ? "Start of file" : "Scroll up for older events";
    if (this.list.childElementCount === 0) this.top.textContent = "No events yet.";
    this.bottom.textContent = `End of valid objects · byte ${this.reader.end.toLocaleString()}`;
  }

  async onScroll() {
    if (this.busy || this.atStart || this.first == null || this.root.scrollTop > 400) return;
    this.busy = true;
    try {
      const page = await this.reader.before(this.first, PAGE);
      this.atStart = page.length < PAGE;
      const height = this.root.scrollHeight;
      const frag = document.createDocumentFragment();
      for (const e of page.reverse()) frag.append(await this.render(e));
      this.list.prepend(frag);
      if (page.length) this.first = page[0].offset;
      this.root.scrollTop += this.root.scrollHeight - height;
      this.edges();
    } finally {
      this.busy = false;
    }
  }

  /** Append events written since the last call, e.g. after Reader.refresh(). */
  async newer() {
    const stick = this.root.scrollTop + this.root.clientHeight >= this.root.scrollHeight - 40;
    for (;;) {
      const page = await this.reader.after(this.last, PAGE);
      if (page.length === 0) break;
      for (const e of page) this.list.append(await this.render(e));
      this.last = page[page.length - 1].offset;
      this.first ??= page[0].offset;
    }
    this.edges();
    if (stick) this.root.scrollTop = this.root.scrollHeight;
  }

  /** Jump to an event, e.g. from a table row: show the events around it. */
  async show(event) {
    this.list.replaceChildren();
    const older = (await this.reader.before(event.offset, PAGE / 2)).reverse();
    const newer = await this.reader.after(event.offset, PAGE / 2);
    for (const e of [...older, event, ...newer]) {
      const node = await this.render(e);
      if (e.offset === event.offset) node.classList.add("hit");
      this.list.append(node);
    }
    this.first = (older[0] ?? event).offset;
    this.last = (newer[newer.length - 1] ?? event).offset;
    this.atStart = older.length < PAGE / 2;
    this.edges();
    this.list.querySelector(".hit")?.scrollIntoView({ block: "center" });
  }

  async render(e) {
    const box = el("article", { class: "log-event", "data-offset": e.offset });
    if (e.schema.root == null) {
      box.append(el("div", { class: "muted" }, `(values unavailable: ${e.schema.error?.message ?? "unknown schema"})`));
    } else {
      const w = e.walk();
      // Small bodies in one read; big ones leaf by leaf, so a huge block isn't fetched.
      const size = e.footer.fstart - e.offset;
      const body = size <= 1 << 16 ? await e.body() : null;
      const bytes = async (off, len) => (body ? body.subarray(off - e.offset, off - e.offset + len) : this.reader.read(off, len));
      const lines = await this.node(e.schema.root, w.tree, w, bytes, "");
      box.append(...lines);
    }
    const fam = e.schema.lineage.length > 1 ? ` · row ${e.familyNumber().toLocaleString()} of ${e.schema.lineage[0]}` : "";
    box.append(
      el(
        "button",
        { class: "footer-line", title: `Byte ${e.offset.toLocaleString()} · click for the whole event`, onclick: () => this.onEvent?.(e) },
        `==[ ${e.type} ${e.n} ${e.timestamp} ]==`,
        el("span", { class: "muted" }, fam),
      ),
    );
    return box;
  }

  /** Lines for a subtree, in file order. */
  async node(t, n, w, bytes, path) {
    if (n == null) return [];
    if (typeof n === "number") return [await this.leaf(w.leaves[n], bytes)];
    if (n.kind === "empty") return [];
    const join = (k) => (path ? `${path}.${k}` : k);
    if (n.kind === "object") {
      const out = [];
      for (const m of t.members) out.push(...(await this.node(m.type, n.members[m.key], w, bytes, join(m.key))));
      return out;
    }
    // Map keys are only in the leaves' paths, so a map's children are labeled by position.
    const items = n.kind === "list"
      ? n.items.map((x, i) => [t.elem, x, join(i)])
      : n.entries.map((x) => [t.value, x.value, join("…")]);
    const leaves = countLeaves(n);
    if (leaves <= MANY) {
      const out = [];
      for (const [tt, x, p] of items) out.push(...(await this.node(tt, x, w, bytes, p)));
      return out;
    }
    // Many leaves: collapsed, rendered when opened.
    const d = el("details", { class: "group-lines" });
    const what = n.kind === "list" ? plural(items.length, "item", "items") : plural(items.length, "entry", "entries");
    d.append(el("summary", {}, el("span", { class: "path" }, path), el("span", { class: "muted" }, ` ${what}, ${plural(leaves, "value", "values")}`)));
    d.addEventListener("toggle", async () => {
      if (!d.open || d.dataset.done) return;
      d.dataset.done = "1";
      for (const [tt, x, p] of items) d.append(...(await this.node(tt, x, w, bytes, p)));
    });
    return [d];
  }

  async leaf(rec, bytes) {
    const path = await pathText(rec, bytes);
    const ann = rec.leaf.annotations.map((a) => ` @${a.name}${a.text != null ? `(${a.text})` : ""}`).join("");
    if (rec.leaf.block) {
      const d = el("details", { class: "block" });
      const preview = utf8.decode(await bytes(rec.voff, Math.min(rec.length, 120)));
      d.append(
        el("summary", {}, el("span", { class: "path" }, path), el("span", { class: "muted" }, ` ${formatBytes(rec.length)}${ann}`), " ",
          el("span", { class: "preview" }, preview.split("\n")[0].slice(0, 100))),
      );
      d.addEventListener("toggle", async () => {
        if (!d.open || d.dataset.done) return;
        d.dataset.done = "1";
        d.append(el("pre", { class: "block-value" }, utf8.decode(await this.reader.read(rec.voff, rec.length))));
        d.append(el("div", { class: "label-line" }, `--[ ${path} ]--`));
      });
      return d;
    }
    const value = utf8.decode(await bytes(rec.voff, Math.min(rec.length, INLINE_PREVIEW)));
    const line = el("div", { class: "line" }, el("span", { class: "path" }, path), ": ", el("span", { class: `value ${rec.leaf.kind}` }, value));
    if (rec.length > INLINE_PREVIEW) {
      line.append(el("button", { class: "more", onclick: async () => {
        line.querySelector(".value").textContent = utf8.decode(await this.reader.read(rec.voff, rec.length));
      } }, `… ${formatBytes(rec.length)}`));
    }
    if (ann) line.append(el("span", { class: "ann" }, ann));
    return line;
  }
}

async function pathText(rec, bytes) {
  return utf8.decode(await bytes(rec.pathOff, rec.plen));
}

function countLeaves(n) {
  if (n == null) return 0;
  if (typeof n === "number") return 1;
  if (n.kind === "empty") return 0;
  if (n.kind === "object") return Object.values(n.members).reduce((a, x) => a + countLeaves(x), 0);
  if (n.kind === "list") return n.items.reduce((a, x) => a + countLeaves(x), 0);
  return n.entries.reduce((a, x) => a + countLeaves(x.value), 0);
}
