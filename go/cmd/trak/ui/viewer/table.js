// The table view: one row per item of an event list, one column per leaf. Nested
// objects are column groups, collapsed to one column until expanded; expanding one adds
// a header row that spans its leaves. Rows come in pages from the reader, and the
// scrollbar maps to the whole list.

import { Scroller } from "./scroll.js";
import { contextMenu, firstLine, fmtInt, fmtStamp, h, pattern, rendered, showText, store } from "./util.js";

const ROW = 26;
const CELL_BYTES = 300;

export class TableView {
  constructor(app, file, path) {
    this.app = app;
    this.file = file;
    this.path = path;
    this.kind = "table";
    this.rows = new Map(); // item number → row data, or a promise of it
    this.pos = 0;
    this.selected = -1;
    this.el = h("div.view.table-view", { tabIndex: 0 });
    this.drawn = -1;
    this.token = 0;
  }

  get spec() {
    return `table:${this.path}@${this.pos}`;
  }

  get title() {
    return `${this.path} · table`;
  }

  async init(pos = 0) {
    this.list = await this.file.get(this.path);
    if (!this.list.field) throw new Error(`${this.path} is not an event list`);
    this.item = this.list.field.item;
    this.layoutKey = `trak.layout.${this.file.schema.root.name}.${pattern(this.path)}`;
    this.layout = { expanded: [], hidden: [], order: {}, widths: {}, ...(store(this.layoutKey) ?? {}) };
    this.scroller = new Scroller({ label: "rows", onChange: (p) => this.go(p, false) });
    this.count = h("span.count");
    this.rowInput = h("input.row-input", { type: "text", inputMode: "numeric", placeholder: "row", "aria-label": "Go to row" });
    const go = () => {
      const n = parseInt(this.rowInput.value.replace(/[, _]/g, ""), 10);
      if (Number.isFinite(n)) this.jump(n < 0 ? this.list.count + n : n);
    };
    this.rowInput.addEventListener("keydown", (e) => e.key === "Enter" && go());
    this.toolbar = h(
      "div.toolbar",
      {},
      this.app.crumbs(this.path, "table"),
      this.count,
      h("span.spacer"),
      h("label.goto", {}, this.rowInput, h("button", { onclick: go }, "Go")),
      h("button", { onclick: (e) => this.columnsMenu(e), title: "Show hidden columns" }, "Columns"),
      h("button", { onclick: () => this.app.navigate(`items:${this.path}@${this.focusRow()}`), title: "Tree view at the selected row" }, "Tree view"),
    );
    this.head = h("div.thead");
    this.body = h("div.tbody");
    this.grid = h("div.grid", {}, this.head, this.body);
    this.wrap = h("div.grid-wrap", {}, this.grid);
    this.el.append(this.toolbar, h("div.table-main", {}, this.wrap, this.scroller.el));
    this.scroller.attach(this.wrap);
    this.scroller.attach(this.el);
    this.buildColumns();
    this.resizeObs = new ResizeObserver(() => this.resized());
    this.resizeObs.observe(this.wrap);
    this.pos = Math.max(0, Math.min(pos, this.list.count - 1));
    this.updateCount();
  }

  updateCount() {
    this.count.textContent = `${fmtInt(this.list.count)} rows`;
  }

  /** Pick up appends: the list may have grown. */
  async refresh() {
    const list = await this.file.get(this.path);
    if (list.count === this.list.count) return;
    const atEnd = this.pos >= this.scroller.max;
    this.list = list;
    this.updateCount();
    this.scroller.setRange(this.list.count, this.page);
    if (atEnd) this.scroller.set(this.scroller.max, true);
    this.drawn = -1;
    await this.go(this.scroller.pos, false);
  }

  focusRow() {
    return this.selected >= 0 ? this.selected : this.pos;
  }

  resized() {
    const page = Math.max(1, Math.floor((this.wrap.clientHeight - this.head.offsetHeight - 14) / ROW));
    if (page === this.page) return;
    this.page = page;
    this.scroller.setRange(this.list.count, page);
    this.scroller.set(this.pos, true);
    this.drawn = -1;
    this.go(this.scroller.pos, false);
  }

