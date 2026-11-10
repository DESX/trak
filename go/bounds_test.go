package trak

import (
	"fmt"
	"math"
	"math/rand"
	"path/filepath"
	"testing"
)

// TestNavigationBounds asserts the read-count bounds from the handover (§9)
// on a 1M-event file (100k with -short). n in 3·log2(n) + 4 is the number of
// events in the table.
func TestNavigationBounds(t *testing.T) {
	const k = 8
	n := 1_000_000
	if testing.Short() {
		n = 100_000
	}
	p := filepath.Join(t.TempDir(), "big.trak")
	w := must(Create(p, Options{}))
	for i := 0; i < k; i++ {
		must(w.Define(fmt.Sprintf("t%d", i), "{ i: num }"))
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < n; i++ {
		typ := 0
		if rng.Float64() >= 0.5 {
			typ = 1 + rng.Intn(k-2)
			if rng.Float64() < 0.001 {
				typ = k - 1 // rare
			}
		}
		must(w.Write(fmt.Sprintf("t%d", typ), map[string]any{"i": i}))
	}
	ok(t, w.Close())

	r := must(Open(p))
	defer r.Close()
	if r.FooterReads > 2*k+4 {
		t.Errorf("open read %d footers", r.FooterReads)
	}
	reads := func(fn func()) int {
		before := r.FooterReads
		fn()
		return r.FooterReads - before
	}
	total := int64(0)
	for _, s := range r.Types() {
		tab := must(r.Table(s.Name))
		clear(r.cache)
		clear(r.oldCache)
		clear(r.latest)
		if got := reads(func() { must(tab.Count()) }); got > 2*k+4 {
			t.Errorf("%s: count read %d footers", s.Name, got)
		}
		clear(r.cache)
		clear(r.oldCache)
		clear(r.latest)
		if got := reads(func() { must(tab.Latest()) }); got > 2*k+4 {
			t.Errorf("%s: latest read %d footers", s.Name, got)
		}
		count := must(tab.Count())
		total += count
		bound := int(3*math.Log2(float64(count)) + 4)
		for _, m := range append([]int64{1, 2, count / 2, count - 1, count}, randomNs(rng, count, 200)...) {
			if m < 1 || m > count {
				continue
			}
			clear(r.cache)
			clear(r.oldCache)
			if got := reads(func() { must(tab.Get(m)) }); got > bound {
				t.Errorf("%s: get(%d) read %d footers, bound %d", s.Name, m, got, bound)
			}
		}
	}
	if total != int64(n) {
		t.Fatalf("total %d", total)
	}

	// Seeks read at most two footers per step of the walk to their node.
	tab := must(r.Table("t0"))
	count := must(tab.Count())
	seekBound := int(2 * (3*math.Log2(float64(count)) + 4))
	for _, ts := range []string{"20000101T000000.000Z", "20991231T000000.000Z"} {
		clear(r.cache)
		clear(r.oldCache)
		if got := reads(func() { must(tab.AtTime(mustTime(ts))) }); got > seekBound {
			t.Errorf("at time %s read %d footers, bound %d", ts, got, seekBound)
		}
	}
	for _, m := range randomNs(rng, count, 50) {
		target := must(tab.Get(m)).Offset()
		clear(r.cache)
		clear(r.oldCache)
		var found *footer
		if got := reads(func() { found = must(r.seek(tab.Schema, startKey, target)) }); got > seekBound {
			t.Errorf("seek to %d read %d footers, bound %d", target, got, seekBound)
		}
		if found == nil || found.start != target {
			t.Fatalf("seek to %d found %v", target, found)
		}
	}
}

// TestFamilyRowBounds asserts that family row m costs O(k log n) reads.
func TestFamilyRowBounds(t *testing.T) {
	const kids = 5
	n := 250_000
	if testing.Short() {
		n = 25_000
	}
	p := filepath.Join(t.TempDir(), "family.trak")
	w := must(Create(p, Options{}))
	must(w.Define("root", "{ i: num }"))
	for k := 0; k < kids; k++ {
		must(w.Define(fmt.Sprintf("root.k%d", k), "{}"))
	}
	must(w.Define("other", "{ i: num }"))
	rng := rand.New(rand.NewSource(8))
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("root.k%d", rng.Intn(kids+2))
		switch name {
		case fmt.Sprintf("root.k%d", kids):
			name = "root"
		case fmt.Sprintf("root.k%d", kids+1):
			name = "other"
		}
		must(w.WriteAt(name, map[string]any{"i": i}, mustTime(fmt.Sprintf("20261107T%02d0000.000Z", i/3600000%24))))
	}
	ok(t, w.Close())

	r := must(Open(p))
	defer r.Close()
	f := must(r.Family("root"))
	rows := must(f.Count())
	k := len(f.Types())
	bound := int(float64(k) * 2 * (3*math.Log2(float64(rows)) + 4))
	for _, m := range append([]int64{1, rows}, randomNs(rng, rows, 100)...) {
		clear(r.cache)
		clear(r.oldCache)
		before := r.FooterReads
		must(f.Get(m))
		if got := r.FooterReads - before; got > bound {
			t.Errorf("family row %d read %d footers, bound %d", m, got, bound)
		}
	}
}

func randomNs(rng *rand.Rand, count int64, k int) []int64 {
	out := make([]int64, k)
	for i := range out {
		out[i] = 1 + rng.Int63n(count)
	}
	return out
}
