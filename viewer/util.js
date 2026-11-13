// Small DOM helpers shared by the views.

/** Create an element: h("div.a.b", {title: "x", onclick}, child, …). */
export function h(spec, attrs = {}, ...children) {
  const [tag, ...classes] = spec.split(".");
  const el = document.createElement(tag || "div");
  if (classes.length) el.className = classes.join(" ");
  for (const [k, v] of Object.entries(attrs ?? {})) {
    if (v == null || v === false) continue;
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "style" && typeof v === "object") Object.assign(el.style, v);
    else if (k in el && k !== "list") el[k] = v;
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) if (c != null && c !== false) el.append(c instanceof Node ? c : String(c));
  return el;
}

export const fmtInt = (n) => n.toLocaleString("en-US");

export function fmtBytes(n) {
  if (n < 1000) return `${n} B`;
  const u = ["KB", "MB", "GB", "TB"];
  let i = -1;
  do {
    n /= 1000;
    i++;
  } while (n >= 1000 && i < u.length - 1);
  return `${n.toFixed(n < 10 ? 1 : 0)} ${u[i]}`;
}

/** "20261113T090000.000Z" → "2026-11-13 09:00:00.000". */
export const fmtStamp = (s) => `${s.slice(0, 4)}-${s.slice(4, 6)}-${s.slice(6, 8)} ${s.slice(9, 11)}:${s.slice(11, 13)}:${s.slice(13, 19)}`;

/** The first line of s, cut to max characters, and whether anything was left out. */
export function firstLine(s, max = 160) {
  let i = s.indexOf("\n");
  let cut = i >= 0;
  let line = cut ? s.slice(0, i) : s;
  if (line.length > max) {
    line = line.slice(0, max);
    cut = true;
  }
  return [line, cut];
}

/** Replace a list path's item numbers with *: sessions.12.turns → sessions.*.turns. */
export const pattern = (path) => path.replace(/(^|\.)\d+(?=\.|$)/g, "$1*");

export function store(key, value) {
  try {
    if (value === undefined) return JSON.parse(localStorage.getItem(key) ?? "null");
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    return null;
  }
  return value;
}

let menuEl = null;

/** Show a context menu of [label, fn] items at the mouse. */
export function contextMenu(ev, items) {
  ev.preventDefault();
  ev.stopPropagation();
  closeMenu();
  menuEl = h(
    "div.menu",
    { role: "menu", style: { left: `${ev.clientX}px`, top: `${ev.clientY}px` } },
    items.filter(Boolean).map(([label, fn]) =>
      h("button", { role: "menuitem", onclick: () => (closeMenu(), fn()) }, label),
    ),
  );
  document.body.append(menuEl);
  const r = menuEl.getBoundingClientRect();
  if (r.right > innerWidth) menuEl.style.left = `${innerWidth - r.width - 4}px`;
  if (r.bottom > innerHeight) menuEl.style.top = `${innerHeight - r.height - 4}px`;
  menuEl.querySelector("button")?.focus();
}

export function closeMenu() {
  menuEl?.remove();
  menuEl = null;
}
addEventListener("mousedown", (e) => menuEl && !menuEl.contains(e.target) && closeMenu());
addEventListener("keydown", (e) => e.key === "Escape" && closeMenu());
addEventListener("blur", closeMenu);

/** Show text in a dialog. `load` resolves to the full text. */
export function showText(title, load) {
  const body = h("pre.detail-body", {}, "Loading…");
  const dlg = h("dialog.detail", {}, h("header", {}, h("span", {}, title), h("button", { onclick: () => dlg.close(), title: "Close" }, "×")), body);
  dlg.addEventListener("close", () => dlg.remove());
  document.body.append(dlg);
  dlg.showModal();
  Promise.resolve(load()).then(
    (t) => (body.textContent = t),
    (e) => (body.textContent = String(e)),
  );
}

/** Tell tests and the debug panel that a view finished drawing. */
export function rendered(view, start) {
  const ms = performance.now() - start;
  document.dispatchEvent(new CustomEvent("trak:rendered", { detail: { view, ms } }));
  return ms;
}
