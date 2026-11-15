// The schema view: the file's description, then every type, root first, as a tree of
// fields with their types and descriptions. Named types are links to their definition,
// and each type lists the fields that use it. "Text" shows the schema as written.

import { h, typeName } from "./util.js";

export class SchemaView {
  constructor(app, file) {
    this.app = app;
    this.file = file;
    this.kind = "schema";
    this.el = h("div.view.schema-view", { tabIndex: 0 });
    this.closed = new Set(); // keys of collapsed rows
    this.asText = false;
  }

  get spec() {
    return "schema:";
  }

  get title() {
    return "schema";
  }

  async init() {
    const s = this.file.schema;
    this.textBtn = h("button", { onclick: () => ((this.asText = !this.asText), this.draw()), title: "Show the schema as written in the file" }, "Text");
    this.el.append(
      h("div.toolbar", {}, h("span.crumbs", {}, h("b", {}, "schema")), h("span.count", {}, `${s.order.length} types`), h("span.spacer"), this.textBtn),
      (this.body = h("div.schema-wrap")),
    );
    this.users = usedBy(s);
  }

  async start() {
    this.draw();
  }

  draw() {
    const s = this.file.schema;
    this.textBtn.classList.toggle("on", this.asText);
    if (this.asText) return this.body.replaceChildren(h("pre.schema-text", {}, s.text));
    const parts = [];
    if (s.desc) parts.push(h("div.file-desc", {}, s.desc));
    for (const t of [s.root, ...s.order.filter((t) => t !== s.root)]) parts.push(this.typeSection(t));
    this.body.replaceChildren(h("div.schema", {}, parts));
  }

  typeSection(t) {
    const root = t === this.file.schema.root;
    const users = this.users.get(t) ?? [];
    return h(
      "section.stype",
      { id: `type-${t.name}` },
      h("div.stype-head", {}, h("span.kw", {}, root ? "root " : "object "), h("b", {}, t.name), t.desc ? h("span.desc", {}, `# ${t.desc}`) : null),
      users.length ? h("div.used dim", {}, "used by ", users.flatMap((u, i) => [i ? ", " : "", this.typeLink(u.type, `${u.type.name}.${u.path}`)])) : null,
      this.fieldRows(t, t.name, 1),
    );
  }

  fieldRows(t, key, depth) {
    const rows = [];
    for (const f of t.fields) {
      const k = `${key}.${f.name}`;
      const inline = (f.kind === "object" && !f.type.name) || (f.kind === "event" && f.item.kind === "object" && !f.item.type.name);
      const open = inline && !this.closed.has(k);
      const row = h(
        `div.line.sfield${inline ? ".openable" : ""}`,
        { style: { paddingLeft: `${12 + depth * 18}px` } },
        h("span.twisty", {}, inline ? (open ? "▾" : "▸") : ""),
        h("span.key", {}, f.name),
        ": ",
        this.typeExpr(f),
        f.desc ? h("span.desc", {}, `# ${f.desc}`) : null,
      );
      if (inline) row.addEventListener("click", (e) => !e.target.closest("a") && (this.closed.has(k) ? this.closed.delete(k) : this.closed.add(k), this.draw()));
      rows.push(row);
      if (open) rows.push(this.fieldRows(f.kind === "event" ? f.item.type : f.type, k, depth + 1));
    }
    return h("div", {}, rows);
  }

  /** A field's type, with named types as links. */
  typeExpr(f) {
    const parts = [];
    let x = f;
    if (x.kind === "event") {
      parts.push(h("span.kw.event", { title: "An append-only list: each item is one append" }, "event "));
      x = x.item;
    }
    if (x.kind === "object") parts.push(x.type.name ? this.typeLink(x.type) : h("span.dim", {}, "{ … }"));
    else parts.push(h("span.stype-leaf", {}, typeName(x)));
    if (x.optional && x.kind === "object") parts.push("?");
    return h("span.sexpr", {}, parts);
  }

  typeLink(t, label = t.name) {
    return h(
      "a",
      {
        href: "#",
        onclick: (e) => {
          e.preventDefault();
          const el = this.body.querySelector(`#type-${CSS.escape(t.name)}`);
          el?.scrollIntoView({ block: "start", behavior: "smooth" });
          el?.classList.remove("flash");
          void el?.offsetWidth;
          el?.classList.add("flash");
        },
      },
      label,
    );
  }

  show() {
    this.el.hidden = false;
  }

  hide() {
    this.el.hidden = true;
  }

  async setPos() {}

  destroy() {}
}

/** For each named type, the fields that use it: [{ type, path }]. */
function usedBy(s) {
  const out = new Map();
  const walk = (owner, t, prefix) => {
    for (const f of t.fields) {
      const x = f.kind === "event" ? f.item : f;
      if (x.kind !== "object") continue;
      if (x.type.name) {
        if (!out.has(x.type)) out.set(x.type, []);
        out.get(x.type).push({ type: owner, path: prefix + f.name });
      } else walk(owner, x.type, `${prefix}${f.name}.`);
    }
  };
  for (const t of s.order) walk(t, t, "");
  return out;
}
