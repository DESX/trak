// The trak viewer: open a file (drop it, pick it, or browse files served by `trak serve`),
// list its types and families, and show them as a log or as tables.

import { BlobSource, check, HttpSource, Reader } from "../js/src/index.js";
import { el, formatBytes, valueTree } from "./cells.js";
import { LogView } from "./log.js";
import { TrakTable } from "./table.js";

const $ = (sel) => document.querySelector(sel);
const state = { reader: null, label: "", view: null, follow: null, http: false };

// --- sources ------------------------------------------------------------------------------

async function openSource(source, label) {
  stopFollow();
  setStatus(`Opening ${label}…`);
  try {
    state.reader = await Reader.open(source);
  } catch (e) {
    setStatus(`Can't open ${label}: ${e.message}`, true);
    return;
  }
  state.label = label;
  state.http = source instanceof HttpSource;
  $("#file-name").textContent = label;
  document.title = `${label} · trak`;
  $("#follow").hidden = !state.http;
  $("#validate").hidden = false;
  await renderTypes();
  await showLog();
}

async function openFile(file) {
  location.hash = "";
  await openSource(new BlobSource(file), file.name);
}

async function openRemote(path) {
  location.hash = `file=${encodeURIComponent(path)}`;
  try {
    await openSource(await HttpSource.open(`files/${path.split("/").map(encodeURIComponent).join("/")}`), path);
  } catch (e) {
    setStatus(`Can't open ${path}: ${e.message}`, true);
  }
}

/** Ask the local service (`trak serve`) for a directory listing; null when there's none. */
async function listDir(dir) {
  try {
    const res = await fetch(`api/files?dir=${encodeURIComponent(dir)}`, { cache: "no-store" });
    if (!res.ok || !(res.headers.get("content-type") ?? "").includes("json")) return null;
    return await res.json();
  } catch {
    return null;
  }
}

function panel() {
  const p = el("section", { class: "browse" });
  $("#main").replaceChildren(p);
  state.view = null;
  return p;
}

function intro() {
  panel().append(
    el("h2", {}, "Open a trak file"),
    el("p", {}, "Drop a .trak file anywhere on this page, or use Open file. It is read in your browser and never uploaded."),
    el("p", { class: "muted" }, "To browse the files on a machine, run trak serve DIR there and open its address."),
  );
}

async function browse(dir = "") {
  const list = await listDir(dir);
  if (list == null) return false;
  const items = el("ul", { class: "files" });
  if (dir) {
    const up = dir.includes("/") ? dir.slice(0, dir.lastIndexOf("/")) : "";
    items.append(el("li", {}, el("button", { class: "dir", onclick: () => browse(up) }, "‹ up")));
  }
  for (const d of list.dirs) {
    const path = dir ? `${dir}/${d}` : d;
    items.append(el("li", {}, el("button", { class: "dir", onclick: () => browse(path) }, `${d}/`)));
  }
  for (const f of list.files) {
    const path = dir ? `${dir}/${f.name}` : f.name;
    items.append(
      el("li", {}, el("button", { class: "file", onclick: () => openRemote(path) }, f.name, el("span", { class: "muted" }, formatBytes(f.size)))),
    );
  }
  if (!list.dirs.length && !list.files.length) items.append(el("li", { class: "muted" }, "No .trak files here."));
  panel().append(el("h2", {}, list.root + (dir ? `/${dir}` : "")), items);
  return true;
}

// --- types --------------------------------------------------------------------------------

async function renderTypes() {
  const r = state.reader;
  const nav = $("#types");
  const tree = el("ul", { class: "type-tree" });
  const byParent = new Map();
  for (const s of r.types()) {
    const key = s.parent ?? "";
    if (!byParent.has(key)) byParent.set(key, []);
    byParent.get(key).push(s);
  }
  const counts = await Promise.all(r.types().map((s) => r.table(s.name).count()));
  const countOf = new Map(r.types().map((s, i) => [s.name, counts[i]]));
  const add = (parent, into) => {
    for (const s of byParent.get(parent) ?? []) {
      const kids = byParent.get(s.name);
      const desc = s.description ? s.description.split("\n")[0] : "";
      const li = el(
        "li",
        {},
        el(
          "div",
          { class: "type" },
          el("button", { class: "name", "data-key": s.name, title: s.description ?? s.name, onclick: () => showTable(s.name, false) }, s.parent ? s.name.slice(s.parent.length + 1) : s.name),
          el("span", { class: "n" }, countOf.get(s.name).toLocaleString()),
          kids ? el("button", { class: "fam", "data-key": `${s.name}*`, title: `All events of ${s.name} and its children, in order`, onclick: () => showTable(s.name, true) }, "family") : null,
        ),
        desc ? el("div", { class: "desc" }, desc) : null,
        s.error ? el("div", { class: "desc warn" }, `values unavailable: ${s.error.message}`) : null,
      );
      if (kids) {
        const sub = el("ul");
        add(s.name, sub);
        li.append(sub);
      }
      into.append(li);
    }
  };
  add("", tree);
  nav.replaceChildren(
    el("button", { class: "log-link", "data-key": "log", onclick: () => showLog() }, "Log (file order)"),
    r.types().length ? tree : el("p", { class: "muted" }, "No types."),
  );
  updateStatus();
}

