package trak

import (
	"fmt"
	"sort"
	"strings"
)

// Kind is the kind of a type node.
type Kind int

const (
	KindStr Kind = iota
	KindNum
	KindEnum
	KindObject
	KindList
	KindMap
)

func (k Kind) String() string {
	return [...]string{"str", "num", "enum", "object", "list", "map"}[k]
}

// Annotation is @Name or @Name(Text).
type Annotation struct {
	Name    string
	Text    string
	HasText bool
}

func (a Annotation) String() string {
	if a.HasText {
		return "@" + a.Name + "(" + a.Text + ")"
	}
	return "@" + a.Name
}

// Type is a node of a schema's type tree. Suffixes (? and annotations) apply
// to the node they follow, including its prefixes (D1).
type Type struct {
	Kind        Kind
	Optional    bool
	Annotations []Annotation
	// Leaves.
	Words   []string // enum words
	Block   bool
	Unknown string // original text of an unknown leaf, read as str
	// Objects.
	Members []Member
	// Lists and maps: the element or value type.
	Elem *Type
}

// IsLeaf reports whether t is str, num or enum.
func (t *Type) IsLeaf() bool { return t.Kind <= KindEnum }

// Member is one key: type entry of an object.
type Member struct {
	Key         string
	Type        *Type
	Description string
	line, pos   int
}

// Field is a leaf column of a type. Path writes list indexes as [] and map
// keys as {}, e.g. "tags.[]". Optional is true when any type from the root to
// the leaf is optional. Annotations and Description come from the leaf's own
// field, the nearest enclosing object member, outermost annotation first.
type Field struct {
	Path        string
	Type        string // "str", "num" or "enum"
	Enum        []string
	Unknown     string
	Block       bool
	Optional    bool
	Annotations []Annotation
	Description string
}

// Schema is a type's schema. Root is nil when the definition can't be parsed;
// Err then says why.
type Schema struct {
	Name        string
	Index       int
	Version     string
	Text        string
	Description string
	Root        *Type
	Fields      []Field
	Err         error
}

// Annotations returns the annotations on the event's top-level object.
func (s *Schema) Annotations() []Annotation {
	if s.Root == nil {
		return nil
	}
	return s.Root.Annotations
}

// Field returns the field with the given path template.
func (s *Schema) Field(path string) (Field, bool) {
	for _, f := range s.Fields {
		if f.Path == path {
			return f, true
		}
	}
	return Field{}, false
}

func validTypeName(name string) bool { return matchWord(name, ".") == len(name) && name != "" }

// matchWord returns the length of an identifier [A-Za-z_][A-Za-z0-9_<extra>]*
// at the start of s, or 0.
func matchWord(s string, extra string) int {
	if s == "" || !(isAlpha(s[0]) || s[0] == '_') {
		return 0
	}
	i := 1
	for i < len(s) && (isAlpha(s[i]) || isDigit(s[i]) || s[i] == '_' || strings.IndexByte(extra, s[i]) >= 0) {
		i++
	}
	return i
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// ParseDefinition parses a definition and returns its root object and type
// description. With strict, unknown leaf types are errors (a 1.0 writer's
// view); without it they are read as str (a reader's view, SPEC §9).
func ParseDefinition(text string, strict bool) (*Type, string, error) {
	p := &parser{s: text, strict: strict, comments: map[int]comment{}}
	return p.parse()
}

func buildSchema(name string, index int, version, text string, strict bool) (*Schema, error) {
	s := &Schema{Name: name, Index: index, Version: version, Text: text}
	major, _, ok := strings.Cut(version, ".")
	var err error
	if !ok || major != "1" || !canonicalVersion([]byte(version)) {
		err = &SchemaError{Msg: "unsupported schema version " + version}
	} else {
		s.Root, s.Description, err = ParseDefinition(text, strict)
	}
	if err != nil {
		if strict {
			return nil, err
		}
		s.Root, s.Err = nil, err
		return s, nil
	}
	s.Fields = flatten(s.Root)
	return s, nil
}

func flatten(root *Type) []Field {
	var out []Field
	flattenObject(root, "", false, &out)
	return out
}

func flattenObject(obj *Type, prefix string, optional bool, out *[]Field) {
	for _, m := range obj.Members {
		path := m.Key
		if prefix != "" {
			path = prefix + "." + m.Key
		}
		flattenType(m.Type, path, optional, nil, m.Description, out)
	}
}

func flattenType(t *Type, path string, optional bool, anns []Annotation, desc string, out *[]Field) {
	optional = optional || t.Optional
	anns = append(append([]Annotation(nil), anns...), t.Annotations...)
	switch t.Kind {
	case KindStr, KindNum, KindEnum:
		*out = append(*out, Field{
			Path: path, Type: t.Kind.String(), Enum: t.Words, Unknown: t.Unknown, Block: t.Block,
			Optional: optional, Annotations: anns, Description: desc,
		})
	case KindObject:
		flattenObject(t, path, optional, out)
	case KindList:
		flattenType(t.Elem, path+".[]", optional, anns, desc, out)
	case KindMap:
		flattenType(t.Elem, path+".{}", optional, anns, desc, out)
	}
}

type comment struct {
	line  int
	alone bool
	text  string
}

type parser struct {
	s        string
	i        int
	strict   bool
	comments map[int]comment
	members  []*Member
	pending  []pendingObject
}

// pendingObject holds an object's members by pointer until descriptions are
// assigned at the end of parsing.
type pendingObject struct {
	obj     *Type
	members []*Member
}

type parseFail struct{ err *SchemaError }

func (p *parser) fail(pos int, format string, args ...any) {
	line := strings.Count(p.s[:pos], "\n") + 1
	col := pos - (strings.LastIndexByte(p.s[:pos], '\n') + 1) + 1
	panic(parseFail{&SchemaError{Msg: fmt.Sprintf(format, args...), Line: line, Column: col}})
}

func (p *parser) peek() byte {
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

func (p *parser) ws() {
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			p.i++
		case strings.HasPrefix(p.s[p.i:], "//"):
			end := strings.IndexByte(p.s[p.i:], '\n')
			if end < 0 {
				end = len(p.s)
			} else {
				end += p.i
			}
			if _, seen := p.comments[p.i]; !seen {
				lineStart := strings.LastIndexByte(p.s[:p.i], '\n') + 1
				p.comments[p.i] = comment{
					line:  strings.Count(p.s[:p.i], "\n") + 1,
					alone: strings.Trim(p.s[lineStart:p.i], " \t") == "",
					text:  strings.Trim(p.s[p.i+2:end], " \t"),
				}
			}
			p.i = end
		default:
			return
		}
	}
}

