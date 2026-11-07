package trak

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func specExample(t testing.TB) []byte {
	data, err := os.ReadFile("../SPEC.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	s = s[strings.Index(s, "## 13. Example"):]
	start := strings.Index(s, "```\n") + 4
	return []byte(s[start : start+strings.Index(s[start:], "```")])
}

const deployText = "// A release pushed to an environment.\n{\n  target: str,\n  version: str @semver,\n}\n"

func mustTime(s string) time.Time {
	t, err := ParseTimestamp(s)
	if err != nil {
		panic(err)
	}
	return t
}

func writeExample(t testing.TB, path string) {
	w, err := Create(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	must(w.Define("build", strings.TrimSuffix(buildSchemaText, "\n")))
	must(w.Define("deploy", deployText))
	must(w.WriteAt("build", map[string]any{
		"user": "ana", "status": "ok", "duration": 910, "tags": []string{"ci"},
		"stdout": "Compiling core...\nDone.",
	}, mustTime("20261107T140210.120Z")))
	must(w.WriteAt("build", struct {
		User     string   `trak:"user"`
		Status   string   `trak:"status"`
		Duration float64  `trak:"duration"`
		Tags     []string `trak:"tags"`
		Stdout   []byte   `trak:"stdout"`
		Error    *string  `trak:"error"`
		private  int
	}{"joel", "failed", 1520, []string{"ci", "nightly"}, []byte("Compiling core...\nld: undefined symbol main"), ptr("link failed"), 0},
		mustTime("20261107T140322.415Z")))
}

func ptr[T any](v T) *T { return &v }

// must returns v, panicking (and so failing the test) on error.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func ok(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestWriterReproducesSpecExample(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.trak")
	writeExample(t, p)
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, specExample(t)) {
		t.Fatalf("got\n%s", got)
	}
}

func TestReaderOnSpecExample(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.trak")
	os.WriteFile(p, specExample(t), 0o644)
	r := must(Open(p))
	defer r.Close()
	if r.Version() != "1.0" || len(r.Types()) != 2 || r.End() != r.Size() {
		t.Fatal("header")
	}
	builds := must(r.Table("build"))
	if n := must(builds.Count()); n != 2 {
		t.Fatalf("count %d", n)
	}
	e := must(builds.Get(1))
	if e.Type() != "build" || e.N() != 1 || e.Offset() != 423 || e.Timestamp() != "20261107T140210.120Z" {
		t.Fatal("event header")
	}
	if tm := must(e.Time()); !tm.Equal(time.Date(2026, 11, 7, 14, 2, 10, 120e6, time.UTC)) {
		t.Fatal(tm)
	}
	if v, ok, _ := e.Get("user"); v != "ana" || !ok {
		t.Fatal("user")
	}
	if v, ok, _ := e.Int("duration"); v != 910 || !ok {
		t.Fatal("duration")
	}
	if v, ok, _ := e.Float("duration"); v != 910 || !ok {
		t.Fatal("duration float")
	}
	if _, ok, _ := e.Get("error"); ok {
		t.Fatal("error should be absent")
	}
	if _, ok, _ := e.Int("error"); ok {
		t.Fatal("error should be absent")
	}
	if raw, _, _ := e.Raw("stdout"); string(raw) != "Compiling core...\nDone." {
		t.Fatal("stdout")
	}
	latest := must(builds.Latest())
	leaves := must(latest.Leaves())
	if len(leaves) != 7 || leaves[4].Path != "tags.1" || string(leaves[4].Value) != "nightly" {
		t.Fatalf("%+v", leaves)
	}
	deploys := must(r.Table("deploy"))
	if n := must(deploys.Count()); n != 0 {
		t.Fatal("deploys")
	}
	if e := must(deploys.Latest()); e != nil {
		t.Fatal("latest deploy")
	}
	if _, err := r.Table("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := builds.Get(3); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if n := must(Check(p)); n != 4 {
		t.Fatal(n)
	}
}

func manyFile(t testing.TB) string {
	p := filepath.Join(t.TempDir(), "many.trak")
	w := must(Create(p, Options{}))
	must(w.Define("a", "{ i: num }"))
	must(w.Define("b", "{ s: str }"))
	must(w.Define("c", "{ }"))
	for i := 1; i <= 2000; i++ {
		ts := fromMillis(1_000_000 + int64(i)*10)
		must(w.WriteAt("a", map[string]any{"i": i}, ts))
		if i%3 == 0 {
			must(w.WriteAt("b", map[string]any{"s": fmt.Sprintf("s%d", i)}, ts))
		}
	}
	w.Close()
	return p
}