// --- views --------------------------------------------------------------------------------

async function showLog(event = null) {
  const main = $("#main");
  const v = new LogView(main, { reader: state.reader, onEvent: (e) => showEvent(e) });
  state.view = v;
  if (event) await v.show(event);
  else await v.load();
  mark(null);
  updateStatus();
}

async function showTable(name, family) {
  const t = new TrakTable($("#main"), { reader: state.reader, name, family, onEvent: (e) => showEvent(e) });
  state.view = t;
  mark(name + (family ? "*" : ""));
  await t.load();
  updateStatus();
}

function mark(key) {
  for (const b of document.querySelectorAll("#types [data-key]")) b.classList.toggle("on", b.dataset.key === (key ?? "log"));
}

async function showEvent(e) {
  const body = e.schema.root ? valueTree(e.schema.root, await e.native()) : el("p", {}, "Values unavailable.");
  const meta = el(
    "p",
    { class: "muted" },
    `${e.type} ${e.n} · ${e.timestamp} · byte ${e.offset.toLocaleString()}`,
    e.schema.lineage.length > 1 ? ` · row ${e.familyNumber()} of family ${e.schema.lineage[0]}` : "",
  );
  const prev = await e.prev();
  const d = el(
    "dialog",
    { class: "trak-detail", onclose: () => d.remove() },
    el("form", { method: "dialog", class: "detail-head" }, el("strong", {}, `${e.type} ${e.n}`), el("button", { "aria-label": "Close" }, "✕")),
    el(
      "div",
      { class: "detail-body" },
      meta,
      body,
      el(
        "p",
        { class: "detail-actions" },
        el("button", { onclick: () => { d.close(); showLog(e); } }, "Show in log"),
        prev ? el("button", { onclick: () => { d.close(); showEvent(prev); } }, `‹ ${e.type} ${prev.n}`) : null,
      ),
    ),
  );
  document.body.append(d);
  d.showModal();
}

// --- status, follow, validate -----------------------------------------------------------

function setStatus(text, error = false) {
  const s = $("#status");
  s.textContent = text;
  s.classList.toggle("error", error);
}

function updateStatus() {
  const r = state.reader;
  if (!r) return;
  const torn = r.size > r.end ? ` · ${formatBytes(r.size - r.end)} torn tail ignored` : "";
  setStatus(`trak ${r.version} · ${formatBytes(r.size)} · ${r.types().length} types${torn} · ${r.blocks.fetches} reads`);
}

function stopFollow() {
  clearInterval(state.follow);
  state.follow = null;
  $("#follow").classList.remove("on");
}

async function tick() {
  if (!(await state.reader.refresh())) return updateStatus();
  if (state.view instanceof LogView) await state.view.newer();
  else if (state.view) await state.view.refresh();
  await renderTypes();
}

$("#follow").addEventListener("click", () => {
  if (state.follow) return stopFollow();
  state.follow = setInterval(() => tick().catch((e) => setStatus(e.message, true)), 2000);
  $("#follow").classList.add("on");
});

$("#validate").addEventListener("click", async () => {
  const r = state.reader;
  if (r.size > 200 * 2 ** 20 && !confirm(`Validating reads all ${formatBytes(r.size)}. Continue?`)) return;
  setStatus("Validating every object…");
  try {
    const n = await check(r);
    setStatus(`Valid: ${n.toLocaleString()} objects. ${r.size > r.end ? "A torn tail is ignored." : ""}`);
  } catch (e) {
    setStatus(`Invalid: ${e.message}`, true);
  }
});

// --- opening files ------------------------------------------------------------------------

$("#pick").addEventListener("change", (e) => e.target.files[0] && openFile(e.target.files[0]));
document.addEventListener("dragover", (e) => {
  e.preventDefault();
  document.body.classList.add("dragging");
});
document.addEventListener("dragleave", (e) => {
  if (e.target === document.documentElement || e.clientX === 0) document.body.classList.remove("dragging");
});
document.addEventListener("drop", (e) => {
  e.preventDefault();
  document.body.classList.remove("dragging");
  const f = e.dataTransfer.files[0];
  if (f) openFile(f);
});
$("#home").addEventListener("click", async () => {
  if (!(await browse(""))) intro();
});

async function start() {
  const served = await browse("");
  if (!served) intro();
  const m = /file=([^&]+)/.exec(location.hash);
  if (m && served) await openRemote(decodeURIComponent(m[1]));
}

start();
