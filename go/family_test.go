package trak

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Event families (SPEC §1, §5, §6, §7): schemas, numbering, tables, seeks and
// append.

func stamp(i int) string {
	return fmt.Sprintf("20261107T14%02d%02d.%03dZ", i/60000%60, i/1000%60, i%1000)
}

type famOp struct {
	name  string
	value map[string]any
}

func build(extra ...any) map[string]any {
	v := map[string]any{"user": "ana", "ok": "yes"}
	for i := 0; i < len(extra); i += 2 {
		v[extra[i].(string)] = extra[i+1]
	}
	return v
}

var famOps = []famOp{
	{"build", build()},
	{"build.step", build("step", "compile")},
	{"other", map[string]any{"x": "1"}},
	{"build.step.cmd", build("step", "compile", "argv", []string{"cc", "-c"})},
	{"build.test", build("passed", 10)},
	{"build.step", build("step", "link")},
	{"build", build("user", "bo")},
}

func makeFamily(t testing.TB, path string, ops []famOp) {
	w := must(Create(path, Options{Overwrite: true}))
	defer w.Close()
	must(w.Define("build", "// A build.\n{ user: str, ok: enum(yes, no) }"))
	must(w.Define("build.step", "// One step.\n{ step: str }"))
	must(w.Define("build.step.cmd", "{ argv: [] str }"))
	must(w.Define("build.test", "{ passed: num }"))
	must(w.Define("other", "{ x: str }"))
	for i, op := range ops {
		if _, err := w.WriteAt(op.name, op.value, mustTime(stamp(i))); err != nil {
			t.Fatal(err)
		}
	}
}

func famFile(t testing.TB) string {
	p := filepath.Join(t.TempDir(), "f.trak")
	makeFamily(t, p, famOps)
	return p
}

type tn struct {
	typ string
	n   int64
}

func allEvents(t testing.TB, r *Reader) []*Event {
	var out []*Event
	ok(t, r.Events(func(e *Event) bool { out = append(out, e); return true }))
	return out
}

func famRange(t testing.TB, fam *Family, a, b int64) []tn {
	var out []tn
	ok(t, fam.Range(a, b, func(e *Event) bool { out = append(out, tn{e.Type(), e.N()}); return true }))
	return out
}