  /** Jump so row n is at the top, and select it. */
  async jump(n) {
    n = Math.max(0, Math.min(n, this.list.count - 1));
    this.selected = n;
    this.scroller.set(n, true);
    await this.go(this.scroller.pos, true);
  }

  // Columns. A column is { key, name, field, kind: "meta" | "leaf" | "group" | "list", children }.

  buildColumns() {
    const meta = [
      { key: "#", name: "#", kind: "meta" },
      { key: "@time", name: "time", kind: "meta" },
    ];
    let cols;
    if (this.item.kind === "object") cols = this.fieldColumns(this.item.type, "");
    else cols = [{ key: this.item.name, name: this.item.name, field: this.item, kind: "leaf" }];
    this.columns = [...meta, ...this.ordered("", cols)];
  }

  fieldColumns(type, prefix) {
    return this.ordered(
      prefix,
      type.fields.map((f) => {
        const key = prefix + f.name;
        if (f.kind === "event") return { key, name: f.name, field: f, kind: "list" };
        if (f.kind === "object") return { key, name: f.name, field: f, kind: "group", children: this.fieldColumns(f.type, key + ".") };
        return { key, name: f.name, field: f, kind: "leaf" };
      }),
    );
  }

  ordered(parent, cols) {
    const order = this.layout.order[parent];
    if (!order) return cols;
    return [...cols].sort((a, b) => rank(order, a.name) - rank(order, b.name));
  }

  /** The visible columns as leaves (collapsed groups are one leaf) and the header rows. */
  visible() {
    const hidden = new Set(this.layout.hidden);
    const expanded = new Set(this.layout.expanded);
    const leaves = [];
    let depth = 1;
    const walk = (cols, d) => {
      depth = Math.max(depth, d);
      const out = [];
      for (const c of cols) {
        if (hidden.has(c.key)) continue;
        if (c.kind === "group" && expanded.has(c.key)) {
          const kids = walk(c.children, d + 1);
          if (kids.length) out.push({ col: c, kids, span: kids.reduce((s, k) => s + k.span, 0) });
        } else {
          leaves.push(c);
          out.push({ col: c, span: 1 });
        }
      }
      return out;
    };
    const tree = walk(this.columns, 1);
    return { leaves, tree, depth };
  }

  width(c) {
    return this.layout.widths[c.key] ?? (c.kind === "meta" ? (c.key === "#" ? 80 : 190) : c.kind === "list" ? 90 : c.kind === "group" ? 220 : c.field?.block ? 320 : c.field?.kind === "num" ? 100 : 150);
  }

  save() {
    store(this.layoutKey, this.layout);
  }

  drawHead() {
    const { leaves, tree, depth } = this.visible();
    this.leaves = leaves;
    this.head.replaceChildren();
    this.head.style.gridTemplateRows = `repeat(${depth}, 24px)`;
    const total = leaves.reduce((s, c) => s + this.width(c), 0);
    this.grid.style.width = `${total}px`;
    let x = 0;
    const place = (nodes, row) => {
      for (const n of nodes) {
        const w = (n.kids ? leafCols(n) : [n.col]).reduce((s, c) => s + this.width(c), 0);
        const el = this.headCell(n, row, depth, x, w);
        this.head.append(el);
        if (n.kids) {
          const keep = x;
          place(n.kids, row + 1);
          x = keep + w;
        } else x += w;
      }
    };
    place(tree, 0);
    this.head.style.height = `${depth * 24}px`;
  }

