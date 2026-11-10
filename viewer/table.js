// <trak-table>-style widget: one type or family as a virtual-scrolling table.
//
//   import { TrakTable } from "./table.js";
//   const t = new TrakTable(element, { reader, name: "build", family: false });
//   await t.load();
//
// Only the visible rows are read: each costs its footer plus small reads per cell. Rows are
// numbered by `n` (or by family number), so scrolling anywhere is one O(log n) walk.
// Leaves inside nested objects get stacked header rows; click a group header to collapse it.

import { columns, el, familyColumns, formatBytes, formatStampShort, leafText, oneLine, rowCells, valueTree } from "./cells.js";

const ROW = 28;
const MAX_SPACER = 8_000_000; // browsers cap element heights; past this, scrolling is scaled
const OVERSCAN = 6;

const WIDTH = { num: 96, enum: 110, str: 200, block: 170, list: 110, map: 110, group: 120 };

export class TrakTable {
  constructor(container, { reader, name, family = false, onEvent = null, onDetail = null }) {
    this.reader = reader;
    this.name = name;
    this.isFamily = family;
    this.source = family ? reader.family(name) : reader.table(name);
    this.onEvent = onEvent;
    this.onDetail = onDetail ?? ((title, body) => this.dialog(title, body));
    this.collapsed = new Set();
    this.rows = new Map();
    this.generation = 0;
    this.root = el("div", { class: "trak-table" });
    container.replaceChildren(this.root);
  }

  async load() {
    this.count = await this.source.count();
    this.allColumns = this.isFamily ? familyColumns(this.source) : columns(this.source.schema);
    this.build();
    this.scrollToRow(this.count, "end");
  }

  /** Re-read the row count, e.g. after the reader picked up appended objects. */
  async refresh() {
    const atEnd = this.body.scrollTop + this.body.clientHeight >= this.body.scrollHeight - 2 * ROW;
    this.source = this.isFamily ? this.reader.family(this.name) : this.reader.table(this.name);
    this.count = await this.source.count();
    this.rows.clear();
    this.sizeSpacer();
    if (atEnd) this.scrollToRow(this.count, "end");
    else this.draw();
  }

  // --- layout ---------------------------------------------------------------------------

  visibleColumns() {
    const out = [];
    const done = new Set();
    for (const c of this.allColumns) {
      const g = c.path.findIndex((_, i) => this.collapsed.has(c.path.slice(0, i + 1).join(".")));
      if (g < 0) {
        out.push(c);
        continue;
      }
      const key = c.path.slice(0, g + 1).join(".");
      if (done.has(key)) continue;
      done.add(key);
      const hidden = this.allColumns.filter((x) => x.key === key || x.key.startsWith(`${key}.`)).length;
      out.push({ key, path: c.path.slice(0, g + 1), kind: "group", hidden, owner: c.owner });
    }
    return out;
  }

  width(c) {
    if (c.kind === "group") return WIDTH.group;
    if (c.kind !== "leaf") return WIDTH[c.kind];
    return c.field.block ? WIDTH.block : WIDTH[c.field.type] ?? WIDTH.str;
  }

  build() {
    this.cols = this.visibleColumns();
    const fixed = [70, 196, ...(this.isFamily ? [130] : [])];
    this.template = [...fixed, ...this.cols.map((c) => this.width(c))].map((w) => `${w}px`).join(" ");
    const depth = Math.max(1, ...this.cols.map((c) => c.path.length));
    const head = el("div", { class: "head", style: { gridTemplateColumns: this.template } });
    const fixedNames = ["#", "time", ...(this.isFamily ? ["type"] : [])];
    fixedNames.forEach((name, i) => {
      head.append(el("div", { class: "h fixed", style: { gridColumn: `${i + 1}`, gridRow: `1 / ${depth + 1}` } }, name));
    });
    const off = fixed.length + 1;
    // Group cells: consecutive columns sharing a path prefix.
    for (let level = 0; level < depth - 1; level++) {
      let i = 0;
      while (i < this.cols.length) {
        const c = this.cols[i];
        if (c.path.length - 1 <= level || c.kind === "group" && c.path.length - 1 <= level) {
          i++;
          continue;
        }
        const prefix = c.path.slice(0, level + 1).join(".");
        let j = i + 1;
        while (j < this.cols.length && this.cols[j].path.length - 1 > level && this.cols[j].path.slice(0, level + 1).join(".") === prefix) j++;
        head.append(
          el("button", {
            class: "h group",
            title: `Hide ${prefix}`,
            style: { gridColumn: `${off + i} / ${off + j}`, gridRow: `${level + 1}` },
            onclick: () => this.toggle(prefix),
          }, `▾ ${c.path[level]}`),
        );
        i = j;
      }
    }
    this.cols.forEach((c, i) => {
      const label = c.kind === "group" ? `▸ ${c.path[c.path.length - 1]} (${c.hidden})` : c.path[c.path.length - 1] + (c.kind === "list" ? " []" : c.kind === "map" ? " {}" : "");
      const tip = [c.key, c.field?.type, c.field?.block ? "block" : null, c.description, this.isFamily ? `from ${c.owner}` : null]
        .filter(Boolean)
        .join(" · ");
      const attrs = {
        class: `h leaf ${c.kind === "group" ? "group collapsed" : ""} ${c.field?.type === "num" ? "num" : ""}`,
        title: c.kind === "group" ? `Show ${c.key}` : tip,
        style: { gridColumn: `${off + i}`, gridRow: `${c.path.length} / ${depth + 1}` },
      };
      if (c.kind === "group") attrs.onclick = () => this.toggle(c.key);
      head.append(el(c.kind === "group" ? "button" : "div", attrs, label));
    });
    this.spacer = el("div", { class: "spacer" });
    this.window = el("div", { class: "rows" });
    // One scroll container for both axes: the header is sticky, so it scrolls sideways with
    // the rows. The canvas below it holds the spacer and the drawn window of rows.
    this.head = head;
    this.body = el("div", { class: "body", onscroll: () => this.schedule() }, head, el("div", { class: "canvas" }, this.spacer, this.window));
    this.toolbar = this.makeToolbar();
    this.root.replaceChildren(this.toolbar, this.body);
    this.sizeSpacer();
    new ResizeObserver(() => this.schedule()).observe(this.body);
  }

