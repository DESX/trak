// Schema definitions: the type tree, flattened fields, and the parser (SPEC §5).

import { SchemaError } from "./errors.js";

const KEY = /[A-Za-z_][A-Za-z0-9_]*/y;
const WORD = /[A-Za-z_][A-Za-z0-9_.]*/y;
const ANNOTATION = /[A-Za-z_][A-Za-z0-9_.-]*/y;
const VERSION = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/;
const TYPE_START = /[|{[_A-Za-z]/;

/**
 * Type tree nodes are plain objects:
 * - leaf:   { kind: "str" | "num" | "enum", words, block, unknown, optional, annotations }
 * - object: { kind: "object", members: [{ key, type, description, line, pos }], ... }
 * - list:   { kind: "list", elem, ... }
 * - map:    { kind: "map", value, ... }
 * `optional` and `annotations` ({ name, text }) are the type's suffixes.
 */
function node(kind, extra) {
  return { kind, optional: false, annotations: [], ...extra };
}

export function isLeaf(t) {
  return t.kind === "str" || t.kind === "num" || t.kind === "enum";
}

/** The parent a type of this name and schema version has, if any (SPEC §5). */
export function parentName(name, version) {
  const m = VERSION.exec(version);
  if (m == null || m[1] !== "1" || m[2] === "0" || !name.includes(".")) return null;
  return name.slice(0, name.lastIndexOf("."));
}

/**
 * Parse a definition into a Schema. Parse failures are kept in `error` and leave `root`
 * null: the type still navigates, but its values are unavailable (SPEC §9).
 * `types` maps the names of schemas defined earlier, for finding a child's parent.
 */
export function buildSchema(name, index, version, text, { strict = false, types = new Map() } = {}) {
  const parent = parentName(name, version);
  let lineage = [name];
  const schema = {
    name, index, version, text, parent, lineage,
    description: null, root: null, fields: [], error: null,
    get annotations() { return this.root ? this.root.annotations : []; },
  };
  try {
    let p = null;
    if (parent != null) {
      p = types.get(parent);
      if (p == null) throw new SchemaError(`parent type '${parent}' must be defined before '${name}'`);
      schema.lineage = lineage = [...p.lineage, name];
    }
    const m = VERSION.exec(version);
    if (m == null || m[1] !== "1") throw new SchemaError(`unsupported schema version ${version}`);
    let [root, description] = new Parser(text, strict).parse();
    if (p != null) {
      if (p.root == null) throw new SchemaError(`parent type '${parent}' can't be parsed`);
      const inherited = new Set(p.root.members.map((x) => x.key));
      for (const mem of root.members) {
        if (inherited.has(mem.key)) {
          const column = mem.pos - text.lastIndexOf("\n", mem.pos - 1);
          throw new SchemaError(`key '${mem.key}' is already defined by '${parent}'`, mem.line, column);
        }
      }
      root = { ...root, members: [...p.root.members, ...root.members] };
    }
    schema.root = root;
    schema.description = description;
    schema.fields = flatten(root);
  } catch (e) {
    if (!(e instanceof SchemaError) || strict) throw e;
    schema.error = e;
  }
  return schema;
}

/** Every leaf column, in schema order. Paths write list indexes as `[]` and map keys as `{}`. */
export function flatten(root) {
  const out = [];
  flattenObject(root, "", false, out);
  return out;
}

function flattenObject(obj, prefix, optional, out) {
  for (const m of obj.members) {
    flattenType(m.type, prefix ? `${prefix}.${m.key}` : m.key, optional, [], m.description, out);
  }
}

function flattenType(t, path, optional, annotations, description, out) {
  optional = optional || t.optional;
  annotations = [...annotations, ...t.annotations];
  if (isLeaf(t)) {
    out.push({
      path, type: t.kind, enum: t.words, unknown: t.unknown, block: t.block,
      optional, annotations, description,
    });
  } else if (t.kind === "object") {
    flattenObject(t, path, optional, out);
  } else if (t.kind === "list") {
    flattenType(t.elem, `${path}.[]`, optional, annotations, description, out);
  } else {
    flattenType(t.value, `${path}.{}`, optional, annotations, description, out);
  }
}

function count(s, c, end) {
  let n = 0;
  for (let i = s.indexOf(c); i >= 0 && i < end; i = s.indexOf(c, i + 1)) n++;
  return n;
}

class Parser {
  constructor(text, strict) {
    this.s = text;
    this.i = 0;
    this.strict = strict;
    this.comments = new Map(); // comment start -> [line, alone on its line, text]
    this.members = [];
  }

  fail(message, pos = this.i) {
    const line = count(this.s, "\n", pos) + 1;
    const column = pos - (this.s.lastIndexOf("\n", pos - 1) + 1) + 1;
    throw new SchemaError(message, line, column);
  }

  peek() {
    return this.i < this.s.length ? this.s[this.i] : "";
  }

  match(re) {
    re.lastIndex = this.i;
    const m = re.exec(this.s);
    return m == null ? null : m[0];
  }

  ws() {
    const s = this.s;
    while (this.i < s.length) {
      const c = s[this.i];
      if (c === " " || c === "\t" || c === "\n") {
        this.i++;
      } else if (s.startsWith("//", this.i)) {
        let end = s.indexOf("\n", this.i);
        if (end < 0) end = s.length;
        if (!this.comments.has(this.i)) {
          const lineStart = s.lastIndexOf("\n", this.i - 1) + 1;
          const alone = s.slice(lineStart, this.i).replace(/[ \t]/g, "") === "";
          const line = count(s, "\n", this.i) + 1;
          this.comments.set(this.i, [line, alone, s.slice(this.i + 2, end).replace(/^[ \t]+|[ \t]+$/g, "")]);
        }
        this.i = end;
      } else {
        return;
      }
    }
  }

  expect(c) {
    this.ws();
    if (this.peek() !== c) this.fail(`expected '${c}'`);
    this.i++;
  }

  parse() {
    for (let i = 0; i < this.s.length; i++) {
      if (this.s.charCodeAt(i) > 127) this.fail("schemas are ASCII", i);
    }
    this.ws();
    if (this.peek() !== "{") this.fail("expected '{' after the type's comment lines");
    const brace = this.i;
    const root = this.parseObject();
    for (;;) {
      this.ws();
      const c = this.peek();
      if (c === "@") root.annotations.push(this.parseAnnotation());
      else if (c === "?") this.fail("an event's top-level type can't be optional");
      else break;
    }
    if (this.i !== this.s.length) this.fail("unexpected text after the definition");
    const comments = [...this.comments.entries()].sort((a, b) => a[0] - b[0]);
    const lines = comments.filter(([pos]) => pos < brace).map(([, [, , text]]) => text);
    const description = lines.join("\n") || null;
    const byLine = new Map();
    for (const m of this.members) {
      const prev = byLine.get(m.line);
      if (prev == null || m.pos > prev.pos) byLine.set(m.line, m);
    }
    for (const [pos, [line, alone, text]] of comments) {
      if (pos > brace && !alone && byLine.has(line)) byLine.get(line).description = text || null;
    }
    return [root, description];
  }

  parseObject() {
    this.expect("{");
    const members = [];
    const keys = new Set();
    for (;;) {
      this.ws();
      if (this.peek() === "}") {
        this.i++;
        return node("object", { members });
      }
      const key = this.match(KEY);
      if (key == null) this.fail("expected a key");
      const pos = this.i;
      if (keys.has(key)) this.fail(`duplicate key '${key}'`);
      keys.add(key);
      this.i += key.length;
      this.expect(":");
      const type = this.parseType();
      const member = { key, type, description: null, line: count(this.s, "\n", pos) + 1, pos };
      members.push(member);
      this.members.push(member);
      this.ws();
      const c = this.peek();
      if (c === ",") this.i++;
      else if (c !== "}") this.fail("expected ',' or '}'");
    }
  }

  parseType() {
    const prefixes = [];
    let base = null;
    for (;;) {
      this.ws();
      const c = this.peek();
      const pos = this.i;
      if (c === "|") {
        prefixes.push(["|", pos]);
        this.i++;
      } else if (c === "[") {
        this.i++;
        this.expect("]");
        prefixes.push(["[]", pos]);
      } else if (c === "{") {
        this.i++;
        this.ws();
        if (this.peek() !== "}") {
          this.i = pos;
          break;
        }
        this.i++;
        const after = this.i;
        this.ws();
        if (TYPE_START.test(this.peek())) {
          prefixes.push(["{}", pos]);
        } else {
          this.i = after;
          base = node("object", { members: [] });
          break;
        }
      } else {
        break;
      }
    }
    let t = base ?? this.parseBase();
    for (const [p, pos] of prefixes.reverse()) {
      if (p === "|") {
        if (!isLeaf(t) || t.block) this.fail("'|' applies only to a leaf", pos);
        t.block = true;
      } else if (p === "[]") {
        t = node("list", { elem: t });
      } else {
        t = node("map", { value: t });
      }
    }
    for (;;) {
      this.ws();
      const c = this.peek();
      if (c === "?") {
        if (t.optional) this.fail("repeated '?'");
        t.optional = true;
        this.i++;
      } else if (c === "@") {
        t.annotations.push(this.parseAnnotation());
      } else {
        return t;
      }
    }
  }

  parseAnnotation() {
    this.i++;
    const name = this.match(ANNOTATION);
    if (name == null) this.fail("expected an annotation name");
    this.i += name.length;
    return { name, text: this.peek() === "(" ? this.parseParens() : null };
  }

  parseParens() {
    const start = this.i + 1;
    const end = this.s.indexOf(")", start);
    if (end < 0 || this.s.slice(start, end).includes("\n")) {
      this.fail("unclosed '(' (text can't contain ')' or a line break)");
    }
    this.i = end + 1;
    return this.s.slice(start, end);
  }

  parseBase() {
    if (this.peek() === "{") return this.parseObject();
    const start = this.i;
    const word = this.match(WORD);
    if (word == null) this.fail("expected a type");
    this.i += word.length;
    const leaf = (kind, extra = {}) => node(kind, { words: [], block: false, unknown: null, ...extra });
    if (word === "str" || word === "num") {
      if (this.peek() === "(") this.fail(`${word} takes no parameters`);
      return leaf(word);
    }
    if (word === "enum") {
      if (this.peek() !== "(") this.fail("expected '(' after enum");
      this.i++;
      const words = [];
      for (;;) {
        this.ws();
        if (this.peek() === ")") {
          this.i++;
          break;
        }
        const w = this.match(KEY);
        if (w == null) this.fail("expected an enum word");
        if (words.includes(w)) this.fail(`duplicate enum word '${w}'`);
        words.push(w);
        this.i += w.length;
        this.ws();
        const c = this.peek();
        if (c === ",") this.i++;
        else if (c !== ")") this.fail("expected ',' or ')'");
      }
      if (words.length === 0) this.fail("enum needs at least one word", start);
      return leaf("enum", { words });
    }
    if (this.peek() === "(") this.parseParens();
    if (this.strict) this.fail(`unknown type '${word}'`, start);
    return leaf("str", { unknown: this.s.slice(start, this.i) });
  }
}
