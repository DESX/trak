// The viewer: a pane of open files on the left, and tabs of views on the right. Every
// view has a URL: #f=FILE&v=SPEC, where SPEC is tree:PATH@k, items:LIST@n, table:LIST@row
// or log:@version. The viewer knows nothing about any schema; it reads it from the file.

import { BlobSource, ServeSource, TrakFile } from "../js/src/index.js";
import { LogView } from "./log.js";
import { TableView } from "./table.js";
import { TreeView } from "./tree.js";
import { contextMenu, fmtBytes, fmtInt, h, showText, store } from "./util.js";

const API = "api";

class App {
  constructor() {
    this.files = (store("trak.files") ?? []).filter((p) => typeof p === "string");
    this.open = new Map(); // key → { key, name, file, tabs, active, local }
    this.current = null;
    this.requests = [];
    this.reqCount = 0;
    this.reqBytes = 0;
    this.following = false;
    this.fileList = document.getElementById("file-list");
    this.tabsEl = document.getElementById("tabs");
    this.views = document.getElementById("views");
    this.status = document.getElementById("status");
    this.debug = document.getElementById("debug");
    document.getElementById("add").addEventListener("click", () => this.pick(""));
    document.getElementById("debug-toggle").addEventListener("click", () => this.toggleDebug());
    document.getElementById("debug-clear").addEventListener("click", () => ((this.requests = []), this.drawDebug()));
    document.getElementById("follow").addEventListener("click", () => this.toggleFollow());
    document.getElementById("schema").addEventListener("click", () => this.current && showText("Schema", () => this.current.file.schema.text));
    addEventListener("hashchange", () => this.route());
    this.drawFiles();
    this.route();
  }

  // Files.

  drawFiles() {
    const items = this.files.map((p) =>
      h(
        "li",
        { class: this.current?.key === p ? "active" : "" },
        h("button.file", { onclick: () => this.select(p), title: p }, h("span.name", {}, p.split("/").pop()), h("span.dir", {}, p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "")),
        h("button.close", { title: "Remove from the list", onclick: () => this.remove(p) }, "×"),
      ),
    );
    for (const o of this.open.values()) {
      if (o.local)
        items.push(h("li", { class: this.current === o ? "active" : "" }, h("button.file", { onclick: () => this.activate(o) }, h("span.name", {}, o.name), h("span.dir", {}, "local file"))));
    }
    if (!items.length) items.push(h("li.empty", {}, "No files yet. Press + to open one."));
    this.fileList.replaceChildren(...items);
  }

  remove(p) {
    this.files = this.files.filter((x) => x !== p);
    store("trak.files", this.files);
    const o = this.open.get(p);
    if (o) {
      o.tabs.forEach((t) => (t.view.destroy(), t.view.el.remove()));
      this.open.delete(p);
      if (this.current === o) {
        this.current = null;
        this.tabsEl.replaceChildren();
        location.hash = "";
      }
    }
    this.drawFiles();
  }

  /** The file picker: folders and .trak files under the served directory. */
  async pick(dir) {
    let dlg = document.getElementById("picker");
    const list = dlg.querySelector(".picker-list");
    const where = dlg.querySelector(".picker-where");
    if (!dlg.open) dlg.showModal();
    list.replaceChildren(h("li.empty", {}, "Loading…"));
    try {
      const res = await fetch(`${API}/files?dir=${encodeURIComponent(dir)}`, { cache: "no-store" });
      if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
      const l = await res.json();
      where.textContent = `${l.root}/${dir}`;
      const join = (n) => (dir ? `${dir}/${n}` : n);
      const items = [];
      if (dir) items.push(h("li", {}, h("button.dir", { onclick: () => this.pick(dir.includes("/") ? dir.slice(0, dir.lastIndexOf("/")) : "") }, "↑ ..")));
      for (const d of l.dirs) items.push(h("li", {}, h("button.dir", { onclick: () => this.pick(join(d)) }, `▸ ${d}/`)));
      for (const f of l.files)
        items.push(
          h("li", {}, h("button.file", { onclick: () => (dlg.close(), this.add(join(f.name))) }, h("span", {}, f.name), h("span.dim", {}, `${fmtBytes(f.size)} · ${f.modified.replace("T", " ").replace("Z", "")}`))),
        );
      if (!l.dirs.length && !l.files.length) items.push(h("li.empty", {}, "No .trak files here."));
      list.replaceChildren(...items);
    } catch (e) {
      list.replaceChildren(h("li.empty", {}, `Can't list files: ${e.message}`));
    }
  }

  add(path) {
    if (!this.files.includes(path)) {
      this.files.push(path);
      store("trak.files", this.files);
    }
    this.select(path);
  }

  async openLocal(blob) {
    const key = `local:${blob.name}:${blob.size}`;
    let o = this.open.get(key);
    if (!o) {
      o = await this.load(key, blob.name, () => new BlobSource(blob));
      o.local = true;
    }
    this.activate(o);
  }

  select(path) {
    location.hash = `f=${encodeURIComponent(path)}`;
  }