  makeToolbar() {
    const n = el("input", { type: "number", min: 1, placeholder: "row", class: "row-input", "aria-label": "Go to row" });
    const t = el("input", { type: "text", placeholder: "2026-11-07T14:00:00Z", class: "time-input", "aria-label": "Go to time" });
    const go = async (e) => {
      e.preventDefault();
      if (n.value) this.scrollToRow(Number(n.value), "center");
      else if (t.value) {
        const hit = await this.source.atTime(t.value.trim());
        if (hit) this.scrollToRow(this.isFamily ? hit.familyNumber(this.name) : hit.n, "start");
      }
    };
    this.countLabel = el("span", { class: "count" });
    return el(
      "form",
      { class: "toolbar", onsubmit: go },
      el("strong", {}, this.isFamily ? `${this.name} family` : this.name),
      this.countLabel,
      el("span", { class: "grow" }),
      el("label", {}, "Row ", n),
      el("label", {}, "Time ≥ ", t),
      el("button", { type: "submit" }, "Go"),
      el("button", { type: "button", onclick: () => this.scrollToRow(1, "start") }, "First"),
      el("button", { type: "button", onclick: () => this.scrollToRow(this.count, "end") }, "Last"),
    );
  }

  toggle(prefix) {
    if (this.collapsed.has(prefix)) this.collapsed.delete(prefix);
    else this.collapsed.add(prefix);
    const first = this.firstVisible ?? 1;
    const highlight = this.highlight;
    this.rows.clear();
    this.build();
    this.scrollToRow(first, "start");
    this.highlight = highlight;
  }

  sizeSpacer() {
    const h = this.count * ROW;
    this.scaled = h > MAX_SPACER;
    this.spacer.style.height = `${Math.min(h, MAX_SPACER)}px`;
    this.countLabel.textContent = `${this.count.toLocaleString()} rows`;
  }

  // --- scrolling ------------------------------------------------------------------------

  /** Row number at the top of the viewport, from the scroll position. */
  view() {
    return Math.max(ROW, this.body.clientHeight - this.head.offsetHeight);
  }

  topRow() {
    const view = this.view();
    const visible = Math.max(1, Math.floor(view / ROW));
    if (!this.scaled) return Math.floor(this.body.scrollTop / ROW) + 1;
    const max = Math.max(1, this.body.scrollHeight - this.body.clientHeight);
    return Math.floor((this.body.scrollTop / max) * Math.max(0, this.count - visible)) + 1;
  }

  scrollToRow(n, where = "start") {
    n = Math.max(1, Math.min(this.count, n));
    const view = this.body.clientHeight ? this.view() : 600;
    const visible = Math.max(1, Math.floor(view / ROW));
    let top = where === "end" ? n - visible + 1 : where === "center" ? n - Math.floor(visible / 2) : n;
    top = Math.max(1, Math.min(top, Math.max(1, this.count - visible + 1)));
    if (!this.scaled) {
      this.body.scrollTop = (top - 1) * ROW;
    } else {
      const max = Math.max(1, this.body.scrollHeight - this.body.clientHeight);
      this.body.scrollTop = ((top - 1) / Math.max(1, this.count - visible)) * max;
    }
    this.highlight = where === "center" || where === "start" ? n : null;
    this.schedule();
  }

  schedule() {
    if (this.pending) return;
    this.pending = requestAnimationFrame(() => {
      this.pending = null;
      this.draw();
    });
  }