func (p *parser) expect(c byte) {
	p.ws()
	if p.peek() != c {
		p.fail(p.i, "expected '%c'", c)
	}
	p.i++
}

func (p *parser) parse() (root *Type, desc string, err error) {
	defer func() {
		if r := recover(); r != nil {
			f, ok := r.(parseFail)
			if !ok {
				panic(r)
			}
			root, desc, err = nil, "", f.err
		}
	}()
	for i := 0; i < len(p.s); i++ {
		if p.s[i] >= 0x80 {
			p.fail(i, "schemas are ASCII")
		}
	}
	p.ws()
	if p.peek() != '{' {
		p.fail(p.i, "expected '{' after the type's comment lines")
	}
	brace := p.i
	root = p.parseObject()
	for {
		p.ws()
		if c := p.peek(); c == '@' {
			root.Annotations = append(root.Annotations, p.parseAnnotation())
		} else if c == '?' {
			p.fail(p.i, "an event's top-level type can't be optional")
		} else {
			break
		}
	}
	if p.i != len(p.s) {
		p.fail(p.i, "unexpected text after the definition")
	}
	positions := make([]int, 0, len(p.comments))
	for pos := range p.comments {
		positions = append(positions, pos)
	}
	sort.Ints(positions)
	var lines []string
	for _, pos := range positions {
		if pos < brace {
			lines = append(lines, p.comments[pos].text)
		}
	}
	desc = strings.Join(lines, "\n")
	byLine := map[int]*Member{}
	for _, m := range p.members {
		if prev := byLine[m.line]; prev == nil || m.pos > prev.pos {
			byLine[m.line] = m
		}
	}
	for _, pos := range positions {
		c := p.comments[pos]
		if pos > brace && !c.alone && byLine[c.line] != nil {
			byLine[c.line].Description = c.text
		}
	}
	for _, po := range p.pending {
		for i, m := range po.members {
			po.obj.Members[i] = *m
		}
	}
	return root, desc, nil
}

func (p *parser) parseObject() *Type {
	p.expect('{')
	obj := &Type{Kind: KindObject, Members: []Member{}}
	var members []*Member
	keys := map[string]bool{}
	for {
		p.ws()
		if p.peek() == '}' {
			p.i++
			break
		}
		n := matchWord(p.s[p.i:], "")
		if n == 0 {
			p.fail(p.i, "expected a key")
		}
		pos := p.i
		key := p.s[pos : pos+n]
		if keys[key] {
			p.fail(pos, "duplicate key '%s'", key)
		}
		keys[key] = true
		p.i += n
		p.expect(':')
		t := p.parseType()
		members = append(members, &Member{Key: key, Type: t, line: strings.Count(p.s[:pos], "\n") + 1, pos: pos})
		p.ws()
		switch p.peek() {
		case ',':
			p.i++
		case '}':
		default:
			p.fail(p.i, "expected ',' or '}'")
		}
	}
	p.members = append(p.members, members...)
	obj.Members = make([]Member, len(members))
	p.pending = append(p.pending, pendingObject{obj, members})
	return obj
}