  async load(key, name, source) {
    const t0 = performance.now();
    this.setStatus(`Opening ${name}…`);
    const file = await TrakFile.open(await source(), { log: (r) => this.logRequest(r) });
    const o = { key, name, file, tabs: [], active: null, openMs: performance.now() - t0 };
    this.open.set(key, o);
    return o;
  }

  async fileFor(path) {
    let o = this.open.get(path);
    if (!o) {
      try {
        o = await this.load(path, path.split("/").pop(), () => ServeSource.open(`${API}/read`, path));
      } catch (e) {
        this.setStatus(`Can't open ${path}: ${e.message}`, true);
        throw e;
      }
      if (!this.files.includes(path)) {
        this.files.push(path);
        store("trak.files", this.files);
      }
    }
    return o;
  }

  activate(o) {
    if (this.current && this.current !== o) this.current.tabs.forEach((t) => t.view.hide());
    this.current = o;
    this.drawFiles();
    this.drawTabs();
    this.drawStatus();
    if (!o.tabs.length) this.navigate(`tree:@${o.file.version - 1}`, false);
    else this.showTab(o.active ?? o.tabs[0]);
  }

  // Routing.

  async route() {
    const q = new URLSearchParams(location.hash.slice(1));
    const f = q.get("f");
    if (!f) {
      if (!this.current) this.setStatus(this.files.length ? "Pick a file on the left." : "Press + to open a trak file.");
      return;
    }
    let o;
    try {
      o = await this.fileFor(f);
    } catch {
      return;
    }
    if (this.current !== o) {
      if (this.current) this.current.tabs.forEach((t) => t.view.hide());
      this.current = o;
      this.drawFiles();
      this.drawStatus();
    }
    const spec = q.get("v") ?? `tree:@${o.file.version - 1}`;
    await this.openSpec(spec);
  }

  /** Open a view: tree:PATH@k, items:LIST@n, table:LIST@row, log:@v. */
  navigate(spec, push = true) {
    const key = this.current?.key;
    if (!key) return;
    const hash = `f=${encodeURIComponent(key)}&v=${encodeURIComponent(spec)}`;
    if (this.current.local) return this.openSpec(spec);
    if (push) location.hash = hash;
    else {
      history.replaceState(null, "", `#${hash}`);
      this.route();
    }
  }

  /** Keep the URL in step with a view's position, without adding history. */
  updateSpec(view) {
    const o = this.current;
    if (!o || o.local || o.active?.view !== view) return;
    history.replaceState(null, "", `#f=${encodeURIComponent(o.key)}&v=${encodeURIComponent(view.spec)}`);
  }

  async openSpec(spec) {
    const o = this.current;
    const m = /^(tree|items|table|log):([^@]*)(?:@(-?\d+))?$/.exec(spec);
    if (!m) return this.setStatus(`Bad view ${spec}`, true);
    const [, kind, path, at] = m;
    const pos = at == null ? null : Number(at);
    const id = `${kind}:${path}`;
    let tab = o.tabs.find((t) => t.id === id);
    try {
      if (!tab) {
        const view = kind === "table" ? new TableView(this, o.file, path) : kind === "log" ? new LogView(this, o.file) : new TreeView(this, o.file, kind, path);
        tab = { id, view };
        o.tabs.push(tab);
        await view.init(pos ?? 0);
        this.views.append(view.el);
        this.showTab(tab);
        await view.start(pos ?? (kind === "log" ? o.file.version : kind === "table" ? 0 : null));
      } else {
        this.showTab(tab);
        if (pos != null) await tab.view.setPos(pos);
      }
    } catch (e) {
      console.error(e);
      if (tab && !tab.view.el.isConnected) o.tabs.splice(o.tabs.indexOf(tab), 1);
      this.setStatus(String(e.message ?? e), true);
      this.drawTabs();
    }
  }

  showTab(tab) {
    const o = this.current;
    if (o.active && o.active !== tab) o.active.view.hide();
    o.active = tab;
    tab.view.show();
    this.drawTabs();
  }

  drawTabs() {
    const o = this.current;
    if (!o) return this.tabsEl.replaceChildren();
    this.tabsEl.replaceChildren(
      ...o.tabs.map((t, i) =>
        h(
          `div.tab${t === o.active ? ".active" : ""}`,
          { role: "tab", "aria-selected": t === o.active },
          h("button.tab-label", { onclick: () => this.navigate(t.view.spec) }, t.view.title),
          i > 0 ? h("button.tab-close", { title: "Close tab", onclick: () => this.closeTab(t) }, "×") : null,
        ),
      ),
    );
  }

  closeTab(t) {
    const o = this.current;
    const i = o.tabs.indexOf(t);
    o.tabs.splice(i, 1);
    t.view.destroy();
    t.view.el.remove();
    if (o.active === t) this.navigate(o.tabs[Math.max(0, i - 1)].view.spec, false);
    else this.drawTabs();
  }

  // Paths.