  headCell(n, row, depth, x, w) {
    const c = n.col;
    const group = c.kind === "group";
    const rows = n.kids ? 1 : depth - row;
    const label = group ? (n.kids ? `▾ ${c.name}` : `▸ ${c.name} (${countLeaves(c)})`) : c.name;
    const el = h(
      `div.h${group ? ".group" : ""}${group && !n.kids ? ".collapsed" : ""}${c.kind === "list" ? ".list" : ""}`,
      {
        style: { left: `${x}px`, top: `${row * 24}px`, width: `${w}px`, height: `${rows * 24}px` },
        title: c.field?.desc || c.key,
        draggable: c.kind !== "meta",
        onclick: group ? () => this.toggle(c.key) : null,
        oncontextmenu: (e) => this.headMenu(e, c, !!n.kids),
      },
      h("span.label", {}, label),
    );
    if (c.kind !== "meta") {
      el.addEventListener("dragstart", (e) => {
        e.dataTransfer.setData("text/x-trak-col", c.key);
        e.dataTransfer.effectAllowed = "move";
      });
      el.addEventListener("dragover", (e) => {
        if ([...e.dataTransfer.types].includes("text/x-trak-col")) {
          e.preventDefault();
          el.classList.add("drop");
        }
      });
      el.addEventListener("dragleave", () => el.classList.remove("drop"));
      el.addEventListener("drop", (e) => {
        e.preventDefault();
        el.classList.remove("drop");
        this.move(e.dataTransfer.getData("text/x-trak-col"), c.key);
      });
    }
    if (!n.kids) {
      const grip = h("div.grip", { title: "Drag to resize" });
      grip.addEventListener("pointerdown", (e) => this.resize(e, c));
      grip.addEventListener("click", (e) => e.stopPropagation());
      el.append(grip);
    }
    return el;
  }

  toggle(key) {
    const s = new Set(this.layout.expanded);
    s.has(key) ? s.delete(key) : s.add(key);
    this.layout.expanded = [...s];
    this.save();
    this.redraw();
  }

  /** Move column `from` before column `to`, if they share a parent. */
  move(from, to) {
    const parent = (k) => (k.includes(".") ? k.slice(0, k.lastIndexOf(".") + 1) : "");
    if (!from || from === to || parent(from) !== parent(to)) return;
    const p = parent(from);
    const siblings = p ? findCol(this.columns, p.slice(0, -1)).children : this.columns.filter((c) => c.kind !== "meta");
    const names = siblings.map((c) => c.name);
    const fromName = from.slice(p.length);
    names.splice(names.indexOf(fromName), 1);
    names.splice(names.indexOf(to.slice(p.length)), 0, fromName);
    this.layout.order[p] = names;
    this.save();
    this.buildColumns();
    this.redraw();
  }

  resize(e, c) {
    e.preventDefault();
    e.stopPropagation();
    const start = e.clientX;
    const w0 = this.width(c);
    const grip = e.target;
    grip.setPointerCapture(e.pointerId);
    const move = (ev) => {
      this.layout.widths[c.key] = Math.max(40, Math.round(w0 + ev.clientX - start));
      this.drawHead();
      this.drawRows(true);
    };
    const up = () => {
      grip.removeEventListener("pointermove", move);
      grip.removeEventListener("pointerup", up);
      this.save();
    };
    grip.addEventListener("pointermove", move);
    grip.addEventListener("pointerup", up);
  }

  headMenu(e, c, open) {
    const items = [];
    if (c.kind === "group") items.push([open ? "Collapse" : "Expand", () => this.toggle(c.key)]);
    if (c.kind !== "meta") items.push([c.kind === "group" ? "Hide group" : "Hide column", () => this.hide(c.key)]);
    if (this.layout.hidden.length) items.push(["Show all columns", () => this.showAll()]);
    if (this.layout.widths[c.key]) items.push(["Reset width", () => (delete this.layout.widths[c.key], this.save(), this.redraw())]);
    items.push(["Reset layout", () => this.resetLayout()]);
    contextMenu(e, items);
  }

  hide(key) {
    this.layout.hidden = [...new Set([...this.layout.hidden, key])];
    this.save();
    this.redraw();
  }

  showAll() {
    this.layout.hidden = [];
    this.save();
    this.redraw();
  }

  resetLayout() {
    this.layout = { expanded: [], hidden: [], order: {}, widths: {} };
    this.save();
    this.buildColumns();
    this.redraw();
  }

