package trak

import (
	"fmt"
	"strings"
)

// Kind is what a field holds (SPEC §1).
type Kind uint8

const (
	KindStr    Kind = iota // str, or |str when Field.Block is set
	KindNum                // num
	KindEnum               // enum(a, b, …)
	KindPath               // path
	KindObject             // a nested object, named or inline
	KindEvent              // event T
)

func (k Kind) String() string {
	return [...]string{"str", "num", "enum", "path", "object", "event"}[k]
}

// Field is one member of an object type.
type Field struct {
	Name     string
	Desc     string
	Kind     Kind
	Block    bool     // |str
	Optional bool     // T?
	Words    []string // enum words
	Type     *Type    // Object: its type; Event of objects: the item type
	Item     *Field   // Event: the item, named like the list
}

// IsLeaf reports whether the field holds a single value.
func (f *Field) IsLeaf() bool { return f.Kind < KindObject }

// Type is an object type. Inline objects have an empty Name.
type Type struct {
	Name   string
	Desc   string
	Fields []*Field

	slots  []*Field // event fields and nested objects that contain event lists
	events bool     // the type contains an event list, directly or in a nested object
}

// Field returns the named field, or nil.
func (t *Type) Field(name string) *Field {
	for _, f := range t.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// Slots returns the fields that have a slot in this type's object node: event
// fields and nested objects that contain event lists, in schema order.
func (t *Type) Slots() []*Field { return t.slots }

func (t *Type) slot(name string) int {
	for i, f := range t.slots {
		if f.Name == name {
			return i
		}
	}
	return -1
}

// HasEvents reports whether objects of this type have an object node.
func (t *Type) HasEvents() bool { return t.events }

// Schema is a parsed schema (SPEC §2).
type Schema struct {
	Text  string
	Root  *Type
	Types map[string]*Type // named object types, the root included
	Order []*Type          // named types in definition order
}

// ParseSchema parses schema text.
func ParseSchema(text string) (*Schema, error) {
	p := &schemaParser{src: text, line: 1, col: 1}
	s, err := p.parse()
	if err != nil {
		return nil, err
	}
	s.Text = text
	return s, nil
}

type token struct {
	kind      byte // 'n' name, 'p' punctuation, 'c' comment, 'e' end
	text      string
	line, col int
	lineStart bool // the first token on its line
}

type schemaParser struct {
	src       string
	pos       int
	line, col int
	toks      []token
	i         int
}

func (p *schemaParser) errAt(t token, format string, args ...any) error {
	return &SchemaError{Msg: fmt.Sprintf(format, args...), Line: t.line, Column: t.col}
}

func (p *schemaParser) lex() error {
	lineStart := true
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		start := token{line: p.line, col: p.col, lineStart: lineStart}
		switch {
		case c == '\n':
			p.pos++
			p.line++
			p.col = 1
			lineStart = true
			continue
		case c == ' ' || c == '\t' || c == '\r':
			p.pos++
			p.col++
			continue
		case c == '#':
			end := strings.IndexByte(p.src[p.pos:], '\n')
			if end < 0 {
				end = len(p.src) - p.pos
			}
			start.kind, start.text = 'c', strings.TrimSpace(p.src[p.pos+1:p.pos+end])
			p.pos += end
			p.col += end
		case isNameStart(c):
			j := p.pos + 1
			for j < len(p.src) && isNameChar(p.src[j]) {
				j++
			}
			start.kind, start.text = 'n', p.src[p.pos:j]
			p.col += j - p.pos
			p.pos = j
		case strings.IndexByte("{}(),:?|", c) >= 0:
			start.kind, start.text = 'p', string(c)
			p.pos++
			p.col++
		default:
			return &SchemaError{Msg: fmt.Sprintf("unexpected character %q", c), Line: p.line, Column: p.col}
		}
		p.toks = append(p.toks, start)
		lineStart = false
	}
	p.toks = append(p.toks, token{kind: 'e', text: "end of schema", line: p.line, col: p.col, lineStart: true})
	return nil
}

func isNameStart(c byte) bool { return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }
func isNameChar(c byte) bool  { return isNameStart(c) || c >= '0' && c <= '9' }

// IsName reports whether s matches [A-Za-z_][A-Za-z0-9_]*.
func IsName(s string) bool {
	if s == "" || !isNameStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isNameChar(s[i]) {
			return false
		}
	}
	return true
}