func TestNavigation(t *testing.T) {
	p := manyFile(t)
	r := must(Open(p))
	defer r.Close()
	a := must(r.Table("a"))
	for n := int64(1); n <= 2000; n++ {
		e := must(a.Get(n))
		if v, _, _ := e.Int("i"); v != n {
			t.Fatalf("event %d has i=%d", n, v)
		}
	}
	for _, n := range []int64{1, 2, 777, 1999, 2000} {
		clear(r.cache)
		before := r.FooterReads
		must(a.Get(n))
		if reads := r.FooterReads - before; reads > 3*11+4 {
			t.Errorf("get(%d) read %d footers", n, reads)
		}
	}
	var got []int64
	ok(t, a.Range(5, 600, func(e *Event) bool { got = append(got, e.N()); return true }))
	if len(got) != 596 || got[0] != 5 || got[595] != 600 {
		t.Fatal("range up")
	}
	got = nil
	ok(t, a.Range(600, 590, func(e *Event) bool { got = append(got, e.N()); return len(got) < 5 }))
	if fmt.Sprint(got) != "[600 599 598 597 596]" {
		t.Fatal(got)
	}
	if err := a.Range(0, 3, func(*Event) bool { return true }); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if e := must(a.AtTime(fromMillis(1_000_000 + 105))); e.N() != 11 {
		t.Fatal(e.N())
	}
	if e := must(a.AtTime(fromMillis(1_000_000 + 20_011))); e != nil {
		t.Fatal("past the end")
	}
	if b := must(r.Table("b")); must(b.Count()) != 666 {
		t.Fatal("b")
	}
	if n := must(Check(p)); n != 3+2000+666 {
		t.Fatal(n)
	}
}

func TestNativeNested(t *testing.T) {
	p := filepath.Join(t.TempDir(), "n.trak")
	w := must(Create(p, Options{}))
	must(w.Define("t", "{ jobs: [] { env: {} str, log: |str? }, labels: {} [] str, empty: [] {}, m: {} num }"))
	must(w.Write("t", map[string]any{
		"jobs": []any{
			map[string]any{"env": Pairs{{"z", "1"}, {"a.b", "2"}}, "log": "x\n--[ log ]--"},
			map[string]any{"env": map[string]string{"b": "2", "a": "1"}},
		},
		"labels": []any{[]any{"none", []string{}}, []any{"some", []string{"1"}}},
		"empty":  []struct{}{{}, {}},
		"m":      map[string]uint8{"k": 7},
	}))
	w.Close()
	must(Check(p))
	r := must(Open(p))
	defer r.Close()
	tab := must(r.Table("t"))
	e := must(tab.Get(1))
	got, _ := json.Marshal(nativeJSON(must(e.Native())))
	want := `{"empty":[{},{}],"jobs":[{"env":[["z","1"],["a.b","2"]],"log":"x\n--[ log ]--"},{"env":[["a","1"],["b","2"]],"log":null}],"labels":[[null,[]],["some",["1"]]],"m":[["k","7"]]}`
	if string(got) != want {
		t.Fatalf("got %s", got)
	}
	if v, _, _ := e.Get("jobs.0.env.a.b"); v != "2" {
		t.Fatal(v)
	}
}

func TestNumFormats(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{0, "0"}, {-12, "-12"}, {uint64(math.MaxUint64), "18446744073709551615"}, {0.1, "0.1"},
		{1e16, "1e+16"}, {float32(0.5), "0.5"}, {json.Number("1.5e3"), "1.5e3"}, {"-0", "-0"}, {[]byte("2"), "2"},
	}
	for _, c := range cases {
		p := filepath.Join(t.TempDir(), "n.trak")
		w := must(Create(p, Options{}))
		must(w.Define("t", "{ x: num }"))
		must(w.Write("t", map[string]any{"x": c.v}))
		w.Close()
		r := must(Open(p))
		e := must(must(r.Table("t")).Get(1))
		if v, _, _ := e.Get("x"); v != c.want {
			t.Errorf("%v: got %s, want %s", c.v, v, c.want)
		}
		r.Close()
	}
}

func TestValueErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "e.trak")
	w := must(Create(p, Options{}))
	defer w.Close()
	must(w.Define("t", "{ x: num, e: enum(a, b)?, s: str?, l: [] str?, m: {} str?, o: {y: str}? }"))
	cases := []struct {
		v   map[string]any
		msg string
	}{
		{map[string]any{"x": "01"}, "not a valid num"},
		{map[string]any{"x": math.NaN()}, "finite"},
		{map[string]any{"x": math.Inf(1)}, "finite"},
		{map[string]any{"x": true}, "bool"},
		{map[string]any{"x": []int{1}}, "decimal string"},
		{map[string]any{"x": 1, "e": "z"}, "not one of a, b"},
		{map[string]any{"x": 1, "e": 3}, "needs a string"},
		{map[string]any{"x": 1, "s": 3}, "needs a string"},
		{map[string]any{"x": 1, "l": "ab"}, "expected a list"},
		{map[string]any{"x": 1, "m": 3}, "expected a map"},
		{map[string]any{"x": 1, "m": []any{[]any{"a"}}}, "pairs"},
		{map[string]any{"x": 1, "m": map[int]string{1: "a"}}, "keys must be strings"},
		{map[string]any{"x": 1, "m": Pairs{{"a", "1"}, {"a", "2"}}}, "repeated map key"},
		{map[string]any{"x": 1, "o": 5}, "expected an object"},
		{map[string]any{"x": 1, "zzz": 1}, "unknown field 'zzz'"},
		{map[string]any{}, "x: value is required"},
		{map[string]any{"x": 1, "o": map[string]any{}}, "o.y: value is required"},
	}
	for _, c := range cases {
		_, err := w.Write("t", c.v)
		var ve *ValueError
		if !errors.As(err, &ve) || !strings.Contains(ve.Msg, c.msg) {
			t.Errorf("%v: got %v, want %q", c.v, err, c.msg)
		}
	}
	if n := must(w.Write("t", map[string]any{"x": 1})); n != 1 {
		t.Fatal(n)
	}
	if _, err := w.Write("nope", nil); err == nil {
		t.Fatal("undefined type")
	}
}