  /** What a path names in the schema: "object", "list" or "leaf". */
  kindOf(path) {
    let typ = this.current.file.schema.root;
    let item = null;
    let kind = "object";
    for (const seg of path ? path.split(".") : []) {
      if (item) {
        if (!/^-?\d+$/.test(seg)) return null;
        kind = item.kind === "object" ? "object" : "leaf";
        typ = item.type;
        item = null;
        continue;
      }
      const f = typ?.field(seg);
      if (!f) return null;
      if (f.kind === "event") {
        item = f.item;
        kind = "list";
      } else if (f.kind === "object") {
        typ = f.type;
        kind = "object";
      } else kind = "leaf";
    }
    return kind;
  }

  /** Follow a stored path: open the item it names, in its list's tree view. */
  openPath(path) {
    const segs = path.split(".");
    while (segs.length && !/^\d+$/.test(segs[segs.length - 1])) segs.pop();
    if (segs.length < 2 || this.kindOf(segs.slice(0, -1).join(".")) !== "list") return this.navigate(`tree:@${this.current.file.version - 1}`);
    this.navigate(`items:${segs.slice(0, -1).join(".")}@${segs[segs.length - 1]}`);
  }

  listMenu(e, path) {
    contextMenu(e, [
      ["Table view", () => this.navigate(`table:${path}@0`)],
      ["Tree view", () => this.navigate(`items:${path}`)],
    ]);
  }

  /** Breadcrumbs for a path: the root, then each segment, each a link. */
  crumbs(path, kind) {
    const root = this.current.file.schema.root.name;
    const segs = path ? path.split(".") : [];
    const parts = [h("a", { href: "#", onclick: (e) => (e.preventDefault(), this.navigate(`tree:@${this.current.file.version - 1}`)) }, root)];
    segs.forEach((s, i) => {
      const p = segs.slice(0, i + 1).join(".");
      const k = this.kindOf(p);
      parts.push(h("span.sep", {}, " › "));
      if (i === segs.length - 1) parts.push(h("b", {}, s));
      else if (k === "list") parts.push(h("a", { href: "#", onclick: (e) => (e.preventDefault(), this.navigate(`table:${p}@0`)), oncontextmenu: (e) => this.listMenu(e, p) }, s));
      else parts.push(h("a", { href: "#", onclick: (e) => (e.preventDefault(), this.navigate(`tree:${p}`)) }, s));
    });
    return h("span.crumbs", { title: kind }, parts);
  }

  // Status, follow and the request log.

  setStatus(text, error = false) {
    this.status.querySelector(".info").textContent = text;
    this.status.classList.toggle("error", error);
  }

  drawStatus() {
    const o = this.current;
    if (!o) return;
    const f = o.file;
    const torn = f.blocks.size - f.size;
    this.setStatus(`${f.magic} · ${fmtBytes(f.size)} · ${fmtInt(f.version)} versions · opened in ${o.openMs.toFixed(0)} ms${torn > 0 ? ` · ${fmtInt(torn)} bytes of torn tail ignored` : ""}`);
  }

  logRequest(r) {
    this.reqCount++;
    this.reqBytes += r.bytes;
    this.requests.push({ n: this.reqCount, at: performance.now(), ...r });
    if (this.requests.length > 500) this.requests.splice(0, this.requests.length - 500);
    document.getElementById("debug-toggle").textContent = `${fmtInt(this.reqCount)} requests · ${fmtBytes(this.reqBytes)}`;
    if (!this.debug.hidden) this.drawDebug();
  }

  toggleDebug() {
    this.debug.hidden = !this.debug.hidden;
    if (!this.debug.hidden) this.drawDebug();
  }

  drawDebug() {
    const rows = this.requests
      .slice(-200)
      .reverse()
      .map((r) =>
        h(
          "tr",
          {},
          h("td", {}, fmtInt(r.n)),
          h("td", {}, fmtInt(r.ranges.length)),
          h("td", {}, fmtBytes(r.bytes)),
          h("td", {}, `${r.ms.toFixed(1)} ms`),
          h("td.ranges", {}, r.ranges.map(([o, n]) => `${fmtInt(o)}+${fmtInt(n)}`).join("  ")),
        ),
      );
    this.debug.querySelector("tbody").replaceChildren(...rows);
  }

  toggleFollow() {
    this.following = !this.following;
    document.getElementById("follow").classList.toggle("on", this.following);
    clearInterval(this.timer);
    if (this.following) this.timer = setInterval(() => this.poll(), 2000);
  }

  async poll() {
    const o = this.current;
    if (!o || this.polling) return;
    this.polling = true;
    try {
      if (await o.file.refresh()) {
        this.drawStatus();
        await Promise.all(o.tabs.map((t) => t.view.refresh?.()));
      } else this.drawStatus();
    } catch (e) {
      this.setStatus(String(e.message ?? e), true);
    } finally {
      this.polling = false;
    }
  }
}

const app = new App();
window.trakApp = app;

document.getElementById("picker-close").addEventListener("click", () => document.getElementById("picker").close());
document.getElementById("pick-local").addEventListener("change", (e) => {
  const f = e.target.files?.[0];
  if (f) {
    document.getElementById("picker").close();
    app.openLocal(f);
  }
  e.target.value = "";
});
