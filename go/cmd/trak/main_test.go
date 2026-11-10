package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/desx/trak/go"
)

const example = "../../../conformance/valid/001-spec-example.trak"

func runCLI(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestTypes(t *testing.T) {
	out, _, code := runCLI(t, "types", example)
	want := `INDEX  NAME    VERSION  PARENT  EVENTS  DESCRIPTION
1      build   1.0      -       2       One CI build run.
2      deploy  1.0      -       0       A release pushed to an environment.
`
	if code != 0 || out != want {
		t.Fatalf("%d\n%s", code, out)
	}
	out, _, _ = runCLI(t, "types", family)
	if !strings.Contains(out, "2      build.step  1.1      build   3       One step of a build.\n") {
		t.Fatal(out)
	}
}

const family = "../../../conformance/valid/026-family-basic.trak"

func TestCatFamily(t *testing.T) {
	out, _, code := runCLI(t, "cat", family, "build", "--family")
	want := `N  TYPE        TIMESTAMP             user  status  duration  tags.[]  stdout   error        step     ok
1  build       20261106T212000.001Z  ana   ok      910                ▸ empty
2  build.step  20261106T212000.002Z  ana   ok      10                 ▸ empty               compile  yes
3  build.step  20261106T212000.003Z  ana   ok      20                 ▸ empty               link     no
4  build       20261106T212000.004Z  joel  failed  1                  ▸ empty  link failed
5  build.step  20261106T212000.005Z  ana   ok      1                  ▸ empty               retry    yes
`
	if code != 0 || out != want {
		t.Fatalf("%d\n%s", code, out)
	}
	for args, rows := range map[string]int{
		"--last 2": 2, "--from 2 --to 4": 3, "--since 20261106T212000.004Z": 2, "--since 20300101T000000.000Z": 0,
	} {
		out, errOut, code := runCLI(t, append([]string{"cat", family, "build", "--family"}, strings.Fields(args)...)...)
		if code != 0 || strings.Count(out, "\n") != rows+1 {
			t.Errorf("%s: %d %s %s", args, code, out, errOut)
		}
	}
	out, _, _ = runCLI(t, "cat", family, "build.step", "--family", "--last", "1")
	if !strings.Contains(out, "\n3  build.step  20261106T212000.005Z") {
		t.Fatal(out)
	}
	out, _, _ = runCLI(t, "cat", "../../../conformance/valid/111-child-of-unparseable-parent.trak", "p", "--family")
	if strings.Count(out, "(values unavailable)") != 3 {
		t.Fatal(out)
	}
	if _, _, code := runCLI(t, "cat", family, "build", "--family=x"); code != 2 {
		t.Fatal(code)
	}
}

func TestSchema(t *testing.T) {
	out, _, code := runCLI(t, "schema", example, "build")
	for _, s := range []string{"build (type 1, version 1.0)", "// One CI build run.",
		"status    enum(ok, failed)\n", "stdout    str               block     @log         compiler output"} {
		if code != 0 || !strings.Contains(out, s) {
			t.Fatalf("missing %q in\n%s", s, out)
		}
	}
	if _, errOut, code := runCLI(t, "schema", example, "nope"); code != 1 || !strings.Contains(errOut, "nope") {
		t.Fatal(errOut)
	}
}

func TestCat(t *testing.T) {
	out, _, code := runCLI(t, "cat", example, "build")
	want := `N  TIMESTAMP             user  status  duration  tags.[]      stdout     error
1  20261107T140210.120Z  ana   ok      910       ci           ▸ 2 lines
2  20261107T140322.415Z  joel  failed  1520      ci, nightly  ▸ 2 lines  link failed
`
	if code != 0 || out != want {
		t.Fatalf("%d\n%s", code, out)
	}
	for args, rows := range map[string]int{
		"--last 1": 1, "--from 2": 1, "--to 1": 1, "--from=2 --to=1": 0,
		"--since 20261107T140300.000Z": 1, "--since 2026-11-07T14:00:00Z": 2, "--since 20300101T000000.000Z": 0,
	} {
		out, errOut, code := runCLI(t, append([]string{"cat", example, "build"}, strings.Fields(args)...)...)
		if code != 0 || strings.Count(out, "\n") != rows+1 {
			t.Errorf("%s: %d %s %s", args, code, out, errOut)
		}
	}
	out, _, _ = runCLI(t, "cat", "../../../conformance/valid/009-map-key-punctuation.trak", "t")
	if !strings.Contains(out, "a.b=1, c:d=2, e]f=3, g h=4,  ]===5, =6") {
		t.Fatal(out)
	}
	out, _, _ = runCLI(t, "cat", "../../../conformance/valid/106-unparseable-newer-schema.trak", "future")
	if !strings.Contains(out, "(values unavailable)") {
		t.Fatal(out)
	}
}

func TestExport(t *testing.T) {
	out, _, code := runCLI(t, "export", example, "--format", "jsonl")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 2 || !strings.HasPrefix(lines[1],
		`{"type":"build","n":2,"timestamp":"20261107T140322.415Z","families":{"build":2},"value":{"user":"joel","status":"failed"`) {
		t.Fatal(out)
	}
	out, _, _ = runCLI(t, "export", family, "build.step")
	if !strings.HasPrefix(out, `{"type":"build.step","n":1,"timestamp":"20261106T212000.002Z","families":{"build":2,"build.step":1},`) {
		t.Fatal(out)
	}
	out, _, _ = runCLI(t, "export", "../../../conformance/valid/020-keyless-map-entries.trak", "t")
	if !strings.Contains(out, `"m":[[null,[]],["full",["x"]],[null,[]]],"n":null`) {
		t.Fatal(out)
	}
	out, _, _ = runCLI(t, "export", example, "deploy")
	if out != "" {
		t.Fatal(out)
	}
	if _, _, code := runCLI(t, "export", example, "--format", "csv"); code != 2 {
		t.Fatal(code)
	}
}

func TestCheck(t *testing.T) {
	out, _, code := runCLI(t, "check", example)
	if code != 0 || !strings.Contains(out, "ok, 4 objects") {
		t.Fatal(out)
	}
	_, errOut, code := runCLI(t, "check", "../../../conformance/invalid/010-bad-num.trak")
	if code != 1 || !strings.Contains(errOut, "(at byte 42)") {
		t.Fatal(errOut)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{"bogus"}, {"types"}, {"cat", example, "build", "--from"},
		{"cat", example, "build", "--nope"}, {"cat", example, "build", "--last", "x"}, {"cat", example, "build", "--since", "x"}} {
		if _, errOut, code := runCLI(t, args...); code != 2 || !strings.Contains(errOut, "usage:") {
			t.Errorf("%v: %d %s", args, code, errOut)
		}
	}
	if out, _, code := runCLI(t, "--help"); code != 0 || !strings.Contains(out, "usage:") {
		t.Fatal(out)
	}
}

type syncBuffer struct {
	ch chan string
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.ch <- string(p)
	return len(p), nil
}

func TestTailFollow(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.trak")
	w, err := trak.Create(p, trak.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Define("a", "{ i: num }")
	w.Define("b", "{ s: |str }")
	for i := 0; i < 12; i++ {
		w.Write("a", map[string]any{"i": i})
	}
	out := &syncBuffer{ch: make(chan string, 100)}
	stop := make(chan struct{})
	done := make(chan error)
	a, _ := parseArgs([]string{"-f", p, "--interval", "10ms"}, tailFlags)
	go func() { done <- tail(out, a, stop) }()
	next := func() string {
		select {
		case s := <-out.ch:
			return s
		case <-time.After(5 * time.Second):
			t.Fatal("timed out")
			return ""
		}
	}
	if first := next(); !strings.HasPrefix(first, "a 3 ") {
		t.Fatalf("tail should start at the 10th-last event: %q", first)
	}
	for i := 0; i < 9; i++ {
		next()
	}
	w.Write("b", map[string]any{"s": "x\ny"})
	if got := next(); !strings.HasPrefix(got, "b 1 ") || !strings.HasSuffix(got, "s=▸ 2 lines\n") {
		t.Fatalf("%q", got)
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
