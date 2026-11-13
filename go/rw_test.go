package trak

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const ciSchema = `# A CI server.
root ci {
  name: str,
  builds: event build,
  config: {                    # settings
    retries: event num,
  },
}

# One build.
object build {
  user: str,                   # who started it
  status: event enum(running, ok, failed),
  log: |str,
  host: { name: str, cores: num? },
  parent: path?,
}
`

var t0 = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

func at(i int) time.Time { return t0.Add(time.Duration(i) * time.Second) }

func mustCreate(t testing.TB, policy Policy) (*Writer, string) {
	t.Helper()
	name := filepath.Join(t.TempDir(), "ci.trak")
	w, err := Create(name, ciSchema, map[string]any{"name": "ci-1"}, t0, &Options{Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w, name
}

func mustAppend(t testing.TB, w *Writer, list string, v any, i int) Append {
	t.Helper()
	a, err := w.Append(list, v, at(i))
	if err != nil {
		t.Fatalf("append %s: %v", list, err)
	}
	return a
}

func get(t testing.TB, f *File, path string, v uint64) any {
	t.Helper()
	x, err := f.Get(path, v)
	if err != nil {
		t.Fatalf("get %s at %d: %v", path, v, err)
	}
	return x
}

func str(t testing.TB, f *File, path string, v uint64) string {
	t.Helper()
	l, ok := get(t, f, path, v).(Leaf)
	if !ok {
		t.Fatalf("%s is not a leaf", path)
	}
	return l.String()
}

func build(i int) map[string]any {
	return map[string]any{"user": fmt.Sprintf("u%d", i), "log": fmt.Sprintf("line 1\n--[ log ]--\nbuild %d", i),
		"host": map[string]any{"name": "h", "cores": i}}
}

func TestRoundTrip(t *testing.T) {
	for _, policy := range []Policy{Reference, Delta} {
		t.Run(policy.String(), func(t *testing.T) {
			w, name := mustCreate(t, policy)
			n := 0
			for i := 0; i < 300; i++ {
				n++
				mustAppend(t, w, "builds", build(i), n)
				n++
				mustAppend(t, w, fmt.Sprintf("builds.%d.status", i), "running", n)
				if i%3 == 0 {
					n++
					mustAppend(t, w, fmt.Sprintf("builds.%d.status", i/2), "ok", n)
				}
				if i%50 == 0 {
					n++
					mustAppend(t, w, "config.retries", i, n)
				}
			}
			w.Close()
			f, err := Open(name)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if f.Version() != uint64(n+1) {
				t.Fatalf("version %d, want %d", f.Version(), n+1)
			}
			v := f.Version()
			if got := str(t, f, "name", v); got != "ci-1" {
				t.Errorf("name = %q", got)
			}
			l := get(t, f, "builds", v).(*List)
			if l.Len() != 300 {
				t.Fatalf("builds: %d", l.Len())
			}
			for i := 0; i < 300; i++ {
				if got := str(t, f, fmt.Sprintf("builds.%d.log", i), v); got != fmt.Sprintf("line 1\n--[ log ]--\nbuild %d", i) {
					t.Fatalf("build %d log = %q", i, got)
				}
				if got := str(t, f, fmt.Sprintf("builds.%d.host.cores", i), v); got != fmt.Sprint(i) {
					t.Fatalf("build %d cores = %q", i, got)
				}
			}
			if got := str(t, f, "builds.-1.user", v); got != "u299" {
				t.Errorf("last user %q", got)
			}
			st := get(t, f, "builds.11.status", v).(*List)
			if st.Len() != 1 {
				t.Errorf("builds.11.status: %d", st.Len())
			}
			st = get(t, f, "builds.12.status", v).(*List)
			if st.Len() != 2 || str(t, f, "builds.12.status.1", v) != "ok" {
				t.Errorf("builds.12.status: %d", st.Len())
			}
			// Old versions: at version 3 there was one build with one status.
			if l := get(t, f, "builds", 3).(*List); l.Len() != 1 {
				t.Errorf("builds at 3: %d", l.Len())
			}
			if l := get(t, f, "builds", 1).(*List); l.Len() != 0 {
				t.Errorf("builds at 1: %d", l.Len())
			}
			if got := str(t, f, "config.retries.-1", v); got != "250" {
				t.Errorf("retries = %q", got)
			}
			// Object versions: build 12 changed twice.
			b := get(t, f, "builds.12", v).(*Object)
			if k, _ := b.K(); k != 2 {
				t.Errorf("build 12 k = %d", k)
			}
			b0, err := b.Version(0)
			if err != nil {
				t.Fatal(err)
			}
			if l, _ := b0.List("status"); l.Len() != 0 {
				t.Errorf("build 12 at k=0 has %d statuses", l.Len())
			}
			cfg := get(t, f, "config", v).(*Object)
			k, _ := cfg.K()
			if k != 5 {
				t.Errorf("config k = %d", k)
			}
			for j := uint64(0); j <= k; j++ {
				c, err := cfg.Version(j)
				if err != nil {
					t.Fatal(err)
				}
				l, _ := c.List("retries")
				if l.Len() != j+1 {
					t.Errorf("config version %d has %d retries", j, l.Len())
				}
			}
			var seen []uint64
			if err := l.Range(5, 40, func(m uint64, item any) error {
				seen = append(seen, m)
				if item.(*Object).Path() != fmt.Sprintf("builds.%d", m) {
					t.Errorf("range item %d", m)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if len(seen) != 35 || seen[0] != 5 || seen[34] != 39 {
				t.Errorf("range: %v", seen)
			}
			if err := Check(name, nil); err != nil {
				t.Fatalf("check: %v", err)
			}
		})
	}
}

func TestDeltaIsSmaller(t *testing.T) {
	size := map[Policy]int64{}
	for _, policy := range []Policy{Reference, Delta} {
		w, name := mustCreate(t, policy)
		for i := 0; i < 2000; i++ {
			mustAppend(t, w, "builds", build(i), i)
			mustAppend(t, w, fmt.Sprintf("builds.%d.status", i), "ok", i)
		}
		w.Close()
		st, _ := os.Stat(name)
		size[policy] = st.Size()
	}
	if size[Delta] >= size[Reference] {
		t.Errorf("delta %d, reference %d", size[Delta], size[Reference])
	}
	t.Logf("reference %d bytes, delta %d bytes", size[Reference], size[Delta])
}

func TestTornTail(t *testing.T) {
	w, name := mustCreate(t, Reference)
	for i := 0; i < 20; i++ {
		mustAppend(t, w, "builds", build(i), i)
	}
	w.Close()
	good, _ := os.ReadFile(name)
	for _, tail := range []string{"user: x\n", "==[ builds.20 22 20261113T090000.000Z ]==BAB\n", "--[ log ]--\nbody\n==[ x"} {
		os.WriteFile(name, append(append([]byte(nil), good...), tail...), 0o644)
		f, err := Open(name)
		if err != nil {
			t.Fatal(err)
		}
		if f.Version() != 21 || f.Size() != int64(len(good)) {
			t.Errorf("tail %q: version %d size %d", tail, f.Version(), f.Size())
		}
		f.Close()
		w, err := OpenWriter(name, nil)
		if err != nil {
			t.Fatal(err)
		}
		mustAppend(t, w, "builds", build(99), 30)
		w.Close()
		if err := Check(name, nil); err != nil {
			t.Errorf("tail %q: check: %v", tail, err)
		}
	}
}

func TestValueErrors(t *testing.T) {
	w, _ := mustCreate(t, Reference)
	mustAppend(t, w, "builds", build(0), 1)
	bad := []struct {
		list string
		v    any
		want string
	}{
		{"builds", map[string]any{"user": "x"}, "log is missing"},
		{"builds", map[string]any{"user": "x", "log": "", "host": map[string]any{"name": "h", "cores": "1.5.2"}}, "not a num"},
		{"builds.0.status", "maybe", "not one of"},
		{"builds", map[string]any{"user": "x", "log": "", "host": map[string]any{"name": "h"}, "parent": "builds.7"}, "item 7"},
		{"builds", map[string]any{"user": "x", "log": "", "host": map[string]any{"name": "h"}, "nope": 1}, "no field nope"},
		{"builds.3.status", "ok", "no item 3"},
		{"name", "x", "holds no event lists"},
		{"builds", map[string]any{"user": "x", "log": "", "host": map[string]any{"name": "h"}, "status": "ok"}, "append its items separately"},
	}
	for _, c := range bad {
		_, err := w.Append(c.list, c.v, at(5))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: %v, want %q", c.list, c.v, err, c.want)
		}
	}
	if _, err := w.Append("builds.0.status", "ok", t0.Add(-time.Hour)); err == nil {
		t.Error("an earlier timestamp was accepted")
	}
	a := mustAppend(t, w, "builds", map[string]any{"user": "x", "log": "", "host": map[string]any{"name": "h"}, "parent": "builds.0"}, 6)
	if a.Version != 3 || a.Path != "builds.1" {
		t.Errorf("append = %+v", a)
	}
	var ve *ValueError
	if _, err := w.Append("builds", nil, at(7)); !errors.As(err, &ve) {
		t.Errorf("nil item: %v", err)
	}
}

func TestVersionAtTime(t *testing.T) {
	w, _ := mustCreate(t, Reference)
	for i := 0; i < 100; i++ {
		mustAppend(t, w, "builds", build(i), i/3*10)
	}
	f := w.File()
	for i := 0; i < 100; i++ {
		want := uint64(i/3*3) + 2 // the first append at that second
		if i < 3 {
			want = 1 // the root shares the first second
		}
		got, err := f.VersionAt(at(i / 3 * 10))
		if err != nil || got != want {
			t.Fatalf("time of %d: %d %v, want %d", i, got, err, want)
		}
	}
	if v, _ := f.VersionAt(at(10000)); v != 0 {
		t.Errorf("after the end: %d", v)
	}
	if v, _ := f.VersionAt(t0.Add(-time.Hour)); v != 1 {
		t.Errorf("before the start: %d", v)
	}
}