  columnsMenu(e) {
    const items = this.layout.hidden.map((k) => [`Show ${k}`, () => ((this.layout.hidden = this.layout.hidden.filter((x) => x !== k)), this.save(), this.redraw())]);
    if (!items.length) items.push(["No hidden columns", () => {}]);
    else items.push(["Show all", () => this.showAll()]);
    contextMenu(e, items);
  }

  redraw() {
    this.drawHead();
    this.resized();
    this.drawRows(true);
  }

  // Rows.

  /** Row data for item m: { m, stamp, version, cells: Map(key → cell) }. */
  loadRows(lo, hi) {
    hi = Math.min(hi, this.list.count);
    const missing = [];
    for (let m = lo; m < hi; m++) if (!this.rows.has(m)) missing.push(m);
    // Contiguous runs share trie reads.
    const runs = [];
    for (const m of missing) {
      const r = runs[runs.length - 1];
      if (r && r[1] === m) r[1]++;
      else runs.push([m, m + 1]);
    }
    for (const [a, b] of runs) {
      const p = this.list.range(a, b).then((items) => Promise.all(items.map((it, i) => this.rowOf(a + i, it))));
      for (let m = a; m < b; m++) {
        const one = p.then((rows) => {
          this.rows.set(m, rows[m - a]);
          return rows[m - a];
        });
        one.catch(() => this.rows.delete(m));
        this.rows.set(m, one);
      }
    }
    if (this.rows.size > 4000) {
      for (const k of [...this.rows.keys()]) {
        if (k < lo - 1500 || k > hi + 1500) this.rows.delete(k);
        if (this.rows.size <= 3000) break;
      }
    }
    const out = [];
    for (let m = lo; m < hi; m++) out.push(this.rows.get(m));
    return Promise.all(out);
  }

  async rowOf(m, item) {
    const cells = new Map();
    let ft;
    if (item.leaves) {
      const leaves = await item.leaves();
      ft = leaves[0]?.footer ?? (await item.leafFooter());
      const [texts, lists] = await Promise.all([
        Promise.all(leaves.map((l) => (l.present ? l.text(CELL_BYTES) : null))),
        Promise.all(item.type.fields.filter((f) => f.kind === "event").map((f) => item.list(f.name))),
      ]);
      leaves.forEach((l, i) => cells.set(l.path.slice(item.path.length + 1), { leaf: l, text: texts[i] }));
      for (const l of lists) cells.set(l.path.slice(item.path.length + 1), { list: l });
    } else {
      ft = item.footer;
      cells.set(this.item.name, { leaf: item, text: await item.text(CELL_BYTES) });
    }
    return { m, item, stamp: ft.stamp, version: ft.version, cells };
  }

  /** Show rows from position p. `fromJump` times a jump rather than a scroll. */
  async go(p, fromJump) {
    const t0 = performance.now();
    this.pos = p;
    const token = ++this.token;
    if (this.page == null) this.resized();
    const rows = await this.loadRows(p, p + this.page);
    if (token !== this.token) return;
    this.current = rows;
    this.drawRows();
    rendered(fromJump ? "table-jump" : "table-scroll", t0);
    this.app.updateSpec(this);
    // Read ahead a page each way.
    this.loadRows(Math.max(0, p - this.page), p).catch(() => {});
    this.loadRows(p + this.page, p + 2 * this.page).catch(() => {});
  }

  drawRows(force = false) {
    if (!this.current) return;
    if (!this.leaves) this.drawHead();
    if (!force && this.drawn === this.pos && this.drawnRows === this.current) return;
    this.drawn = this.pos;
    this.drawnRows = this.current;
    const frag = document.createDocumentFragment();
    for (const r of this.current) {
      const row = h("div.row", { "data-m": r.m, onclick: () => this.select(r.m), oncontextmenu: (e) => this.rowMenu(e, r) });
      if (r.m === this.selected) row.classList.add("hit");
      for (const c of this.leaves) row.append(this.cell(r, c));
      frag.append(row);
    }
    this.body.replaceChildren(frag);
  }