func (p *parser) parseType() *Type {
	type prefix struct {
		kind byte
		pos  int
	}
	var prefixes []prefix
	var node *Type
	for node == nil {
		p.ws()
		c, pos := p.peek(), p.i
		if c == '|' {
			prefixes = append(prefixes, prefix{'|', pos})
			p.i++
		} else if c == '[' {
			p.i++
			p.expect(']')
			prefixes = append(prefixes, prefix{'[', pos})
		} else if c == '{' {
			p.i++
			p.ws()
			if p.peek() != '}' {
				p.i = pos
				break
			}
			p.i++
			after := p.i
			p.ws()
			if c := p.peek(); c == '|' || c == '{' || c == '[' || c == '_' || isAlpha(c) {
				prefixes = append(prefixes, prefix{'{', pos})
			} else {
				p.i = after
				node = &Type{Kind: KindObject, Members: []Member{}}
			}
		} else {
			break
		}
	}
	if node == nil {
		node = p.parseBase()
	}
	for i := len(prefixes) - 1; i >= 0; i-- {
		switch prefixes[i].kind {
		case '|':
			if !node.IsLeaf() || node.Block {
				p.fail(prefixes[i].pos, "'|' applies only to a leaf")
			}
			node.Block = true
		case '[':
			node = &Type{Kind: KindList, Elem: node}
		case '{':
			node = &Type{Kind: KindMap, Elem: node}
		}
	}
	for {
		p.ws()
		switch p.peek() {
		case '?':
			if node.Optional {
				p.fail(p.i, "repeated '?'")
			}
			node.Optional = true
			p.i++
		case '@':
			node.Annotations = append(node.Annotations, p.parseAnnotation())
		default:
			return node
		}
	}
}

func (p *parser) parseAnnotation() Annotation {
	p.i++
	n := matchWord(p.s[p.i:], ".-")
	if n == 0 {
		p.fail(p.i, "expected an annotation name")
	}
	a := Annotation{Name: p.s[p.i : p.i+n]}
	p.i += n
	if p.peek() == '(' {
		a.Text, a.HasText = p.parseParens(), true
	}
	return a
}

func (p *parser) parseParens() string {
	start := p.i + 1
	end := strings.IndexByte(p.s[start:], ')')
	if end < 0 || strings.IndexByte(p.s[start:start+end], '\n') >= 0 {
		p.fail(p.i, "unclosed '(' (text can't contain ')' or a line break)")
	}
	p.i = start + end + 1
	return p.s[start : start+end]
}

func (p *parser) parseBase() *Type {
	if p.peek() == '{' {
		return p.parseObject()
	}
	start := p.i
	n := matchWord(p.s[p.i:], ".")
	if n == 0 {
		p.fail(p.i, "expected a type")
	}
	word := p.s[p.i : p.i+n]
	p.i += n
	switch word {
	case "str", "num":
		if p.peek() == '(' {
			p.fail(p.i, "%s takes no parameters", word)
		}
		if word == "str" {
			return &Type{Kind: KindStr}
		}
		return &Type{Kind: KindNum}
	case "enum":
		if p.peek() != '(' {
			p.fail(p.i, "expected '(' after enum")
		}
		p.i++
		var words []string
		for {
			p.ws()
			if p.peek() == ')' {
				p.i++
				break
			}
			n := matchWord(p.s[p.i:], "")
			if n == 0 {
				p.fail(p.i, "expected an enum word")
			}
			w := p.s[p.i : p.i+n]
			for _, x := range words {
				if x == w {
					p.fail(p.i, "duplicate enum word '%s'", w)
				}
			}
			words = append(words, w)
			p.i += n
			p.ws()
			switch p.peek() {
			case ',':
				p.i++
			case ')':
			default:
				p.fail(p.i, "expected ',' or ')'")
			}
		}
		if len(words) == 0 {
			p.fail(start, "enum needs at least one word")
		}
		return &Type{Kind: KindEnum, Words: words}
	}
	if p.peek() == '(' {
		p.parseParens()
	}
	if p.strict {
		p.fail(start, "unknown type '%s'", word)
	}
	return &Type{Kind: KindStr, Unknown: p.s[start:p.i]}
}