func TestTimestamps(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.trak")
	w := must(Create(p, Options{}))
	must(w.Define("t", "{}"))
	must(w.WriteAt("t", map[string]any{}, mustTime("20991231T235959.999Z")))
	must(w.Write("t", map[string]any{}))
	if _, err := w.WriteAt("t", map[string]any{}, mustTime("20991231T235959.998Z")); err == nil {
		t.Fatal("backwards timestamp accepted")
	}
	w.Close()
	r := must(Open(p))
	defer r.Close()
	e := must(must(r.Table("t")).Get(2))
	if e.Timestamp() != "20991231T235959.999Z" {
		t.Fatal(e.Timestamp())
	}
	for _, bad := range []string{"20261332T000000.000Z", "20230229T000000.000Z", "00000101T000000.000Z",
		"20260101T240000.000Z", "20260101T000060.000Z", "2026-01-01", "20260101T000000.000"} {
		if _, err := ParseTimestamp(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestDefineErrors(t *testing.T) {
	w := must(Create(filepath.Join(t.TempDir(), "s.trak"), Options{}))
	defer w.Close()
	must(w.Define("a", "{}"))
	for name, def := range map[string]string{"a": "{}", "trak.x": "{}", "1x": "{}", "b": "{ x: foo }"} {
		if _, err := w.Define(name, def); err == nil {
			t.Errorf("Define(%q) succeeded", name)
		}
	}
	if len(w.Types()) != 1 {
		t.Fatal(w.Types())
	}
}

func TestCreateAndLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.trak")
	os.WriteFile(p, []byte("precious"), 0o644)
	if _, err := Create(p, Options{}); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	w := must(Create(p, Options{Overwrite: true, Sync: true}))
	if _, err := OpenAppend(p, Options{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("second writer: %v", err)
	}
	r := must(Open(p))
	r.Close()
	must(w.Define("a", "{}"))
	ok(t, w.Flush())
	w.Close()
	if _, err := w.Define("b", "{}"); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(p); st.Mode()&0o111 != 0 {
			t.Fatal("file is executable")
		}
	}
}

func TestAppendContinuesIdentically(t *testing.T) {
	dir := t.TempDir()
	whole := filepath.Join(dir, "whole.trak")
	writeExample(t, whole)
	data, _ := os.ReadFile(whole)
	split := filepath.Join(dir, "split.trak")
	// Cut after the first event, reopen, write the second.
	cut := bytes.Index(data, []byte("user: joel"))
	os.WriteFile(split, data[:cut], 0o644)
	w := must(OpenAppend(split, Options{}))
	must(w.WriteAt("build", map[string]any{
		"user": "joel", "status": "failed", "duration": 1520, "tags": []string{"ci", "nightly"},
		"stdout": "Compiling core...\nld: undefined symbol main", "error": "link failed",
	}, mustTime("20261107T140322.415Z")))
	w.Close()
	got, _ := os.ReadFile(split)
	if !bytes.Equal(got, data) {
		t.Fatal("append differs")
	}
}

func TestAppendMany(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.trak")
	w := must(Create(p, Options{}))
	for _, n := range "abcde" {
		must(w.Define(string(n), "{ i: num }"))
	}
	w.Close()
	for round := 0; round < 20; round++ {
		w := must(OpenAppend(p, Options{}))
		for i := 0; i < 37; i++ {
			must(w.Write(string("abcde"[(i*7+round)%5]), map[string]any{"i": i}))
		}
		if round == 10 {
			must(w.Define("late", "{}"))
			must(w.Write("late", map[string]any{}))
		}
		w.Close()
		must(Check(p))
	}
}

func TestTornTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.trak")
	good := specExample(t)
	complete := "==[ build 3 20261107T140322.415Z ]==fQBB\n"
	for _, tail := range []string{"user: x\nstat", "==[ build 3 2026", complete} {
		os.WriteFile(p, append(append([]byte(nil), good...), tail...), 0o644)
		r := must(Open(p))
		if r.End() != int64(len(good)) || must(must(r.Table("build")).Count()) != 2 {
			t.Fatal("recovery")
		}
		r.Close()
		_, err := Check(p)
		if (tail == complete) != (err != nil) {
			t.Fatalf("check with tail %q: %v", tail, err)
		}
		w := must(OpenAppend(p, Options{}))
		w.Close()
		if got, _ := os.ReadFile(p); !bytes.Equal(got, good) {
			t.Fatal("tail not truncated")
		}
	}
}

func TestEmptyAndMagicOnly(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.trak")
	must(Create(p, Options{})).Close()
	q := filepath.Join(dir, "empty.trak")
	os.WriteFile(q, nil, 0o644)
	for _, path := range []string{p, q} {
		w := must(OpenAppend(path, Options{}))
		must(w.Define("a", "{}"))
		w.Close()
	}
	a, _ := os.ReadFile(p)
	b, _ := os.ReadFile(q)
	if !bytes.Equal(a, b) {
		t.Fatal("differ")
	}
}

func TestRefresh(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.trak")
	w := must(Create(p, Options{}))
	defer w.Close()
	r := must(Open(p))
	defer r.Close()
	if changed := must(r.Refresh()); changed {
		t.Fatal("nothing changed")
	}
	must(w.Define("a", "{ i: num }"))
	must(w.Write("a", map[string]any{"i": 1}))
	if !must(r.Refresh()) {
		t.Fatal("refresh")
	}
	tab := must(r.Table("a"))
	must(w.Write("a", map[string]any{"i": 2}))
	must(r.Refresh())
	if n := must(tab.Count()); n != 2 {
		t.Fatal(n)
	}
}

func TestBadMagic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.trak")
	for data, msg := range map[string]string{"": "empty", "trac 1.0\n": "magic", "trak 1.0": "magic",
		"trak 01.0\n": "magic", "trak 2.0\n": "major"} {
		os.WriteFile(p, []byte(data), 0o644)
		_, err := Open(p)
		var fe *FormatError
		if !errors.As(err, &fe) || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: %v", data, err)
		}
	}
	os.WriteFile(p, []byte("trak 2.0\n"), 0o644)
	var uv *UnsupportedVersionError
	if _, err := Open(p); !errors.As(err, &uv) || uv.Version != "2" {
		t.Fatal(err)
	}
}

func TestUnparseableSchema(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.trak")
	w := must(Create(p, Options{}))
	must(w.Define("t", "{ a: str }"))
	must(w.Write("t", map[string]any{"a": "x"}))
	w.Close()
	data, _ := os.ReadFile(p)
	os.WriteFile(p, bytes.Replace(data, []byte("{ a: str }"), []byte("{ a: s?? }"), 1), 0o644)
	r := must(Open(p))
	tab := must(r.Table("t"))
	if n := must(tab.Count()); n != 1 || tab.Schema.Err == nil {
		t.Fatal("navigation")
	}
	var se *SchemaError
	if _, _, err := must(tab.Get(1)).Get("a"); !errors.As(err, &se) {
		t.Fatal(err)
	}
	r.Close()
	if _, err := Check(p); err == nil {
		t.Fatal("check passed")
	}
	w = must(OpenAppend(p, Options{}))
	defer w.Close()
	if _, err := w.Write("t", map[string]any{"a": "y"}); !errors.As(err, &se) {
		t.Fatal(err)
	}
}