func (p *schemaParser) peek() token { return p.toks[p.i] }

func (p *schemaParser) next() token {
	t := p.toks[p.i]
	if t.kind != 'e' {
		p.i++
	}
	return t
}

// comments collects the comment lines at the cursor, joined by spaces.
func (p *schemaParser) comments() string {
	var parts []string
	for p.peek().kind == 'c' {
		if t := p.next(); t.text != "" {
			parts = append(parts, t.text)
		}
	}
	return strings.Join(parts, " ")
}

// trailing returns a comment on the same line as the previous token.
func (p *schemaParser) trailing() string {
	if t := p.peek(); t.kind == 'c' && !t.lineStart {
		p.next()
		return t.text
	}
	return ""
}

func (p *schemaParser) expect(text string) (token, error) {
	t := p.next()
	if t.text != text || t.kind == 'c' || t.kind == 'e' && text != "end of schema" {
		return t, p.errAt(t, "expected %q, found %q", text, t.text)
	}
	return t, nil
}

func (p *schemaParser) name(what string) (token, error) {
	t := p.next()
	if t.kind != 'n' {
		return t, p.errAt(t, "expected %s, found %q", what, t.text)
	}
	return t, nil
}

// pending is a reference to a named type, resolved after parsing.
type pending struct {
	f   *Field
	tok token
}

