// Columns and cell values, shared by the table and log views.
//
// A row costs its footer plus small reads: the value walk gives every leaf's offsets from
// the footer alone, so a cell reads at most PREVIEW bytes of its value, and block values
// are read in full only when someone expands them (SPEC §12).

const utf8 = new TextDecoder("utf-8");
export const PREVIEW = 240;

/**
 * Columns for a schema: one per leaf outside lists and maps, and one per top-level list or
 * map (shown as a count you can expand). Each column has `path` (segments), `kind`
 * ("leaf" | "list" | "map"), the leaf `field` when there is one, and `description`.
 */
export function columns(schema, own = null) {
  const out = [];
  const seen = new Set();
  for (const f of schema.fields) {
    const segs = f.path.split(".");
    const cut = segs.findIndex((s) => s === "[]" || s === "{}");
    const path = cut < 0 ? segs : segs.slice(0, cut);
    const key = path.join(".");
    if (seen.has(key)) continue;
    seen.add(key);
    const kind = cut < 0 ? "leaf" : segs[cut] === "[]" ? "list" : "map";
    out.push({
      key,
      path,
      kind,
      field: cut < 0 ? f : null,
      description: f.description,
      owner: own == null ? schema.name : own(path[0]),
    });
  }
  return out;
}

/** Columns for a family table: the family type's, then each descendant's own, in order. */
export function familyColumns(family) {
  const out = [];
  const seen = new Set();
  for (const s of family.types) {
    const parent = s === family.schema || s.parent == null ? null : family.reader.schema(s.parent);
    const inherited = new Set(parent?.root ? parent.root.members.map((m) => m.key) : []);
    for (const c of columns(s)) {
      if (inherited.has(c.path[0]) || seen.has(c.key)) continue;
      seen.add(c.key);
      out.push({ ...c, owner: s.name });
    }
  }
  return out;
}

/** Walk the decoded tree to the node at `path` (object keys only). */
function nodeAt(tree, path) {
  let n = tree;
  for (const k of path) {
    if (n == null || n.kind !== "object") return undefined;
    n = n.members[k];
  }
  return n;
}

/**
 * The cells of one event for the given columns, as `{ text, more, kind, count, leaf }`:
 * `text` is a short preview, `more` says the value was cut, `count` is a list's or map's
 * length. Reads at most PREVIEW bytes per leaf.
 */
export async function rowCells(reader, event, cols) {
  if (event.schema.root == null) return cols.map(() => ({ text: "", unavailable: true }));
  const w = event.walk();
  return Promise.all(
    cols.map(async (c) => {
      const node = nodeAt(w.tree, c.path);
      if (node === undefined) return { text: "", missing: true };
      if (node === null) return { text: "", absent: true };
      if (c.kind === "list") return { kind: "list", count: node.items.length, text: plural(node.items.length, "item", "items") };
      if (c.kind === "map") return { kind: "map", count: node.entries.length, text: plural(node.entries.length, "entry", "entries") };
      if (typeof node !== "number") return { text: "" };
      const rec = w.leaves[node];
      const bytes = await reader.read(rec.voff, Math.min(rec.length, PREVIEW));
      const text = utf8.decode(bytes);
      return { kind: "leaf", leaf: rec, text, more: rec.length > PREVIEW, block: rec.leaf.block, length: rec.length };
    }),
  );
}

export function plural(n, one, many) {
  return `${n.toLocaleString()} ${n === 1 ? one : many}`;
}

/** A whole leaf's value as text. */
export async function leafText(reader, rec) {
  return utf8.decode(await reader.read(rec.voff, rec.length));
}

/** One line of a preview for a table cell: line breaks and tabs shown as ↵ and →. */
export function oneLine(text, max = 80) {
  const s = text.replace(/\r?\n/g, "↵").replace(/\t/g, "→");
  return s.length > max ? `${s.slice(0, max)}…` : s;
}

export function formatBytes(n) {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n;
  let i = -1;
  do {
    v /= 1024;
    i++;
  } while (v >= 1024 && i < units.length - 1);
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

export function formatStampShort(stamp) {
  // 20261107T140210.120Z -> 2026-11-07 14:02:10.120
  return `${stamp.slice(0, 4)}-${stamp.slice(4, 6)}-${stamp.slice(6, 8)} ${stamp.slice(9, 11)}:${stamp.slice(11, 13)}:${stamp.slice(13, 19)}`;
}

/** Create an element: el("div", { class: "x", onclick }, child, "text", …). */
export function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else if (k === "class") e.className = v;
    else if (k === "style" && typeof v === "object") Object.assign(e.style, v);
    else e.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    e.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return e;
}

/** Render a native value (from `event.native()`) of schema type `type` as a tree. */
export function valueTree(type, value, depth = 0) {
  if (value === null) return el("span", { class: "absent" }, "absent");
  if (type.kind === "str" || type.kind === "num" || type.kind === "enum") {
    if (type.block || value.includes("\n")) return el("pre", { class: "block-value" }, value);
    return el("span", { class: "leaf-value" }, value);
  }
  let entries;
  let label;
  if (type.kind === "object") {
    entries = type.members.map((m) => [m.key, m.type, value[m.key]]);
    label = plural(entries.length, "field", "fields");
  } else if (type.kind === "list") {
    entries = value.map((v, i) => [String(i), type.elem, v]);
    label = plural(entries.length, "item", "items");
  } else {
    entries = value.map(([k, v]) => [k ?? "(unknown key)", type.value, v]);
    label = plural(entries.length, "entry", "entries");
  }
  if (entries.length === 0) return el("span", { class: "absent" }, type.kind === "object" ? "{}" : "empty");
  const d = el("details", { open: depth < 2 ? true : null, class: "tree" });
  d.append(el("summary", {}, label));
  const dl = el("dl");
  for (const [k, t, v] of entries) dl.append(el("dt", {}, k), el("dd", {}, valueTree(t, v, depth + 1)));
  d.append(dl);
  return d;
}