  select(m) {
    this.selected = m;
    for (const el of this.body.children) el.classList.toggle("hit", Number(el.dataset.m) === m);
  }

  rowMenu(e, r) {
    contextMenu(e, [
      ["Tree view at this row", () => this.app.navigate(`items:${this.path}@${r.m}`)],
      r.item.at ? ["Versions of this item", () => this.app.navigate(`tree:${r.item.path}`)] : null,
      ["Show in log", () => this.app.navigate(`log:@${r.version}`)],
    ]);
  }

  cell(r, c) {
    const w = this.width(c);
    const el = h("div.c", { style: { width: `${w}px` } });
    if (c.kind === "meta") {
      el.classList.add(c.key === "#" ? "n" : "time");
      el.textContent = c.key === "#" ? fmtInt(r.m) : fmtStamp(r.stamp);
      return el;
    }
    if (c.kind === "group") {
      el.classList.add("summary");
      const parts = [];
      for (const [k, v] of r.cells) if (k.startsWith(c.key + ".") && v.text != null) parts.push(`${k.slice(c.key.length + 1)}: ${firstLine(v.text, 40)[0]}`);
      el.textContent = parts.join(" · ");
      el.title = "Click the header to expand";
      return el;
    }
    const v = r.cells.get(c.key);
    if (c.kind === "list") {
      const l = v?.list;
      el.classList.add("count");
      el.append(h("a", { href: "#", onclick: (e) => (e.preventDefault(), this.app.navigate(`table:${l.path}@0`)) }, `${fmtInt(l?.count ?? 0)} ▸`));
      el.addEventListener("contextmenu", (e) => this.app.listMenu(e, l.path));
      return el;
    }
    if (!v || v.text == null) {
      el.classList.add("absent");
      el.textContent = "—";
      return el;
    }
    const f = c.field;
    const [line, cut] = firstLine(v.text);
    const more = cut || v.leaf.len > CELL_BYTES;
    if (f.kind === "num") el.classList.add("num");
    if (f.kind === "enum") {
      el.append(h("span.enum", { "data-v": v.text }, v.text));
    } else if (f.kind === "path") {
      el.append(h("a.path", { href: "#", onclick: (e) => (e.preventDefault(), this.app.openPath(v.text)) }, v.text));
    } else {
      el.append(h("span", {}, line));
      if (more) {
        el.classList.add("expand");
        el.append(h("span.more", {}, ` ▸ ${fmtBytesShort(v.leaf.len)}`));
        el.title = "Click to expand";
        el.addEventListener("click", () => showText(`${v.leaf.path} (${fmtInt(v.leaf.len)} bytes)`, () => v.leaf.text(4 << 20)));
      }
    }
    return el;
  }

  show() {
    this.el.hidden = false;
    if (this.drawn === -1 && this.page) this.go(this.pos, true);
    this.el.focus({ preventScroll: true });
  }

  hide() {
    this.el.hidden = true;
  }

  /** Apply a position from the URL. */
  async setPos(p) {
    if (p !== this.pos) await this.jump(p);
  }

  async start(pos) {
    this.drawHead();
    this.resized();
    this.scroller.set(pos, true);
    this.selected = pos > 0 ? pos : -1;
    await this.go(this.scroller.pos, true);
  }

  destroy() {
    this.resizeObs?.disconnect();
  }
}

function rank(order, name) {
  const i = order.indexOf(name);
  return i < 0 ? 1e9 : i;
}

function leafCols(n) {
  return n.kids ? n.kids.flatMap(leafCols) : [n.col];
}

function countLeaves(c) {
  return c.children ? c.children.reduce((s, k) => s + countLeaves(k), 0) : 1;
}

function findCol(cols, key) {
  for (const c of cols) {
    if (c.key === key) return c;
    if (c.children) {
      const x = findCol(c.children, key);
      if (x) return x;
    }
  }
  return null;
}

function fmtBytesShort(n) {
  return n < 1000 ? `${n} B` : n < 1e6 ? `${(n / 1000).toFixed(1)} KB` : `${(n / 1e6).toFixed(1)} MB`;
}