func (p *schemaParser) parse() (*Schema, error) {
	if err := p.lex(); err != nil {
		return nil, err
	}
	s := &Schema{Types: map[string]*Type{}}
	var refs []pending
	for {
		desc := p.comments()
		t := p.next()
		if t.kind == 'e' {
			break
		}
		if t.text != "object" && t.text != "root" || t.kind != 'n' {
			return nil, p.errAt(t, "expected object or root, found %q", t.text)
		}
		nt, err := p.name("a type name")
		if err != nil {
			return nil, err
		}
		if s.Types[nt.text] != nil || isBuiltin(nt.text) {
			return nil, p.errAt(nt, "type %s is already defined", nt.text)
		}
		typ := &Type{Name: nt.text, Desc: desc}
		if t.text == "root" {
			if s.Root != nil {
				return nil, p.errAt(t, "a schema has exactly one root")
			}
			s.Root = typ
		}
		s.Types[nt.text] = typ
		s.Order = append(s.Order, typ)
		if _, err := p.expect("{"); err != nil {
			return nil, err
		}
		if typ.Desc == "" {
			typ.Desc = p.trailing()
		}
		if err := p.fields(typ, &refs); err != nil {
			return nil, err
		}
	}
	if s.Root == nil {
		return nil, &SchemaError{Msg: "the schema has no root"}
	}
	for _, r := range refs {
		typ := s.Types[r.tok.text]
		if typ == nil {
			return nil, p.errAt(r.tok, "unknown type %s", r.tok.text)
		}
		if typ == s.Root {
			return nil, p.errAt(r.tok, "the root type %s can't be used as a field", r.tok.text)
		}
		r.f.Type = typ
	}
	for _, typ := range s.Order {
		linkItems(typ)
	}
	for _, typ := range s.Order {
		if err := checkCycles(typ, nil); err != nil {
			return nil, err
		}
	}
	done := map[*Type]bool{}
	for _, typ := range s.Order {
		if err := derive(typ, done); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func isBuiltin(name string) bool {
	switch name {
	case "str", "num", "enum", "path", "event", "object", "root":
		return true
	}
	return false
}

// fields parses members up to the closing brace.
func (p *schemaParser) fields(typ *Type, refs *[]pending) error {
	for {
		desc := p.comments()
		if p.peek().text == "}" && p.peek().kind == 'p' {
			p.next()
			return nil
		}
		nt, err := p.name("a field name or }")
		if err != nil {
			return err
		}
		if typ.Field(nt.text) != nil {
			return p.errAt(nt, "field %s is defined twice", nt.text)
		}
		if _, err := p.expect(":"); err != nil {
			return err
		}
		f := &Field{Name: nt.text, Desc: desc}
		typ.Fields = append(typ.Fields, f)
		if err := p.typeExpr(f, refs, false); err != nil {
			return err
		}
		if t := p.peek(); t.kind == 'p' && t.text == "," {
			p.next()
		} else if !(t.kind == 'p' && t.text == "}") && !(t.kind == 'c') {
			return p.errAt(t, "expected , or }, found %q", t.text)
		}
		if c := p.trailing(); c != "" && f.Desc == "" {
			f.Desc = c
		}
	}
}

// typeExpr parses a field's type into f.
func (p *schemaParser) typeExpr(f *Field, refs *[]pending, inEvent bool) error {
	t := p.next()
	switch {
	case t.kind == 'p' && t.text == "|":
		if n, err := p.name("str"); err != nil || n.text != "str" {
			return p.errAt(n, "expected str after |")
		}
		f.Kind, f.Block = KindStr, true
	case t.kind == 'p' && t.text == "{":
		inline := &Type{}
		if c := p.trailing(); c != "" && f.Desc == "" {
			f.Desc = c
		}
		if err := p.fields(inline, refs); err != nil {
			return err
		}
		f.Kind, f.Type = KindObject, inline
	case t.kind == 'n' && t.text == "event":
		if inEvent {
			return p.errAt(t, "an event list can't hold event lists directly")
		}
		item := &Field{Name: f.Name}
		if err := p.typeExpr(item, refs, true); err != nil {
			return err
		}
		if item.Optional {
			return p.errAt(t, "event list items can't be optional")
		}
		f.Kind, f.Item = KindEvent, item
		return nil
	case t.kind == 'n' && t.text == "str":
		f.Kind = KindStr
	case t.kind == 'n' && t.text == "num":
		f.Kind = KindNum
	case t.kind == 'n' && t.text == "path":
		f.Kind = KindPath
	case t.kind == 'n' && t.text == "enum":
		if _, err := p.expect("("); err != nil {
			return err
		}
		for {
			w, err := p.name("an enum word")
			if err != nil {
				return err
			}
			for _, x := range f.Words {
				if x == w.text {
					return p.errAt(w, "enum word %s is repeated", w.text)
				}
			}
			f.Words = append(f.Words, w.text)
			sep := p.next()
			if sep.text == ")" {
				break
			}
			if sep.text != "," {
				return p.errAt(sep, "expected , or ), found %q", sep.text)
			}
			if p.peek().text == ")" {
				p.next()
				break
			}
		}
		f.Kind = KindEnum
	case t.kind == 'n' && !isBuiltin(t.text):
		f.Kind = KindObject
		*refs = append(*refs, pending{f: f, tok: t})
	default:
		return p.errAt(t, "expected a type, found %q", t.text)
	}
	if q := p.peek(); q.kind == 'p' && q.text == "?" {
		p.next()
		f.Optional = true
	}
	return nil
}

// checkCycles rejects a type that contains itself.
func checkCycles(t *Type, stack []*Type) error {
	for _, s := range stack {
		if s == t {
			return &SchemaError{Msg: fmt.Sprintf("type %s contains itself", t.Name)}
		}
	}
	stack = append(stack, t)
	for _, f := range t.Fields {
		if f.Type != nil {
			if err := checkCycles(f.Type, stack); err != nil {
				return err
			}
		}
	}
	return nil
}

// linkItems sets each object event list's Type to its item type.
func linkItems(t *Type) {
	for _, f := range t.Fields {
		if f.Kind == KindEvent && f.Item.Kind == KindObject {
			f.Type = f.Item.Type
		}
		if f.Type != nil && f.Type.Name == "" {
			linkItems(f.Type)
		}
	}
}

// derive fills in slots and events, children first.
func derive(t *Type, done map[*Type]bool) error {
	if done[t] {
		return nil
	}
	done[t] = true
	for _, f := range t.Fields {
		if f.Type != nil {
			if err := derive(f.Type, done); err != nil {
				return err
			}
		}
		switch {
		case f.Kind == KindEvent:
			t.slots = append(t.slots, f)
		case f.Kind == KindObject && f.Type.events:
			if f.Optional {
				return &SchemaError{Msg: fmt.Sprintf("optional field %s can't contain event lists", f.Name)}
			}
			t.slots = append(t.slots, f)
		}
	}
	if len(t.slots) > 64 {
		return &SchemaError{Msg: "an object can have at most 64 event lists and nested objects with event lists"}
	}
	t.events = len(t.slots) > 0
	return nil
}

// segments splits a path into its dotted parts.
func segments(path string) []string {
	if path == "" {
		return nil
	}
	return strings.Split(path, ".")
}
