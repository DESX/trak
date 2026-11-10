package trak

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// File-order paging with cursors, write positions and directory sync.

const cursorStamp = "20261107T140000.000Z"

func pageAll(t testing.TB, r *Reader, forward bool, limit int) []tn {
	var out []tn
	cursor := int64(-1)
	for {
		var page []*Event
		if forward {
			page = must(r.After(cursor, limit))
		} else {
			page = must(r.Before(cursor, limit))
		}
		if len(page) == 0 {
			return out
		}
		if len(page) > limit {
			t.Fatalf("page of %d, limit %d", len(page), limit)
		}
		for _, e := range page {
			out = append(out, tn{e.Type(), e.N()})
		}
		cursor = page[len(page)-1].Offset()
	}
}

func mixed(t testing.TB, path string, n int, seed int64, types int) []Pos {
	rng := rand.New(rand.NewSource(seed))
	w := must(Create(path, Options{Overwrite: true}))
	defer w.Close()
	for k := 0; k < types; k++ {
		must(w.Define(fmt.Sprintf("t%d", k), "{ i: num }"))
	}
	var pos []Pos
	for i := 0; i < n; i++ {
		k := 0
		if rng.Float64() >= 0.6 {
			k = rng.Intn(types)
		}
		pos = append(pos, must(w.WriteAt(fmt.Sprintf("t%d", k), map[string]any{"i": i}, mustTime(cursorStamp))))
		if rng.Float64() < 0.01 {
			must(w.Define(fmt.Sprintf("late%d", i), "{}"))
		}
	}
	return pos
}

func scanAll(t testing.TB, r *Reader) []tn {
	var out []tn
	for _, e := range allEvents(t, r) {
		out = append(out, tn{e.Type(), e.N()})
	}
	return out
}

func reversed[T any](s []T) []T {
	var out []T
	if len(s) > 0 {
		out = make([]T, len(s))
	}
	for i, x := range s {
		out[len(s)-1-i] = x
	}
	return out
}

func TestPagesCoverEveryEventOnce(t *testing.T) {
	for _, n := range []int{0, 1, 2, 57} {
		for _, limit := range []int{1, 2, 5, 100} {
			p := filepath.Join(t.TempDir(), "m.trak")
			mixed(t, p, n, 5, 4)
			r := must(Open(p))
			all := scanAll(t, r)
			if got := pageAll(t, r, true, limit); !reflect.DeepEqual(got, all) {
				t.Errorf("n %d limit %d forward: %v", n, limit, got)
			}
			if got := pageAll(t, r, false, limit); !reflect.DeepEqual(got, reversed(all)) {
				t.Errorf("n %d limit %d backward: %v", n, limit, got)
			}
			r.Close()
		}
	}
}

func offsets(events []*Event) []int64 {
	out := make([]int64, len(events))
	for i, e := range events {
		out[i] = e.Offset()
	}
	return out
}

func TestWriteReturnsPosition(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.trak")
	pos := mixed(t, p, 40, 5, 4)
	r := must(Open(p))
	defer r.Close()
	for i, e := range allEvents(t, r) {
		if (Pos{e.N(), e.Offset()}) != pos[i] {
			t.Fatalf("event %d: %v, wrote %v", i, Pos{e.N(), e.Offset()}, pos[i])
		}
	}
	// A write position is a cursor.
	if got := must(r.After(pos[9].Offset, 1)); got[0].Offset() != pos[10].Offset {
		t.Fatal(got[0].Offset())
	}
	if got := must(r.Before(pos[10].Offset, 1)); got[0].Offset() != pos[9].Offset {
		t.Fatal(got[0].Offset())
	}
}

func TestCursorEdges(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.trak")
	pos := mixed(t, p, 10, 5, 4)
	r := must(Open(p))
	defer r.Close()
	if got := must(r.After(pos[len(pos)-1].Offset, 100)); len(got) != 0 {
		t.Fatal(got)
	}
	if got := must(r.Before(pos[0].Offset, 100)); len(got) != 0 {
		t.Fatal(got)
	}
	if got := must(r.After(-1, 0)); len(got) != 0 {
		t.Fatal(got)
	}
	if a, b := offsets(must(r.Before(r.End(), 2))), offsets(must(r.Before(-1, 2))); !reflect.DeepEqual(a, b) || len(a) != 2 {
		t.Fatal(a, b)
	}
	if got := must(r.After(int64(len("trak 1.1\n")), 100)); len(got) != 10 { // the magic line's end
		t.Fatal(len(got))
	}
	for _, bad := range []int64{3, pos[2].Offset + 1, r.End() + 1, r.Size() + 100} {
		if _, err := r.After(bad, 100); !errors.Is(err, ErrBadCursor) {
			t.Errorf("after %d: %v", bad, err)
		}
		if _, err := r.Before(bad, 100); !errors.Is(err, ErrBadCursor) {
			t.Errorf("before %d: %v", bad, err)
		}
	}
}

