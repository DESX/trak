package trak

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const buildSchemaText = `// One CI build run.
{
  user: str,                  // who triggered the build
  status: enum(ok, failed),
  duration: num @ms,          // wall-clock time
  tags: [] str,
  stdout: |str @log,          // compiler output
  error: str?,                // set when the build failed
}
`

func TestSchemaSpecExample(t *testing.T) {
	s, err := buildSchema("build", 1, "1.0", buildSchemaText, true)
	if err != nil {
		t.Fatal(err)
	}
	if s.Description != "One CI build run." {
		t.Errorf("description %q", s.Description)
	}
	var paths []string
	for _, f := range s.Fields {
		paths = append(paths, f.Path)
	}
	if !reflect.DeepEqual(paths, []string{"user", "status", "duration", "tags.[]", "stdout", "error"}) {
		t.Errorf("paths %v", paths)
	}
	user, _ := s.Field("user")
	stdout, _ := s.Field("stdout")
	errF, _ := s.Field("error")
	dur, _ := s.Field("duration")
	if user.Description != "who triggered the build" || !stdout.Block || !errF.Optional ||
		dur.Annotations[0].String() != "@ms" {
		t.Errorf("fields %+v", s.Fields)
	}
	if _, ok := s.Field("nope"); ok {
		t.Error("unexpected field")
	}
}

func TestSchemaBinding(t *testing.T) {
	root, _, err := ParseDefinition("{ a: [] str?, b: [] str @x, c: |str @log(x y), d: {} num? @u, e: {}, f: {} {} }", true)
	if err != nil {
		t.Fatal(err)
	}
	m := root.Members
	if m[0].Type.Kind != KindList || !m[0].Type.Optional || m[0].Type.Elem.Optional {
		t.Error("a")
	}
	if m[1].Type.Kind != KindList || len(m[1].Type.Annotations) != 1 {
		t.Error("b")
	}
	if !m[2].Type.Block || m[2].Type.Annotations[0].String() != "@log(x y)" {
		t.Error("c")
	}
	if m[3].Type.Kind != KindMap || !m[3].Type.Optional {
		t.Error("d")
	}
	if m[4].Type.Kind != KindObject || len(m[4].Type.Members) != 0 {
		t.Error("e")
	}
	if m[5].Type.Kind != KindMap || m[5].Type.Elem.Kind != KindObject {
		t.Error("f")
	}
}

func TestSchemaDescriptions(t *testing.T) {
	text := "// Line one.\n//\n// Line three.\n{\n  a: str, b: str, // b\n  // ignored\n  c: { // c\n    d: str, // d\n  },\n  e: str, //\n}\n"
	s, err := buildSchema("t", 1, "1.0", text, true)
	if err != nil {
		t.Fatal(err)
	}
	if s.Description != "Line one.\n\nLine three." {
		t.Errorf("%q", s.Description)
	}
	got := []string{s.Root.Members[0].Description, s.Root.Members[1].Description, s.Root.Members[2].Description, s.Root.Members[3].Description}
	if !reflect.DeepEqual(got, []string{"", "b", "c", ""}) {
		t.Errorf("%q", got)
	}
	if d, _ := s.Field("c.d"); d.Description != "d" {
		t.Error("c.d")
	}
}

func TestSchemaUnknownLeaves(t *testing.T) {
	s, _ := buildSchema("t", 1, "1.1", "{ a: si.meter, b: |vec(3)? }", false)
	if s.Err != nil || s.Fields[0].Unknown != "si.meter" || s.Fields[1].Unknown != "vec(3)" || !s.Fields[1].Block {
		t.Fatalf("%+v %v", s.Fields, s.Err)
	}
	if _, err := buildSchema("t", 1, "1.0", "{ a: foo }", true); err == nil {
		t.Fatal("strict should reject unknown leaves")
	}
	for _, v := range []string{"2.0", "1.00", "01.0"} {
		if s, _ := buildSchema("t", 1, v, "{}", false); s.Root != nil || s.Err == nil {
			t.Errorf("version %s should be unparseable", v)
		}
	}
}

func TestSchemaErrors(t *testing.T) {
	cases := []struct {
		text, msg    string
		line, column int
	}{
		{"", "expected '{'", 1, 1},
		{"{ a: str", "expected ',' or '}'", 1, 9},
		{"{ a str }", "expected ':'", 1, 5},
		{"{ a: str, a: num }", "duplicate key 'a'", 1, 11},
		{"{ 1a: str }", "expected a key", 1, 3},
		{"{\n  a: |[] str }", "'|' applies only to a leaf", 2, 6},
		{"{ a: ||str }", "'|' applies only to a leaf", 1, 6},
		{"{ a: str?? }", "repeated '?'", 1, 10},
		{"{ a: enum() }", "at least one word", 1, 6},
		{"{ a: enum(x, x) }", "duplicate enum word", 1, 14},
		{"{ a: enum(x y) }", "expected ',' or ')'", 1, 13},
		{"{ a: enum(1) }", "expected an enum word", 1, 11},
		{"{ a: enum }", "expected '(' after enum", 1, 10},
		{"{ a: str(x) }", "str takes no parameters", 1, 9},
		{"{ a: num @ }", "expected an annotation name", 1, 11},
		{"{ a: num @x(oops }", "unclosed '('", 1, 12},
		{"{ a: = }", "expected a type", 1, 6},
		{"{ a: [ str }", "expected ']'", 1, 8},
		{"{ a: str }?", "can't be optional", 1, 11},
		{"{ a: str } x", "unexpected text", 1, 12},
		{"{ a: str } // é", "schemas are ASCII", 1, 15},
		{"{ a: str,\r\n}", "expected a key", 1, 10},
	}
	for _, c := range cases {
		_, _, err := ParseDefinition(c.text, true)
		var se *SchemaError
		if !errors.As(err, &se) || !strings.Contains(se.Msg, c.msg) || se.Line != c.line || se.Column != c.column {
			t.Errorf("%q: got %v, want %q at %d:%d", c.text, err, c.msg, c.line, c.column)
		}
	}
}
