// The tree view: one object as an expandable tree, at one position of a scrollbar.
//
//   tree:PATH@k     the object at PATH, at its own version k (the root's versions are
//                   the file's appends)
//   items:LIST@n    the object that owns LIST, as it was right after item n was
//                   appended, with item n open
//
// Fields one level down show at first; objects and lists open on click. Values that
// changed since the previous position are highlighted.

import { Scroller } from "./scroll.js";
import { contextMenu, firstLine, fmtInt, fmtStamp, h, rendered, showText } from "./util.js";

const PAGE = 10; // list items shown per page in the tree

export class TreeView {
  constructor(app, file, mode, path) {
    this.app = app;
    this.file = file;
    this.mode = mode; // "tree" or "items"
    this.path = path;
    this.kind = mode;
    this.pos = 0;
    this.open = new Set(); // relative paths of open objects and lists
    this.pages = new Map(); // relative list path → first item shown
    this.ids = new Map(); // relative path → identity, from the last render
    this.el = h("div.view.tree-view", { tabIndex: 0 });
    this.token = 0;
  }

  get spec() {
    return `${this.mode}:${this.path}@${this.pos}`;
  }

  get title() {
    const name = this.path || this.file.schema.root.name;
    return this.mode === "tree" ? `${name} · versions` : `${name} · tree`;
  }

  async init() {
    if (this.mode === "tree") {
      this.base = await this.file.get(this.path);
      if (!this.base.type) throw new Error(`${this.path} is not an object`);
      this.count = (await this.base.k()) + 1;
    } else {
      this.list = await this.file.get(this.path);
      if (!this.list.field) throw new Error(`${this.path} is not an event list`);
      this.count = this.list.count;
      const dot = this.path.lastIndexOf(".");
      this.owner = dot < 0 ? "" : this.path.slice(0, dot);
      this.listRel = dot < 0 ? this.path : this.path.slice(dot + 1);
      // The owner may be nested inside an item: open every level down to the list.
      this.open.add(this.listRel);
    }
    this.scroller = new Scroller({ label: "versions", onChange: (p) => this.go(p, false) });
    this.input = h("input.version-input", { type: "text", inputMode: "numeric", "aria-label": this.mode === "tree" ? "Version" : "Item" });
    // The root's versions are the file's: its version k is file version k + 1.
    this.ofs = this.mode === "tree" && this.path === "" ? 1 : 0;
    const go = () => {
      const n = parseInt(this.input.value.replace(/[, _]/g, ""), 10);
      if (Number.isFinite(n)) this.jump(n < 0 ? this.count + n : n - this.ofs);
    };
    this.input.addEventListener("keydown", (e) => e.key === "Enter" && go());
    this.label = h("span.count");
    this.when = h("span.when");
    this.toolbar = h(
      "div.toolbar",
      {},
      this.app.crumbs(this.path, this.mode),
      this.label,
      this.when,
      h("span.spacer"),
      h("label.goto", {}, this.mode === "tree" ? "version " : "item ", this.input, h("button", { onclick: go }, "Go")),
      h("button", { onclick: () => this.step(-1), title: "Previous (↑)" }, "◂"),
      h("button", { onclick: () => this.step(1), title: "Next (↓)" }, "▸"),
      this.mode === "items" ? h("button", { onclick: () => this.app.navigate(`table:${this.path}@${this.pos}`) }, "Table view") : null,
    );
    this.body = h("div.tree");
    this.el.append(this.toolbar, h("div.tree-main", {}, h("div.tree-wrap", {}, this.body), this.scroller.el));
    this.scroller.attach(this.el);
    new ResizeObserver(() => this.scroller.draw()).observe(this.el);
  }

  step(d) {
    this.scroller.set(this.pos + d);
  }

  async jump(n) {
    this.scroller.set(n, true);
    await this.go(this.scroller.pos, true);
  }

  async start(pos) {
    this.scroller.setRange(this.count, 1);
    this.scroller.set(pos ?? this.count - 1, true);
    await this.go(this.scroller.pos, true);
  }

  async refresh() {
    const atEnd = this.pos === this.count - 1;
    if (this.mode === "tree") {
      this.base = await this.file.get(this.path);
      this.count = (await this.base.k()) + 1;
    } else {
      this.list = await this.file.get(this.path);
      this.count = this.list.count;
    }
    this.scroller.setRange(this.count, 1);
    if (atEnd && this.pos !== this.count - 1) await this.jump(this.count - 1);
  }

