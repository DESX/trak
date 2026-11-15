// The schema language (SPEC §2). A port of go/schema.go.

import { SchemaError } from "./errors.js";

export const Kind = Object.freeze({ str: "str", num: "num", enum: "enum", path: "path", object: "object", event: "event" });

const BUILTIN = new Set(["str", "num", "enum", "path", "event", "object", "root"]);
const isNameStart = (c) => c === "_" || (c >= "A" && c <= "Z") || (c >= "a" && c <= "z");
const isNameChar = (c) => isNameStart(c) || (c >= "0" && c <= "9");

export function isName(s) {
  if (!s || !isNameStart(s[0])) return false;
  for (let i = 1; i < s.length; i++) if (!isNameChar(s[i])) return false;
  return true;
}

/**
 * A field: { name, desc, kind, block, optional, words, type, item }.
 * `type` is the object type for objects and object event lists; `item` is an event list's item.
 */
export class Field {
  constructor(name, desc = "") {
    this.name = name;
    this.desc = desc;
    this.kind = null;
    this.block = false;
    this.optional = false;
    this.words = [];
    this.type = null;
    this.item = null;
  }
  get isLeaf() {
    return this.kind !== Kind.object && this.kind !== Kind.event;
  }
}

/** An object type. Inline objects have an empty name. */
export class Type {
  constructor(name = "", desc = "") {
    this.name = name;
    this.desc = desc;
    this.fields = [];
    /** Event fields and nested objects that contain event lists, in schema order. */
    this.slots = [];
    this.events = false;
  }
  field(name) {
    return this.fields.find((f) => f.name === name) ?? null;
  }
  slot(name) {
    return this.slots.findIndex((f) => f.name === name);
  }
}

export class Schema {
  constructor(text) {
    this.text = text;
    /** The file's description: a comment block at the start, then a blank line. */
    this.desc = "";
    this.root = null;
    this.types = new Map();
    this.order = [];
  }
}

function lex(src) {
  const toks = [];
  let line = 1;
  let col = 1;
  let lineStart = true;
  let i = 0;
  while (i < src.length) {
    const c = src[i];
    const t = { kind: "", text: "", line, col, lineStart };
    if (c === "\n") {
      i++;
      line++;
      col = 1;
      lineStart = true;
      continue;
    }
    if (c === " " || c === "\t" || c === "\r") {
      i++;
      col++;
      continue;
    }
    if (c === "#") {
      let end = src.indexOf("\n", i);
      if (end < 0) end = src.length;
      t.kind = "c";
      t.text = src.slice(i + 1, end).trim();
      col += end - i;
      i = end;
    } else if (isNameStart(c)) {
      let j = i + 1;
      while (j < src.length && isNameChar(src[j])) j++;
      t.kind = "n";
      t.text = src.slice(i, j);
      col += j - i;
      i = j;
    } else if ("{}(),:?|".includes(c)) {
      t.kind = "p";
      t.text = c;
      i++;
      col++;
    } else {
      throw new SchemaError(`unexpected character ${JSON.stringify(c)}`, line, col);
    }
    toks.push(t);
    lineStart = false;
  }
  toks.push({ kind: "e", text: "end of schema", line, col, lineStart: true });
  return toks;
}

