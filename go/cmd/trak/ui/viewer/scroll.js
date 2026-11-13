// A scrollbar over a position in [0, count − 1]: the table's rows, the tree's versions
// and the log's appends all scroll with it. Its track maps to the whole range, so
// dragging to the middle asks for the middle and nothing in between.

import { h } from "./util.js";

export class Scroller {
  /** `onChange(position)` is called on every move. `step` is how far one wheel notch moves. */
  constructor({ onChange, label = "position" }) {
    this.count = 0;
    this.pos = 0;
    this.page = 1; // positions visible at once
    this.onChange = onChange;
    this.thumb = h("div.thumb");
    this.track = h("div.track", { role: "scrollbar", "aria-label": label, tabIndex: 0 }, this.thumb);
    this.el = h("div.scroller", {}, this.track);
    this.track.addEventListener("pointerdown", (e) => this.down(e));
    this.track.addEventListener("keydown", (e) => this.key(e));
  }

  /** Let wheel and keys over `el` move the position. */
  attach(el) {
    el.addEventListener(
      "wheel",
      (e) => {
        if (Math.abs(e.deltaY) < Math.abs(e.deltaX)) return;
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
    const moves = { ArrowDown: 1, ArrowUp: -1, PageDown: this.page, PageUp: -this.page, Home: -Infinity, End: Infinity };
    if (!(e.key in moves) || e.target.matches("input, textarea")) return;
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

  draw() {
    const t = this.track.clientHeight || 1;
    const size = this.count <= this.page ? t : Math.max(28, (t * this.page) / this.count);
    this.thumb.style.height = `${size}px`;
    this.thumb.style.transform = `translateY(${this.max ? ((t - size) * this.pos) / this.max : 0}px)`;
    this.el.classList.toggle("idle", this.count <= this.page);
  }

  down(e) {
    e.preventDefault();
    this.track.setPointerCapture(e.pointerId);
    const r = this.track.getBoundingClientRect();
    const size = this.thumb.offsetHeight;
    const thumbTop = r.top + (this.max ? ((r.height - size) * this.pos) / this.max : 0);
    let grab;
    if (e.target === this.thumb) grab = e.clientY - thumbTop;
    else {
      grab = size / 2; // jump the thumb to the click
      this.toY(e.clientY, r, grab, size);
    }
    const move = (ev) => this.toY(ev.clientY, r, grab, size);
    const up = () => {
      this.track.removeEventListener("pointermove", move);
      this.track.removeEventListener("pointerup", up);
      this.track.classList.remove("dragging");
    };
    this.track.classList.add("dragging");
    this.track.addEventListener("pointermove", move);
    this.track.addEventListener("pointerup", up);
  }

  toY(y, r, grab, size) {
    const span = r.height - size;
    this.set(span > 0 ? ((y - grab - r.top) / span) * this.max : 0);
  }
}
