package trak

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// FuzzRead feeds mutated files to the reader and check: they may fail, but must
// not panic or hang, and what check accepts must read back.
func FuzzRead(f *testing.F) {
	for _, dir := range []string{"valid", "invalid"} {
		names, _ := filepath.Glob(filepath.Join(vectorDir, dir, "*.trak"))
		for _, n := range names {
			b, _ := os.ReadFile(n)
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := NewFile(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return
		}
		v := r.Version()
		if v == 0 {
			return
		}
		if _, err := r.check(nil); err != nil {
			return
		}
		root, err := r.Root(v)
		if err != nil {
			t.Fatalf("checked file: root: %v", err)
		}
		if _, err := Dump(root); err != nil {
			t.Fatalf("checked file: dump: %v", err)
		}
	})
}

func TestFuzzSeeds(t *testing.T) {
	// The seeds themselves, without the fuzzing engine.
	names, _ := filepath.Glob(filepath.Join(vectorDir, "*", "*.trak"))
	if len(names) < 20 {
		t.Fatalf("only %d vectors", len(names))
	}
	for _, n := range names {
		b, _ := os.ReadFile(n)
		for cut := 0; cut < len(b); cut += 1 + len(b)/50 {
			NewFile(bytes.NewReader(b[:cut]), int64(cut)) // must not panic
		}
	}
}
