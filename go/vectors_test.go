package trak

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// The conformance vectors in ../conformance are written by this test:
//
//	go test -run TestVectors -update
//
// valid/NAME.trak has NAME.json: the tree dumped at some versions. invalid/NAME.trak
// has NAME.txt: what check must say about it, and "open: " first if opening it must
// fail.

var update = flag.Bool("update", false, "rewrite the conformance vectors")

const vectorDir = "../conformance"

type vector struct {
	name   string
	build  func(t *testing.T, path string) // writes the file
	tail   string                          // bytes appended after, a torn tail
	reason string                          // invalid: what check says
	patch  func(b []byte) []byte           // invalid: changes made after writing
	open   bool                            // invalid: Open must fail too
}

type writeFn func(w *Writer, n *int)

func writeWith(schema string, root map[string]any, policy Policy, steps writeFn) func(t *testing.T, path string) {
	return func(t *testing.T, path string) {
		w, err := Create(path, schema, root, t0, &Options{Policy: policy})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		steps(w, &n)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// app appends and panics on error: vectors are fixed programs.
func app(w *Writer, n *int, list string, v any) {
	*n++
	if _, err := w.Append(list, v, t0.Add(time.Duration(*n)*time.Second)); err != nil {
		panic(fmt.Sprintf("append %s: %v", list, err))
	}
}

const leafSchema = `# Lists of leaves.
root leaves {
  title: str,
  words: event str,
  notes: event |str,
  counts: event num,
  states: event enum(a, b, c),
  links: event path,
}
`

const nestedSchema = `# Nested objects with event lists.
root nest {
  config: {
    limits: { soft: event num, hard: event num },
    name: event str,
  },
  items: event item,
}

object item {
  label: str,
  meta: { owner: str, size: num? },
  extra: { why: |str }?,
  sub: { log: event str },
}
`

const deepSchema = `root deep {
  a: event a,
}

object a {
  x: num,
  b: event b,
}

object b {
  y: num,
  c: event enum(on, off),
}
`

const onlyLeaves = `# A root with only leaves.
root plain {
  name: str,
  note: |str?,
}
`

func vectors() []vector {
	ci := func(policy Policy) func(t *testing.T, path string) {
		return writeWith(ciSchema, map[string]any{"name": "ci-1"}, policy, func(w *Writer, n *int) {
			for i := 0; i < 4; i++ {
				app(w, n, "builds", build(i))
				app(w, n, fmt.Sprintf("builds.%d.status", i), "running")
			}
			app(w, n, "builds.1.status", "ok")
			app(w, n, "config.retries", 3)
			app(w, n, "builds", map[string]any{"user": "x", "log": "", "host": map[string]any{"name": "h"}, "parent": "builds.1"})
			app(w, n, "builds.3.status", "failed")
		})
	}
	growth := func(policy Policy) func(t *testing.T, path string) {
		return writeWith(leafSchema, map[string]any{"title": "growth"}, policy, func(w *Writer, n *int) {
			for i := 0; i < 300; i++ {
				app(w, n, "counts", i)
				if i%37 == 0 {
					app(w, n, "words", fmt.Sprintf("w%d", i))
				}
			}
		})
	}
	return []vector{
		{name: "valid/001-ci", build: ci(Reference)},
		{name: "valid/002-root-only", build: writeWith(onlyLeaves, map[string]any{"name": "just the root"}, Reference, func(*Writer, *int) {})},
		{name: "valid/003-leaf-lists", build: writeWith(leafSchema, map[string]any{"title": "leaves"}, Reference, func(w *Writer, n *int) {
			app(w, n, "words", "hello")
			app(w, n, "words", "")
			app(w, n, "notes", "line one\nline two\n")
			app(w, n, "notes", "--[ notes ]--\n==[ fake 9 20261113T090000.000Z ]==AAA\n--[ /notes ]--")
			app(w, n, "counts", "-1.5e-3")
			app(w, n, "counts", 0)
			app(w, n, "states", "c")
			app(w, n, "links", "words.1")
			app(w, n, "links", "notes")
			app(w, n, "links", "title")
		})},
		{name: "valid/004-nested", build: writeWith(nestedSchema, nil, Reference, func(w *Writer, n *int) {
			app(w, n, "config.limits.soft", 10)
			app(w, n, "config.name", "first")
			app(w, n, "items", map[string]any{"label": "a", "meta": map[string]any{"owner": "ann"}})
			app(w, n, "items", map[string]any{"label": "b", "meta": map[string]any{"owner": "bo", "size": 3}, "extra": map[string]any{"why": "because\nof this"}})
			app(w, n, "items.0.sub.log", "started")
			app(w, n, "config.limits.hard", 20)
			app(w, n, "items.0.sub.log", "done")
			app(w, n, "config.limits.soft", 12)
		})},
		{name: "valid/005-trie-growth", build: growth(Reference)},
		{name: "valid/006-deep", build: writeWith(deepSchema, nil, Reference, func(w *Writer, n *int) {
			for i := 0; i < 3; i++ {
				app(w, n, "a", map[string]any{"x": i})
				for j := 0; j < 3; j++ {
					app(w, n, fmt.Sprintf("a.%d.b", i), map[string]any{"y": j})
					app(w, n, fmt.Sprintf("a.%d.b.%d.c", i, j), "on")
				}
			}
			app(w, n, "a.0.b.1.c", "off")
			app(w, n, "a.2.b.0.c", "off")
		})},
		{name: "valid/007-delta", build: growth(Delta)},
		{name: "valid/008-ci-delta", build: ci(Delta)},
		{name: "valid/009-unicode", build: writeWith(leafSchema, map[string]any{"title": "ünïcödé ✓"}, Reference, func(w *Writer, n *int) {
			app(w, n, "words", "日本語")
			app(w, n, "notes", "emoji 🎉\r\nwindows line")
		})},
		{name: "valid/010-torn-tail", build: ci(Reference), tail: "user: someone\n--[ log ]--\nhalf a"},
		{name: "valid/011-same-time", build: func(t *testing.T, path string) {
			w, err := Create(path, leafSchema, map[string]any{"title": "now"}, t0, nil)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 40; i++ {
				if _, err := w.Append("counts", i, t0); err != nil {
					t.Fatal(err)
				}
			}
			w.Close()
		}},

		{name: "invalid/001-major-2", build: ci(Reference), open: true, reason: "unsupported", patch: func(b []byte) []byte { return bytes.Replace(b, []byte("trak 1.2"), []byte("trak 2.0"), 1) }},
		{name: "invalid/002-old-1-1", build: ci(Reference), open: true, reason: "unsupported", patch: func(b []byte) []byte { return bytes.Replace(b, []byte("trak 1.2"), []byte("trak 1.1"), 1) }},
		{name: "invalid/003-schema-syntax", build: ci(Reference), open: true, reason: "expected", patch: func(b []byte) []byte { return bytes.Replace(b, []byte("builds: event build,"), []byte("builds: evnt  build,"), 1) }},
		{name: "invalid/004-body-framing", build: ci(Reference), reason: "body: expected user:", patch: func(b []byte) []byte { return bytes.Replace(b, []byte("user: u1\n"), []byte("usr:  u1\n"), 1) }},
		{name: "invalid/005-bad-num", build: ci(Reference), reason: "not a num", patch: func(b []byte) []byte { return bytes.Replace(b, []byte("retries: 3\n"), []byte("retries: x\n"), 1) }},
		{name: "invalid/006-bad-enum", build: ci(Reference), reason: "not one of", patch: func(b []byte) []byte { return bytes.Replace(b, []byte("status: failed\n"), []byte("status: faied!\n"), 1) }},
		{name: "invalid/007-missing-path", build: ci(Reference), reason: "item 7", patch: func(b []byte) []byte { return bytes.Replace(b, []byte("parent: builds.1\n"), []byte("parent: builds.7\n"), 1) }},
		{name: "invalid/008-time-backwards", build: ci(Reference), reason: "before", patch: func(b []byte) []byte {
			return bytes.Replace(b, []byte(" 5 20261113T090004.000Z ]=="), []byte(" 5 20261113T080004.000Z ]=="), 1)
		}},
		{name: "invalid/009-version-gap", build: ci(Reference), reason: "follows version", patch: func(b []byte) []byte {
			return bytes.Replace(b, []byte(" 5 20261113T090004.000Z ]=="), []byte(" 6 20261113T090004.000Z ]=="), 1)
		}},
		{name: "invalid/010-not-a-path-copy", build: hooked(func(p *plan, _ *int64) {
			if joinSegs(p.segs) == "builds.2.status.0" {
				// builds' trie: item 0's entry, unchanged, written as item 1's.
				for i := range p.nodes {
					if p.nodes[i].kind == nodeTrie && len(p.nodes[i].entries) > 2 {
						p.nodes[i].entries[0] = p.nodes[i].entries[1]
						break
					}
				}
			}
		}), reason: "entry"},
		{name: "invalid/011-wrong-k", build: hooked(func(p *plan, _ *int64) {
			if joinSegs(p.segs) == "builds.1.status.1" {
				for i := range p.nodes {
					if p.nodes[i].kind == nodeItem {
						p.nodes[i].k += 2
					}
				}
			}
		}), reason: "prev or jump is wrong"},
		{name: "invalid/012-wrong-version-jump", build: hooked(func(p *plan, vjump *int64) {
			if joinSegs(p.segs) == "builds.3" {
				*vjump = 0
			}
		}), reason: "version jump"},
		{name: "invalid/013-new-item-not-last", build: ci(Reference), reason: "must be the last", patch: func(b []byte) []byte {
			return bytes.Replace(b, []byte("==[ builds.3 "), []byte("==[ builds.2 "), 1)
		}},
		{name: "invalid/014-torn-tail", build: ci(Reference), reason: "after the last valid append", patch: func(b []byte) []byte { return append(b, "user: x\n"...) }},
	}
}

func hooked(h func(p *plan, vjump *int64)) func(t *testing.T, path string) {
	return func(t *testing.T, path string) {
		w, err := Create(path, ciSchema, map[string]any{"name": "ci-1"}, t0, nil)
		if err != nil {
			t.Fatal(err)
		}
		w.hook = h
		n := 0
		for i := 0; i < 4; i++ {
			app(w, &n, "builds", build(i))
			app(w, &n, fmt.Sprintf("builds.%d.status", i), "running")
		}
		app(w, &n, "builds.1.status", "ok")
		w.Close()
	}
}

// dumps returns the tree at every version, or at about 12 of them.
func dumps(t *testing.T, f *File) map[string]any {
	out := map[string]any{}
	n := f.Version()
	step := uint64(1)
	if n > 12 {
		step = n / 12
	}
	vs := []uint64{}
	for v := uint64(1); v <= n; v += step {
		vs = append(vs, v)
	}
	if vs[len(vs)-1] != n {
		vs = append(vs, n)
	}
	for _, v := range vs {
		root, err := f.Root(v)
		if err != nil {
			t.Fatal(err)
		}
		d, err := Dump(root)
		if err != nil {
			t.Fatalf("dump at %d: %v", v, err)
		}
		out[fmt.Sprint(v)] = d
	}
	return out
}

func TestVectors(t *testing.T) {
	tmp := t.TempDir()
	seen := map[string]bool{}
	for _, vec := range vectors() {
		t.Run(vec.name, func(t *testing.T) {
			path := filepath.Join(tmp, strings.ReplaceAll(vec.name, "/", "-")+".trak")
			vec.build(t, path)
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if vec.patch != nil {
				nb := vec.patch(b)
				if bytes.Equal(nb, b) {
					t.Fatal("the patch changed nothing")
				}
				b = nb
			}
			b = append(b, vec.tail...)
			out := filepath.Join(vectorDir, vec.name+".trak")
			seen[out] = true
			if *update {
				os.MkdirAll(filepath.Dir(out), 0o755)
				if err := os.WriteFile(out, b, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := os.ReadFile(out)
			if err != nil || !bytes.Equal(got, b) {
				t.Fatalf("%s is stale or missing: run go test -run TestVectors -update", out)
			}
			if strings.HasPrefix(vec.name, "invalid/") {
				reason := vec.reason
				if vec.open {
					reason = "open: " + reason
				}
				txt := filepath.Join(vectorDir, vec.name+".txt")
				seen[txt] = true
				if *update {
					os.WriteFile(txt, []byte(reason+"\n"), 0o644)
				}
				if r, _ := os.ReadFile(txt); string(r) != reason+"\n" {
					t.Fatalf("%s is stale", txt)
				}
				_, oerr := Open(out)
				if vec.open != (oerr != nil) {
					t.Fatalf("open: %v", oerr)
				}
				err := Check(out, nil)
				if err == nil || !strings.Contains(err.Error(), vec.reason) {
					t.Fatalf("check: %v, want %q", err, vec.reason)
				}
				return
			}
			f, err := Open(out)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := Check(out, nil); (err != nil) != (vec.tail != "") {
				t.Fatalf("check: %v", err)
			}
			want := map[string]any{"version": f.Version(), "size": f.Size(), "dumps": dumps(t, f)}
			js, _ := json.MarshalIndent(want, "", " ")
			js = append(js, '\n')
			jf := filepath.Join(vectorDir, vec.name+".json")
			seen[jf] = true
			if *update {
				os.WriteFile(jf, js, 0o644)
			}
			if got, _ := os.ReadFile(jf); !bytes.Equal(got, js) {
				t.Fatalf("%s is stale", jf)
			}
		})
	}
	// No stray files.
	var stray []string
	for _, dir := range []string{"valid", "invalid"} {
		ents, _ := os.ReadDir(filepath.Join(vectorDir, dir))
		for _, e := range ents {
			if p := filepath.Join(vectorDir, dir, e.Name()); !seen[p] {
				if *update {
					os.Remove(p)
				} else {
					stray = append(stray, p)
				}
			}
		}
	}
	sort.Strings(stray)
	if len(stray) > 0 {
		t.Errorf("stray vector files: %v", stray)
	}
}
