// A scrollbar over a position in [0, count − 1]: the table's rows, the tree's versions
// and the log's appends all scroll with it. Its track maps to the whole range, so
// dragging to the middle asks for the middle and nothing in between. With axis "x" it
// is a horizontal bar over pixels, for the table's columns.

import { h } from "./util.js";

export class Scroller {
  /**
   * `onChange(position)` is called on every move. `label(position)` names a position
   * for the bubble shown while dragging.
   */
  constructor({ onChange, label = "position", axis = "y", name }) {
    this.count = 0;
    this.pos = 0;
    this.page = 1; // positions visible at once
    this.onChange = onChange;
    this.axis = axis;
    this.name = name ?? ((p) => `${p.toLocaleString("en-US")}`);
    this.thumb = h("div.thumb");
    this.bubble = h("div.bubble", { hidden: true });
    this.track = h("div.track", { role: "scrollbar", "aria-label": label, "aria-orientation": axis === "x" ? "horizontal" : "vertical", tabIndex: 0 }, this.thumb);
    this.el = h(`div.scroller.${axis}`, { title: axis === "x" ? "" : `Drag through every ${label}` }, this.track, this.bubble);
    this.track.addEventListener("pointerdown", (e) => this.down(e));
    this.track.addEventListener("keydown", (e) => this.key(e));
  }

  /** Let wheel and keys over `el` move the position. */
  attach(el) {
    el.addEventListener(
      "wheel",
      (e) => {
        if (e.shiftKey || Math.abs(e.deltaY) < Math.abs(e.deltaX)) return;
        e.preventDefault();
        this.acc = (this.acc ?? 0) + e.deltaY / (e.deltaMode === 1 ? 1 : 26);
        const n = Math.trunc(this.acc);
        if (n) {
          this.acc -= n;
          this.set(this.pos + n);
        }
      },
      { passive: false },
    );
    el.addEventListener("keydown", (e) => this.key(e));
  }

  key(e) {
    const moves =
      this.axis === "x"
        ? { ArrowRight: 40, ArrowLeft: -40, Home: -Infinity, End: Infinity }
        : { ArrowDown: 1, ArrowUp: -1, PageDown: this.page, PageUp: -this.page, Home: -Infinity, End: Infinity };
    if (!(e.key in moves) || e.target.matches("input, textarea")) return;
    if (this.axis === "x" && e.target !== this.track) return;
    e.preventDefault();
    this.set(this.pos + moves[e.key]);
  }

  setRange(count, page) {
    this.count = count;
    this.page = Math.max(1, page);
    this.draw();
  }

  get max() {
    return Math.max(0, this.count - this.page);
  }

  set(pos, quiet = false) {
    pos = Math.max(0, Math.min(this.max, Math.round(Number.isFinite(pos) ? pos : pos > 0 ? this.max : 0)));
    const moved = pos !== this.pos;
    this.pos = pos;
    this.draw();
    if (moved && !quiet) this.onChange(pos);
  }

  get length() {
    return (this.axis === "x" ? this.track.clientWidth : this.track.clientHeight) || 1;
  }

  draw() {
    const t = this.length;
    const idle = this.count <= this.page;
    const size = idle ? t : Math.max(32, (t * this.page) / this.count);
    const at = this.max ? ((t - size) * this.pos) / this.max : 0;
    if (this.axis === "x") {
      this.thumb.style.width = `${size}px`;
      this.thumb.style.transform = `translateX(${at}px)`;
    } else {
      this.thumb.style.height = `${size}px`;
      this.thumb.style.transform = `translateY(${at}px)`;
      this.bubble.style.top = `${at + size / 2}px`;
      this.bubble.textContent = this.name(this.pos);
    }
    this.el.classList.toggle("idle", idle);
  }

  down(e) {
    if (e.button !== 0) return;
    e.preventDefault();
    this.track.focus({ preventScroll: true });
    this.track.setPointerCapture(e.pointerId);
    const x = this.axis === "x";
    const coord = (ev) => (x ? ev.clientX : ev.clientY);
    const r = this.track.getBoundingClientRect();
    const start = x ? r.left : r.top;
    const t = x ? r.width : r.height;
    const size = x ? this.thumb.offsetWidth : this.thumb.offsetHeight;
    const thumbStart = start + (this.max ? ((t - size) * this.pos) / this.max : 0);
    const grab = e.target === this.thumb ? coord(e) - thumbStart : size / 2; // else jump the thumb to the click
    const to = (c) => this.set(t - size > 0 ? ((c - grab - start) / (t - size)) * this.max : 0);
    if (e.target !== this.thumb) to(coord(e));
    // Coalesce moves to one per frame: a fast drag asks only for where it is now.
    let pending = null;
    const move = (ev) => {
      if (pending == null) requestAnimationFrame(() => (to(pending), (pending = null)));
      pending = coord(ev);
    };
    const up = () => {
      this.track.removeEventListener("pointermove", move);
      this.track.removeEventListener("pointerup", up);
      this.track.removeEventListener("pointercancel", up);
      this.track.classList.remove("dragging");
      this.bubble.hidden = true;
    };
    this.track.classList.add("dragging");
    this.bubble.hidden = x;
    this.track.addEventListener("pointermove", move);
    this.track.addEventListener("pointerup", up);
    this.track.addEventListener("pointercancel", up);
  }
}
