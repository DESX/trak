package trak

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func FuzzSchemaParser(f *testing.F) {
	for _, s := range []string{buildSchemaText, "{ a: [] {} |str? @x(y) }", "// d\n{ e: enum(a, b,) }", "{}"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		root, _, err := ParseDefinition(text, false)
		if err == nil {
			flatten(root)
		}
	})
}

// FuzzReader opens arbitrary bytes and walks everything it finds. It must
// never panic or hang.
func FuzzReader(f *testing.F) {
	paths, _ := filepath.Glob("../conformance/*/*.trak")
	for _, p := range paths {
		if data, err := os.ReadFile(p); err == nil && len(data) < 4096 {
			f.Add(data)
		}
	}
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(dir, "f.trak")
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Skip()
		}
		Check(p)
		r, err := Open(p)
		if err != nil {
			return
		}
		defer r.Close()
		for _, s := range r.Types() {
			tab, _ := r.Table(s.Name)
			count, err := tab.Count()
			if err != nil {
				continue
			}
			for n := int64(1); n <= count && n < 50; n++ {
				if e, err := tab.Get(n); err == nil {
					e.Native()
					e.Leaves()
				}
			}
		}
		r.Events(func(e *Event) bool { e.Native(); e.FamilyNumber(""); e.Prev(); return true })
		for _, s := range r.Types() {
			fam, _ := r.Family(s.Name)
			if count, err := fam.Count(); err == nil && count > 0 {
				fam.Get(count)
				fam.Range(1, min(count, 50), func(*Event) bool { return true })
				fam.AtTime(time.UnixMilli(0))
			}
		}
		for _, forward := range []bool{true, false} {
			cursor := int64(-1)
			for i := 0; i < 20; i++ {
				var page []*Event
				if forward {
					page, err = r.After(cursor, 3)
				} else {
					page, err = r.Before(cursor, 3)
				}
				if err != nil || len(page) == 0 {
					break
				}
				cursor = page[len(page)-1].Offset()
			}
		}
	})
}
