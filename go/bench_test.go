package trak

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func BenchmarkWriteSmall(b *testing.B) {
	p := filepath.Join(b.TempDir(), "w.trak")
	w, _ := Create(p, Options{})
	w.Define("build", "{ user: str, status: enum(ok, failed), duration: num, tags: [] str }")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.WriteAt("build", map[string]any{"user": "user" + strconv.Itoa(i%100), "status": "ok", "duration": i % 5000, "tags": []any{"ci"}}, time.UnixMilli(1_794_000_000_000+int64(i)))
	}
	w.Close()
}

func BenchmarkScan40(b *testing.B) {
	p := filepath.Join(b.TempDir(), "w.trak")
	w, _ := Create(p, Options{})
	w.Define("build", "{ user: str, status: enum(ok, failed), duration: num, tags: [] str }")
	for i := 0; i < 100000; i++ {
		w.WriteAt("build", map[string]any{"user": "user" + strconv.Itoa(i%100), "status": "ok", "duration": i % 5000, "tags": []any{"ci"}}, time.UnixMilli(1_794_000_000_000+int64(i)))
	}
	w.Close()
	r, _ := Open(p)
	t, _ := r.Table("build")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a := int64(1 + (i*7919)%99000)
		t.Range(a, a+39, func(e *Event) bool { e.Leaves(); return true })
	}
}

func BenchmarkScan40Big(b *testing.B) {
	r, err := Open(os.Getenv("BENCH_FILE"))
	if err != nil {
		b.Skip(err)
	}
	t, _ := r.Table("build")
	count, _ := t.Count()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a := int64(1 + (int64(i)*7919)%(count-40))
		t.Range(a, a+39, func(e *Event) bool { e.Leaves(); return true })
	}
}