  /** The object to show at position p, and the append that made it. */
  async target(p) {
    if (this.mode === "tree") {
      const obj = await this.base.at(p);
      return { obj, made: await obj.written() };
    }
    const item = await this.list.item(p);
    const made = item.footer ? await this.file.appendEndingAt(item.footer.end) : await item.created();
    const root = await this.file.root(made.version);
    return { obj: await root.get(this.owner), made, item: p };
  }

  async go(p, fromJump) {
    const t0 = performance.now();
    this.pos = p;
    const token = ++this.token;
    if (this.mode === "items") {
      this.pages.set(this.listRel, Math.max(0, p - Math.floor(PAGE / 2)));
      this.open.add(`${this.listRel}.${p}`);
    }
    const { obj, made, item } = await this.target(p);
    if (token !== this.token) return;
    const ids = new Map();
    const lines = await this.lines(obj, "", 0, ids, item);
    if (token !== this.token) return;
    const first = this.ids.size === 0;
    for (const l of lines) {
      if (!first && l.id !== undefined && this.ids.has(l.rel) && this.ids.get(l.rel) !== l.id) l.el.classList.add("changed");
      if (!first && l.id !== undefined && !this.ids.has(l.rel) && l.depth > 0) l.el.classList.add("added");
    }
    this.ids = ids;
    this.body.replaceChildren(...lines.map((l) => l.el));
    this.input.value = String(p + this.ofs);
    this.label.textContent = this.mode === "tree" ? `version ${fmtInt(p + this.ofs)} of ${fmtInt(this.count - 1 + this.ofs)}` : `item ${fmtInt(p)} of ${fmtInt(this.count)}`;
    this.when.replaceChildren(
      h("a", { href: "#", title: "Show this append in the log", onclick: (e) => (e.preventDefault(), this.app.navigate(`log:@${made.version}`)) }, `v${fmtInt(made.version)}`),
      ` · ${fmtStamp(made.stamp)} · ${made.path}`,
    );
    rendered(fromJump ? "tree-jump" : "tree-step", t0);
    this.app.updateSpec(this);
    // Read ahead one position each way, so the next step is already cached.
    for (const q of [p + 1, p - 1]) {
      if (q < 0 || q >= this.count) continue;
      this.target(q)
        .then(({ obj, item }) => this.lines(obj, "", 0, new Map(), item))
        .catch(() => {});
    }
  }

  /** Lines for every field of obj, opening what's open. */
  async lines(obj, prefix, depth, ids, focusItem) {
    const out = [];
    const fields = obj.type.fields;
    const parts = await Promise.all(
      fields.map(async (f) => {
        const rel = prefix + f.name;
        if (f.kind === "event") return this.listLines(await obj.list(f.name), rel, depth, ids, focusItem);
        if (f.kind === "object") {
          const sub = await obj.nested(f);
          const open = this.open.has(rel);
          const id = sub.node.i ? `${sub.node.end}:${sub.node.i}` : `-${sub.leavesAt}`;
          ids.set(rel, id);
          const line = this.line(depth, rel, id, [h("span.twisty", {}, open ? "▾" : "▸"), h("span.key", {}, f.name), h("span.dim", {}, open ? "" : " {…}")], f, {
            toggle: rel,
            menu: (e) => this.objectMenu(e, sub),
          });
          if (!open) return [line];
          return [line, ...(await this.lines(sub, rel + ".", depth + 1, ids, focusItem))];
        }
        const leaf = await obj.leaf(f.name);
        return [await this.leafLine(leaf, f, rel, depth, ids, f.name)];
      }),
    );
    for (const p of parts) out.push(...p);
    return out;
  }

  async leafLine(leaf, f, rel, depth, ids, name) {
    const id = leaf.present ? leaf.off : "absent";
    ids.set(rel, id);
    const key = h("span.key", {}, name);
    if (!leaf.present) return this.line(depth, rel, id, [key, h("span.dim", {}, ": —")], f);
    const text = await leaf.text(400);
    let value;
    if (f.kind === "path") value = h("a.path", { href: "#", onclick: (e) => (e.preventDefault(), this.app.openPath(text)) }, text);
    else if (f.kind === "enum") value = h("span.enum", { "data-v": text }, text);
    else {
      const [line, cut] = firstLine(text, 200);
      value = h(`span.v${f.kind === "num" ? ".num" : ""}`, {}, f.block ? line || "(empty)" : line);
      if (cut || leaf.len > 400) {
        value = h("span.expand", { title: "Click to read it all", onclick: () => showText(`${leaf.path} (${fmtInt(leaf.len)} bytes)`, () => leaf.text(4 << 20)) }, value, h("span.more", {}, ` ▸ ${fmtInt(leaf.len)} bytes`));
      }
    }
    const v = leaf.footer.version;
    const src = h("a.src", { href: "#", title: `Written by version ${fmtInt(v)}`, onclick: (e) => (e.preventDefault(), this.app.navigate(`log:@${v}`)) }, `v${fmtInt(v)}`);
    return this.line(depth, rel, id, [key, ": ", value, src], f);
  }