func TestChildSchemas(t *testing.T) {
	r := must(Open(famFile(t)))
	defer r.Close()
	step := must(r.Schema("build.step"))
	if step.Parent != "build" || !reflect.DeepEqual(step.Lineage, []string{"build", "build.step"}) || step.Version != "1.1" {
		t.Fatalf("%+v", step)
	}
	if b := must(r.Schema("build")); b.Version != "1.0" || b.Parent != "" || !reflect.DeepEqual(b.Lineage, []string{"build"}) {
		t.Fatalf("%+v", b)
	}
	if step.Description != "One step." || step.Text != "// One step.\n{ step: str }\n" {
		t.Fatalf("%q %q", step.Description, step.Text)
	}
	paths := func(s *Schema) []string {
		var out []string
		for _, f := range s.Fields {
			out = append(out, f.Path)
		}
		return out
	}
	if got := paths(step); !reflect.DeepEqual(got, []string{"user", "ok", "step"}) {
		t.Fatal(got)
	}
	cmd := must(r.Schema("build.step.cmd"))
	if got := paths(cmd); !reflect.DeepEqual(got, []string{"user", "ok", "step", "argv.[]"}) {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(cmd.Lineage, []string{"build", "build.step", "build.step.cmd"}) {
		t.Fatal(cmd.Lineage)
	}
}

func TestFamilyNumbers(t *testing.T) {
	r := must(Open(famFile(t)))
	defer r.Close()
	events := allEvents(t, r)
	type row struct {
		typ       string
		root, own int64
	}
	var got []row
	var exts []uint64
	for _, e := range events {
		got = append(got, row{e.Type(), must(e.FamilyNumber("")), must(e.FamilyNumber(e.Type()))})
		exts = append(exts, e.f.vlqs[5])
	}
	want := []row{{"build", 1, 1}, {"build.step", 2, 1}, {"other", 1, 1}, {"build.step.cmd", 3, 1},
		{"build.test", 4, 1}, {"build.step", 5, 3}, {"build", 6, 6}}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	cmd := events[3]
	if n := must(cmd.FamilyNumber("build.step")); n != 2 {
		t.Fatal(n)
	}
	if _, err := cmd.FamilyNumber("build.test"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// Extensions as written: one per ancestor, plus its own once a child exists.
	if !reflect.DeepEqual(exts, []uint64{1, 2, 0, 2, 1, 2, 1}) {
		t.Fatal(exts)
	}
}

func TestFamilyTable(t *testing.T) {
	r := must(Open(famFile(t)))
	defer r.Close()
	f := must(r.Family("build"))
	if f.Name() != "build" || f.Types()[0].Name != "build" || len(f.Types()) != 4 {
		t.Fatal(f.Types())
	}
	if n := must(f.Count()); n != 6 {
		t.Fatal(n)
	}
	want := []tn{{"build", 1}, {"build.step", 1}, {"build.step.cmd", 1}, {"build.test", 1}, {"build.step", 2}, {"build", 2}}
	if got := famRange(t, f, 1, 6); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if got := famRange(t, f, 5, 2); !reflect.DeepEqual(got, []tn{{"build.step", 2}, {"build.test", 1}, {"build.step.cmd", 1}, {"build.step", 1}}) {
		t.Fatal(got)
	}
	if e := must(f.Get(4)); e.Type() != "build.test" {
		t.Fatal(e.Type())
	}
	if e := must(f.Latest()); e.Type() != "build" || e.N() != 2 {
		t.Fatal(e.Type(), e.N())
	}
	for _, m := range []int64{0, 7} {
		if _, err := f.Get(m); !errors.Is(err, ErrNotFound) {
			t.Fatal(m, err)
		}
	}
	if err := f.Range(1, 7, func(*Event) bool { return true }); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	stopped := 0
	ok(t, f.Range(1, 6, func(*Event) bool { stopped++; return stopped < 2 }))
	if stopped != 2 {
		t.Fatal(stopped)
	}
	sub := must(r.Family("build.step"))
	if got := famRange(t, sub, 1, must(sub.Count())); !reflect.DeepEqual(got, []tn{{"build.step", 1}, {"build.step.cmd", 1}, {"build.step", 2}}) {
		t.Fatal(got)
	}
	if n := must(must(r.Family("other")).Count()); n != 1 {
		t.Fatal(n)
	}
	if n := must(must(r.Family("build.test")).Count()); n != 1 {
		t.Fatal(n)
	}
	at := must(f.AtTime(mustTime(stamp(3))))
	if at == nil || at.Type() != "build.step.cmd" || must(at.FamilyNumber("")) != 3 {
		t.Fatal(at)
	}
	if at := must(f.AtTime(mustTime(stamp(99)))); at != nil {
		t.Fatal(at)
	}
	if _, err := r.Family("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestEmptyFamily(t *testing.T) {
	p := filepath.Join(t.TempDir(), "e.trak")
	w := must(Create(p, Options{}))
	must(w.Define("a", "{}"))
	must(w.Define("a.b", "{}"))
	ok(t, w.Close())
	r := must(Open(p))
	defer r.Close()
	f := must(r.Family("a"))
	if n := must(f.Count()); n != 0 {
		t.Fatal(n)
	}
	if e := must(f.Latest()); e != nil {
		t.Fatal(e)
	}
	if at := must(f.AtTime(mustTime("20261107T140000.000Z"))); at != nil {
		t.Fatal(at)
	}
}

func TestWriterFamilyRules(t *testing.T) {
	p := filepath.Join(t.TempDir(), "w.trak")
	w := must(Create(p, Options{}))
	var se *SchemaError
	if _, err := w.Define("a.b", "{}"); !errors.As(err, &se) || !strings.Contains(se.Msg, "before its child") {
		t.Fatal(err)
	}
	must(w.Define("a", "{ x: str }"))
	if _, err := w.Define("a.b", "{\n  y: str,\n  x: str,\n}"); !errors.As(err, &se) ||
		!strings.Contains(se.Msg, "already defined by 'a'") || se.Line != 3 || se.Column != 3 {
		t.Fatal(err)
	}
	must(w.Define("a.b", "{ y: str }"))
	must(w.WriteAt("a", map[string]any{"x": "1"}, mustTime("20261107T140000.005Z")))
	var ve *ValueError
	if _, err := w.WriteAt("a.b", map[string]any{"x": "2", "y": "3"}, mustTime("20261107T140000.004Z")); !errors.As(err, &ve) ||
		!strings.Contains(ve.Msg, "family a") {
		t.Fatal(err)
	}
	if _, err := w.WriteAt("a.b", map[string]any{"y": "3"}, mustTime("20261107T140000.006Z")); !errors.As(err, &ve) ||
		!strings.Contains(ve.Msg, "required") {
		t.Fatal(err)
	}
	// The default timestamp never goes back within the family.
	must(w.WriteAt("a.b", map[string]any{"x": "2", "y": "3"}, mustTime("20991107T140000.000Z")))
	must(w.Write("a", map[string]any{"x": "4"}))
	ok(t, w.Close())
	r := must(Open(p))
	defer r.Close()
	events := allEvents(t, r)
	if ts := events[len(events)-1].Timestamp(); ts != "20991107T140000.000Z" {
		t.Fatal(ts)
	}
}

func TestLateChildNumbers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "late.trak")
	w := must(Create(p, Options{}))
	must(w.Define("job", "{ i: num }"))
	for i := 0; i < 5; i++ {
		must(w.Write("job", map[string]any{"i": i}))
	}
	must(w.Define("job.retry", "{}"))
	must(w.Write("job", map[string]any{"i": 5}))
	must(w.Write("job.retry", map[string]any{"i": 6}))
	ok(t, w.Close())
	r := must(Open(p))
	defer r.Close()
	var exts []uint64
	var nums []int64
	for _, e := range allEvents(t, r) {
		exts = append(exts, e.f.vlqs[5])
		nums = append(nums, must(e.FamilyNumber("")))
	}
	if !reflect.DeepEqual(exts, []uint64{0, 0, 0, 0, 0, 1, 1}) || !reflect.DeepEqual(nums, []int64{1, 2, 3, 4, 5, 6, 7}) {
		t.Fatal(exts, nums)
	}
	if e := must(must(r.Family("job")).Get(7)); e.Type() != "job.retry" {
		t.Fatal(e.Type())
	}
	if n := must(Check(p)); n != 9 {
		t.Fatal(n)
	}
}

func TestAppendRestoresFamilyState(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	names := []string{"build", "build.step", "build.step.cmd", "build.test", "other"}
	var ops []famOp
	for i := 0; i < 300; i++ {
		name := names[rng.Intn(len(names))]
		v := build()
		switch name {
		case "other":
			v = map[string]any{"x": "1"}
		case "build.step":
			v["step"] = "s"
		case "build.step.cmd":
			v["step"], v["argv"] = "s", []string{}
		case "build.test":
			v["passed"] = 1
		}
		ops = append(ops, famOp{name, v})
	}
	dir := t.TempDir()
	whole, parts := filepath.Join(dir, "whole.trak"), filepath.Join(dir, "parts.trak")
	makeFamily(t, whole, ops)
	makeFamily(t, parts, ops[:100])
	for _, span := range [][2]int{{100, 101}, {101, 250}, {250, 300}} {
		w := must(OpenAppend(parts, Options{}))
		for i := span[0]; i < span[1]; i++ {
			must(w.WriteAt(ops[i].name, ops[i].value, mustTime(stamp(i))))
		}
		ok(t, w.Close())
	}
	a, _ := os.ReadFile(parts)
	b, _ := os.ReadFile(whole)
	if !bytes.Equal(a, b) {
		t.Fatal("appended file differs")
	}
	w := must(OpenAppend(parts, Options{}))
	defer w.Close()
	var ve *ValueError
	if _, err := w.WriteAt("build.test", build("passed", 1), mustTime(stamp(0))); !errors.As(err, &ve) ||
		!strings.Contains(ve.Msg, "family build") {
		t.Fatal(err)
	}
}

func TestFamilyGetMatchesScan(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	p := filepath.Join(t.TempDir(), "wide.trak")
	w := must(Create(p, Options{}))
	must(w.Define("r", "{ i: num }"))
	names := []string{"r"}
	for i := 0; i < 9; i++ {
		names = append(names, fmt.Sprintf("r.k%d", i))
		must(w.Define(names[len(names)-1], "{}"))
	}
	must(w.Define("r.k0.g", "{}"))
	names = append(names, "r.k0.g")
	for i := 0; i < 3000; i++ {
		must(w.WriteAt(names[rng.Intn(len(names))], map[string]any{"i": i}, mustTime(stamp(i/3))))
	}
	ok(t, w.Close())
	r := must(Open(p))
	defer r.Close()
	events := allEvents(t, r)
	for _, name := range []string{"r", "r.k0"} {
		f := must(r.Family(name))
		var members []*Event
		for _, e := range events {
			for _, a := range e.Schema.Lineage {
				if a == name {
					members = append(members, e)
				}
			}
		}
		count := int64(len(members))
		if n := must(f.Count()); n != count {
			t.Fatal(name, n, count)
		}
		for _, m := range append([]int64{1, 2, count}, randomNs(rng, count, 100)...) {
			e := must(f.Get(m))
			if e.Offset() != members[m-1].Offset() {
				t.Fatalf("%s: row %d", name, m)
			}
		}
		lo, hi := int64(17), min(400, count)
		var up, down []int64
		ok(t, f.Range(lo, hi, func(e *Event) bool { up = append(up, e.Offset()); return true }))
		ok(t, f.Range(hi, lo, func(e *Event) bool { down = append(down, e.Offset()); return true }))
		for i := lo; i <= hi; i++ {
			if up[i-lo] != members[i-1].Offset() || down[hi-i] != members[i-1].Offset() {
				t.Fatalf("%s: range row %d", name, i)
			}
		}
		for _, ms := range []int{0, 1, 500, 999} {
			at := must(f.AtTime(mustTime(stamp(ms))))
			var want *Event
			for _, e := range members {
				if e.Timestamp() >= stamp(ms) {
					want = e
					break
				}
			}
			if (at == nil) != (want == nil) || at != nil && at.Offset() != want.Offset() {
				t.Fatalf("%s: at %d", name, ms)
			}
		}
	}
	if n := must(Check(p)); n != 3011 {
		t.Fatal(n)
	}
}

func TestCheckRejectsBadFamilies(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.trak")
	makeFamily(t, p, famOps)
	data, _ := os.ReadFile(p)
	var line []byte
	for _, l := range bytes.Split(data, []byte("\n")) {
		if bytes.HasPrefix(l, []byte("==[ build.step 1 ")) {
			line = l
		}
	}
	head, letters, _ := bytes.Cut(line, []byte("]=="))
	vlqs := must(appendVLQs(nil, letters, 0))
	if !reflect.DeepEqual(vlqs[5:8], []uint64{2, 2, 1}) { // E = 2: number 2 in build, 1 in build.step
		t.Fatal(vlqs)
	}
	vlqs[6] = 9
	forged := append(append([]byte(nil), head...), "]=="...)
	for _, v := range vlqs {
		forged = AppendVLQ(forged, v)
	}
	ok(t, os.WriteFile(p, bytes.Replace(data, line, forged, 1), 0o644))
	var fe *FormatError
	if _, err := Check(p); !errors.As(err, &fe) || !strings.Contains(fe.Msg, "family numbers") {
		t.Fatal(err)
	}
}

func TestPrev(t *testing.T) {
	r := must(Open(famFile(t)))
	defer r.Close()
	e := must(must(r.Table("build.step")).Latest())
	if e.N() != 2 {
		t.Fatal(e.N())
	}
	before := r.FooterReads
	p := must(e.Prev())
	if r.FooterReads-before != 1 {
		t.Fatal(r.FooterReads - before)
	}
	if v, _, _ := p.Get("step"); p.N() != 1 || v != "compile" {
		t.Fatal(p.N(), v)
	}
	if pp := must(p.Prev()); pp != nil {
		t.Fatal(pp)
	}
}

func TestSeekAndAtTime(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.trak")
	w := must(Create(p, Options{}))
	must(w.Define("t", "{ i: num }"))
	for i := 0; i < 1000; i++ {
		must(w.WriteAt("t", map[string]any{"i": i}, mustTime(stamp(i/4*4))))
	}
	ok(t, w.Close())
	r := must(Open(p))
	defer r.Close()
	tab := must(r.Table("t"))
	for i := 0; i <= 1000; i += 7 {
		e := must(tab.AtTime(mustTime(stamp(i))))
		want := (i + 3) / 4 * 4
		if want >= 1000 {
			if e != nil {
				t.Fatal(i, e.N())
			}
		} else if e == nil || e.N() != int64(want+1) || e.Timestamp() != stamp(want) {
			t.Fatal(i, e)
		}
	}
}

func TestDottedNameIn10IsPlain(t *testing.T) {
	r := must(Open("../conformance/valid/109-dotted-name-in-1-0.trak"))
	defer r.Close()
	for _, s := range r.Types() {
		if s.Parent != "" || len(s.Lineage) != 1 {
			t.Fatalf("%+v", s)
		}
	}
}