func TestForgedFooterIsNotACursor(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.trak")
	forged := "x\n==[ t 1 20261107T140000.000Z ]==BBBBBAB\n"
	w := must(Create(p, Options{}))
	must(w.Define("t", "{ out: |str }"))
	must(w.WriteAt("t", map[string]any{"out": forged}, mustTime(cursorStamp)))
	must(w.WriteAt("t", map[string]any{"out": "y"}, mustTime(cursorStamp)))
	ok(t, w.Close())
	data, _ := os.ReadFile(p)
	inside := int64(bytes.Index(data, []byte("]==BBBBBAB\n")) + len("]==BBBBBAB\n"))
	r := must(Open(p))
	defer r.Close()
	if _, err := r.After(inside, 100); !errors.Is(err, ErrBadCursor) {
		t.Fatal(err)
	}
	if got := must(r.After(-1, 100)); len(got) != 2 || got[0].N() != 1 || got[1].N() != 2 {
		t.Fatal(got)
	}
}

func TestCursorSurvivesRefreshAndTornTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.trak")
	pos := mixed(t, p, 20, 5, 4)
	r := must(Open(p))
	cursor := pos[len(pos)-1].Offset
	if got := must(r.After(cursor, 100)); len(got) != 0 {
		t.Fatal(got)
	}
	w := must(OpenAppend(p, Options{}))
	var more []int64
	for i := 0; i < 3; i++ {
		more = append(more, must(w.WriteAt("t1", map[string]any{"i": i}, mustTime(cursorStamp))).Offset)
	}
	ok(t, w.Close())
	if !must(r.Refresh()) {
		t.Fatal("refresh")
	}
	if got := offsets(must(r.After(cursor, 100))); !reflect.DeepEqual(got, more) {
		t.Fatal(got, more)
	}
	f := must(os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0))
	must(f.WriteString("i: 9\n==[ t1 9 2026"))
	ok(t, f.Close())
	if must(r.Refresh()) {
		t.Fatal("a torn tail changed nothing")
	}
	if got := offsets(must(r.After(cursor, 100))); !reflect.DeepEqual(got, more) {
		t.Fatal(got, more)
	}
	r.Close()
	// After truncation, a cursor past the new end is stale.
	ok(t, os.Truncate(p, more[1]))
	r = must(Open(p))
	defer r.Close()
	if _, err := r.After(more[2], 100); !errors.Is(err, ErrBadCursor) {
		t.Fatal(err)
	}
	if got := offsets(must(r.After(cursor, 100))); !reflect.DeepEqual(got, more[:1]) {
		t.Fatal(got)
	}
}

func TestForwardPageReadsAreBounded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.trak")
	pos := mixed(t, p, 20000, 5, 6)
	r := must(Open(p))
	defer r.Close()
	k := len(r.Types())
	for _, start := range []int{0, 5000, 19990} {
		clear(r.cache)
		clear(r.oldCache)
		clear(r.latest)
		before := r.FooterReads
		page := must(r.After(pos[start].Offset, 50))
		end := min(start+51, len(pos))
		for i, e := range page {
			if e.Offset() != pos[start+1+i].Offset {
				t.Fatalf("start %d: event %d", start, i)
			}
		}
		if len(page) != end-start-1 {
			t.Fatalf("start %d: %d events", start, len(page))
		}
		// Every type's latest node in one walk (~3K reads), a seek and a
		// navigation in each of the 6 types that have events (~6 log2 n
		// each), then the walk back over at most 6 * 50 objects, plus the
		// schemas among them.
		if got := r.FooterReads - before; got > 3*k+6*2*6*15+6*50+10 {
			t.Errorf("start %d: %d reads", start, got)
		}
	}
	clear(r.cache)
	clear(r.oldCache)
	before := r.FooterReads
	must(r.Before(pos[10000].Offset, 50))
	if got := r.FooterReads - before; got > 50+5 { // plus any schemas in between
		t.Errorf("before: %d reads", got)
	}
}

func TestSyncCreateSyncsDirectory(t *testing.T) {
	var synced []string
	syncDirFunc = func(path string) error {
		synced = append(synced, path)
		return syncDir(path)
	}
	defer func() { syncDirFunc = syncDir }()
	dir := t.TempDir()
	w := must(Create(filepath.Join(dir, "s.trak"), Options{Sync: true}))
	must(w.Define("t", "{}"))
	ok(t, w.Close())
	if len(synced) != 1 {
		t.Fatal(synced)
	}
	synced = nil
	w = must(Create(filepath.Join(dir, "n.trak"), Options{}))
	must(w.Define("t", "{}"))
	ok(t, w.Close())
	if len(synced) != 0 {
		t.Fatal(synced)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "s.trak")); !bytes.Equal(data, []byte("trak 1.1\n{}\n##[ t 1 1.0 ]##DAA\n")) {
		t.Fatal(string(data))
	}
}