  async draw() {
    const gen = ++this.generation;
    if (this.count === 0) {
      this.window.replaceChildren(el("div", { class: "empty" }, "No events yet."));
      return;
    }
    const visible = Math.ceil(this.view() / ROW) + 1;
    const top = this.topRow();
    this.firstVisible = top;
    const a = Math.max(1, top - (this.scaled ? 0 : OVERSCAN));
    const b = Math.min(this.count, top + visible + (this.scaled ? 0 : OVERSCAN));
    const offset = this.scaled ? this.body.scrollTop : (a - 1) * ROW;
    this.window.style.transform = `translateY(${offset}px)`;
    // Draw what we have now; fetch the rest, then draw again.
    this.paint(a, b);
    const missing = [];
    for (let n = a; n <= b; n++) if (!this.rows.has(n)) missing.push(n);
    if (missing.length === 0) return;
    try {
      const lo = missing[0];
      const hi = missing[missing.length - 1];
      for await (const e of this.source.range(lo, hi)) {
        if (gen !== this.generation) return;
        const n = this.isFamily ? e.familyNumber(this.name) : e.n;
        if (this.rows.has(n)) continue;
        this.rows.set(n, { event: e, cells: await rowCells(this.reader, e, this.cellColumns(e)) });
      }
      if (this.rows.size > 4000) {
        for (const k of [...this.rows.keys()].slice(0, this.rows.size - 2000)) this.rows.delete(k);
      }
      if (gen === this.generation) this.paint(a, b);
    } catch (err) {
      this.window.replaceChildren(el("div", { class: "error" }, String(err.message ?? err)));
    }
  }

  /** The cells to read for an event: groups are hidden, and a family's columns that the
   * event's type doesn't have are empty. */
  cellColumns(e) {
    const have = new Set(e.schema.fields.map((f) => f.path.split(".")[0]));
    return this.cols.map((c) => (c.kind === "group" || !have.has(c.path[0]) ? { ...c, path: ["\u0000none"] } : c));
  }

  paint(a, b) {
    const frag = document.createDocumentFragment();
    for (let n = a; n <= b; n++) {
      const row = this.rows.get(n);
      const cls = `row ${n % 2 ? "odd" : ""} ${n === this.highlight ? "hit" : ""}`;
      const r = el("div", { class: cls, style: { gridTemplateColumns: this.template } });
      if (row == null) {
        r.append(el("div", { class: "c n" }, n.toLocaleString()), el("div", { class: "c loading" }, "…"));
        frag.append(r);
        continue;
      }
      const e = row.event;
      r.append(
        el("button", { class: "c n", title: "Show the whole event", onclick: () => this.openEvent(e) }, n.toLocaleString()),
        el("div", { class: "c time", title: e.timestamp }, formatStampShort(e.timestamp)),
      );
      if (this.isFamily) r.append(el("div", { class: "c type", title: `${e.type} ${e.n}` }, e.type));
      row.cells.forEach((cell, i) => r.append(this.cell(e, this.cols[i], cell)));
      frag.append(r);
    }
    this.window.replaceChildren(frag);
  }

  cell(e, col, cell) {
    const num = col.field?.type === "num" ? " num" : "";
    if (cell.unavailable) return el("div", { class: "c muted" }, "(values unavailable)");
    if (cell.missing || col.kind === "group") return el("div", { class: "c muted" });
    if (cell.absent) return el("div", { class: "c absent" }, "—");
    if (cell.kind === "list" || cell.kind === "map") {
      if (cell.count === 0) return el("div", { class: "c muted" }, cell.kind === "list" ? "[]" : "{}");
      return el("button", { class: "c expand", onclick: () => this.openPart(e, col) }, `▸ ${cell.text}`);
    }
    if (cell.block || cell.more) {
      const lines = cell.block ? ` · ${formatBytes(cell.length)}` : "";
      return el(
        "button",
        { class: `c expand${num}`, title: "Show the whole value", onclick: () => this.openLeaf(e, col, cell) },
        cell.block ? `▸ ${oneLine(cell.text, 40)}${lines}` : `${oneLine(cell.text, 60)}`,
      );
    }
    return el("div", { class: `c${num}`, title: cell.text.length > 24 ? cell.text : null }, oneLine(cell.text));
  }

  async openLeaf(e, col, cell) {
    const text = await leafText(this.reader, cell.leaf);
    this.onDetail(`${e.type} ${e.n} · ${col.key}`, el("pre", { class: "block-value" }, text));
  }

  async openPart(e, col) {
    const native = await e.native();
    let v = native;
    let t = e.schema.root;
    for (const k of col.path) {
      v = v?.[k];
      t = t.members.find((m) => m.key === k).type;
    }
    this.onDetail(`${e.type} ${e.n} · ${col.key}`, valueTree(t, v));
  }

  async openEvent(e) {
    if (this.onEvent) return this.onEvent(e);
    const body = e.schema.root ? valueTree(e.schema.root, await e.native()) : el("p", {}, "Values unavailable.");
    this.onDetail(`${e.type} ${e.n} · ${e.timestamp}`, body);
  }

  dialog(title, body) {
    const d = el(
      "dialog",
      { class: "trak-detail", onclose: () => d.remove() },
      el("form", { method: "dialog", class: "detail-head" }, el("strong", {}, title), el("button", { "aria-label": "Close" }, "✕")),
      el("div", { class: "detail-body" }, body),
    );
    this.root.append(d);
    d.showModal();
  }
}