/** Parse schema text. Throws SchemaError. */
export function parseSchema(text) {
  const toks = lex(text);
  let p = 0;
  const peek = () => toks[p];
  const next = () => (toks[p].kind === "e" ? toks[p] : toks[p++]);
  const err = (t, msg) => new SchemaError(msg, t.line, t.col);
  const comments = () => {
    const parts = [];
    while (peek().kind === "c") {
      const t = next();
      if (t.text) parts.push(t.text);
    }
    return parts.join(" ");
  };
  const trailing = () => {
    const t = peek();
    if (t.kind === "c" && !t.lineStart) {
      next();
      return t.text;
    }
    return "";
  };
  const expect = (s) => {
    const t = next();
    if (t.text !== s || t.kind === "c" || t.kind === "e") throw err(t, `expected "${s}", found "${t.text}"`);
    return t;
  };
  const name = (what) => {
    const t = next();
    if (t.kind !== "n") throw err(t, `expected ${what}, found "${t.text}"`);
    return t;
  };
  const s = new Schema(text);
  const refs = [];
  // Comment lines at the start describe the file if a blank line follows them.
  let j = 0;
  while (toks[j].kind === "c" && (j === 0 || toks[j].line === toks[j - 1].line + 1)) j++;
  if (j > 0 && toks[j].line > toks[j - 1].line + 1) {
    s.desc = toks.slice(0, j).map((t) => t.text).filter(Boolean).join(" ");
    p = j;
  }

  function typeExpr(f, inEvent) {
    const t = next();
    if (t.kind === "p" && t.text === "|") {
      const n = next();
      if (n.text !== "str") throw err(n, "expected str after |");
      f.kind = Kind.str;
      f.block = true;
    } else if (t.kind === "p" && t.text === "{") {
      const inline = new Type();
      const c = trailing();
      if (c && !f.desc) f.desc = c;
      fields(inline);
      f.kind = Kind.object;
      f.type = inline;
    } else if (t.kind === "n" && t.text === "event") {
      if (inEvent) throw err(t, "an event list can't hold event lists directly");
      const item = new Field(f.name);
      typeExpr(item, true);
      if (item.optional) throw err(t, "event list items can't be optional");
      f.kind = Kind.event;
      f.item = item;
      return;
    } else if (t.kind === "n" && (t.text === "str" || t.text === "num" || t.text === "path")) {
      f.kind = t.text;
    } else if (t.kind === "n" && t.text === "enum") {
      expect("(");
      for (;;) {
        const w = name("an enum word");
        if (f.words.includes(w.text)) throw err(w, `enum word ${w.text} is repeated`);
        f.words.push(w.text);
        const sep = next();
        if (sep.text === ")") break;
        if (sep.text !== ",") throw err(sep, `expected , or ), found "${sep.text}"`);
        if (peek().text === ")") {
          next();
          break;
        }
      }
      f.kind = Kind.enum;
    } else if (t.kind === "n" && !BUILTIN.has(t.text)) {
      f.kind = Kind.object;
      refs.push({ f, tok: t });
    } else {
      throw err(t, `expected a type, found "${t.text}"`);
    }
    if (peek().kind === "p" && peek().text === "?") {
      next();
      f.optional = true;
    }
  }

  function fields(typ) {
    for (;;) {
      const desc = comments();
      if (peek().kind === "p" && peek().text === "}") {
        next();
        return;
      }
      const nt = name("a field name or }");
      if (typ.field(nt.text)) throw err(nt, `field ${nt.text} is defined twice`);
      expect(":");
      const f = new Field(nt.text, desc);
      typ.fields.push(f);
      typeExpr(f, false);
      const t = peek();
      if (t.kind === "p" && t.text === ",") next();
      else if (!(t.kind === "p" && t.text === "}") && t.kind !== "c") throw err(t, `expected , or }, found "${t.text}"`);
      const c = trailing();
      if (c && !f.desc) f.desc = c;
    }
  }

  for (;;) {
    const desc = comments();
    const t = next();
    if (t.kind === "e") break;
    if (t.kind !== "n" || (t.text !== "object" && t.text !== "root")) throw err(t, `expected object or root, found "${t.text}"`);
    const nt = name("a type name");
    if (s.types.has(nt.text) || BUILTIN.has(nt.text)) throw err(nt, `type ${nt.text} is already defined`);
    const typ = new Type(nt.text, desc);
    if (t.text === "root") {
      if (s.root) throw err(t, "a schema has exactly one root");
      s.root = typ;
    }
    s.types.set(nt.text, typ);
    s.order.push(typ);
    expect("{");
    if (!typ.desc) typ.desc = trailing();
    fields(typ);
  }
  if (!s.root) throw new SchemaError("the schema has no root");
  for (const r of refs) {
    const typ = s.types.get(r.tok.text);
    if (!typ) throw err(r.tok, `unknown type ${r.tok.text}`);
    if (typ === s.root) throw err(r.tok, `the root type ${r.tok.text} can't be used as a field`);
    r.f.type = typ;
  }
  const link = (typ) => {
    for (const f of typ.fields) {
      if (f.kind === Kind.event && f.item.kind === Kind.object) f.type = f.item.type;
      if (f.type && !f.type.name) link(f.type);
    }
  };
  s.order.forEach(link);
  const cycles = (typ, stack) => {
    if (stack.includes(typ)) throw new SchemaError(`type ${typ.name} contains itself`);
    for (const f of typ.fields) if (f.type) cycles(f.type, [...stack, typ]);
  };
  s.order.forEach((typ) => cycles(typ, []));
  const done = new Set();
  const derive = (typ) => {
    if (done.has(typ)) return;
    done.add(typ);
    for (const f of typ.fields) {
      if (f.type) derive(f.type);
      if (f.kind === Kind.event) typ.slots.push(f);
      else if (f.kind === Kind.object && f.type.events) {
        if (f.optional) throw new SchemaError(`optional field ${f.name} can't contain event lists`);
        typ.slots.push(f);
      }
    }
    if (typ.slots.length > 64) throw new SchemaError("an object can have at most 64 event lists and nested objects with event lists");
    typ.events = typ.slots.length > 0;
  };
  s.order.forEach(derive);
  return s;
}