  async listLines(list, rel, depth, ids, focusItem) {
    const id = `${list.count}:${list.root.end}:${list.root.i}`;
    ids.set(rel, id);
    const open = this.open.has(rel);
    const head = this.line(
      depth,
      rel,
      id,
      [h("span.twisty", {}, open ? "▾" : "▸"), h("span.key", {}, list.field.name), h("span.dim", {}, ` [${fmtInt(list.count)}]`)],
      list.field,
      { toggle: rel, menu: (e) => this.app.listMenu(e, list.path) },
    );
    if (!open || list.count === 0) return [head];
    let first = this.pages.get(rel);
    if (first == null) first = Math.max(0, list.count - PAGE);
    first = Math.max(0, Math.min(first, list.count - PAGE));
    const items = await list.range(first, first + PAGE);
    const out = [head];
    const nav = (label, to, title) => h("button.page", { title, onclick: () => (this.pages.set(rel, to), this.go(this.pos, true)) }, label);
    out.push(
      this.line(depth + 1, `${rel}#nav`, undefined, [
        h("span.dim", {}, `items ${fmtInt(first)}–${fmtInt(first + items.length - 1)} of ${fmtInt(list.count)} `),
        first > 0 ? nav("◂ earlier", Math.max(0, first - PAGE), "Earlier items") : null,
        first + PAGE < list.count ? nav("later ▸", first + PAGE, "Later items") : null,
        first + PAGE < list.count ? nav("latest", list.count - PAGE, "The latest items") : null,
      ]),
    );
    const parts = await Promise.all(
      items.map(async (it, i) => {
        const m = first + i;
        const irel = `${rel}.${m}`;
        const focus = this.mode === "items" && rel === this.listRel && m === focusItem;
        if (!it.type) {
          const l = await this.leafLine(it, list.field.item, irel, depth + 1, ids, String(m));
          if (focus) l.el.classList.add("focus");
          return [l];
        }
        const open = this.open.has(irel);
        const nid = `${it.node.end}:${it.node.i}`;
        ids.set(irel, nid);
        const line = this.line(depth + 1, irel, nid, [h("span.twisty", {}, open ? "▾" : "▸"), h("span.key", {}, String(m)), h("span.dim", {}, open ? "" : ` ${await this.preview(it)}`)], null, {
          toggle: irel,
          menu: (e) => this.objectMenu(e, it),
        });
        if (focus) line.el.classList.add("focus");
        if (!open) return [line];
        return [line, ...(await this.lines(it, irel + ".", depth + 2, ids, focusItem))];
      }),
    );
    for (const p of parts) out.push(...p);
    return out;
  }

  /** A one-line preview of an item: its first few short leaves. */
  async preview(obj) {
    const leaves = (await obj.leaves()).filter((l) => l.present && l.field && !l.field.block).slice(0, 3);
    const texts = await Promise.all(leaves.map((l) => l.text(40)));
    return leaves.map((l, i) => `${l.path.slice(obj.path.length + 1)}: ${firstLine(texts[i], 30)[0]}`).join(" · ");
  }

  line(depth, rel, id, content, field, { toggle, menu } = {}) {
    const el = h("div.line", { style: { paddingLeft: `${12 + depth * 18}px` }, title: field?.desc || "" }, content);
    if (toggle) {
      el.classList.add("openable");
      el.addEventListener("click", (e) => {
        if (e.target.closest("a, .expand")) return;
        this.open.has(toggle) ? this.open.delete(toggle) : this.open.add(toggle);
        this.go(this.pos, true);
      });
    }
    if (menu) el.addEventListener("contextmenu", menu);
    return { el, rel, id, depth };
  }

  objectMenu(e, obj) {
    contextMenu(e, [
      ["Tree view (versions)", () => this.app.navigate(`tree:${obj.path}`)],
      obj.node.i ? ["Show in log", async () => this.app.navigate(`log:@${(await obj.written()).version}`)] : null,
    ]);
  }

  show() {
    this.el.hidden = false;
    this.scroller.draw();
    this.el.focus({ preventScroll: true });
  }

  hide() {
    this.el.hidden = true;
  }

  async setPos(p) {
    if (p !== this.pos) await this.jump(p);
  }

  destroy() {}
}
