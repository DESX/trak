// Command bench compares writer policies on Sophie files (bench/README.md).
//
//	go run ./internal/bench [--write 300MB] [FILE.trak …]
//
// For each file: footer bytes per append, overhead, node forms, and the footer
// reads a cold lookup costs at the latest version and at old ones. --write also
// measures write throughput by generating a file of that size with each policy.
package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	trak "github.com/desx/trak/go"
	"github.com/desx/trak/go/internal/sophie"
)

func main() {
	write := flag.String("write", "", "also measure writes: generate this many bytes per policy (such as 300MB)")
	lookups := flag.Int("lookups", 400, "lookups per kind")
	flag.Parse()
	if *write != "" {
		size, err := strconv.ParseFloat(strings.TrimSuffix(strings.ToUpper(*write), "MB"), 64)
		if err != nil {
			fail(err)
		}
		dir, _ := os.MkdirTemp("", "trak-bench-")
		defer os.RemoveAll(dir)
		fmt.Println("| Policy | Appends | Seconds | Appends/s | MB/s |")
		fmt.Println("|---|---:|---:|---:|---:|")
		for _, p := range []trak.Policy{trak.Reference, trak.Delta} {
			name := filepath.Join(dir, p.String()+".trak")
			st, err := sophie.Generate(name, sophie.Config{Size: int64(size * 1e6), Seed: 1, Policy: p})
			if err != nil {
				fail(err)
			}
			s := st.Elapsed.Seconds()
			fmt.Printf("| %s | %d | %.1f | %.0f | %.0f |\n", p, st.Appends, s, float64(st.Appends)/s, float64(st.Bytes)/1e6/s)
			os.Remove(name)
		}
		fmt.Println()
	}
	if flag.NArg() == 0 {
		return
	}
	fmt.Println("| File | Size | Versions | Footer bytes per append (mean / p99 / max) | Footers | Nodes full / delta | Reads, latest (mean / max) | Reads, old (mean / max) | Lookup, cold (mean) |")
	fmt.Println("|---|---:|---:|---|---:|---|---|---|---:|")
	for _, name := range flag.Args() {
		rep, err := trak.CheckReport(name, nil)
		if err != nil {
			fail(fmt.Errorf("%s: %w", name, err))
		}
		mean, p99, max := rep.FooterStats()
		latest, old, ms := lookupsOf(name, *lookups)
		fmt.Printf("| %s | %.2f GB | %d | %.0f / %d / %d | %.1f%% | %d / %d | %s | %s | %.2f ms |\n",
			filepath.Base(name), float64(rep.Size)/1e9, rep.Versions, mean, p99, max,
			100*float64(rep.FooterBytes)/float64(rep.Size), rep.FullNodes, rep.DeltaNodes, latest, old, ms)
	}
}

// lookupsOf resolves random turn fields with an empty footer cache, at the latest
// version and at random old ones, and counts the footers each lookup reads.
func lookupsOf(name string, n int) (latest, old string, ms float64) {
	f, err := trak.Open(name)
	if err != nil {
		fail(err)
	}
	defer f.Close()
	r := rand.New(rand.NewPCG(1, 2))
	v := f.Version()
	measure := func(at func() (uint64, string)) string {
		var reads []int
		var total time.Duration
		for len(reads) < n {
			ver, path := at()
			if path == "" {
				continue
			}
			f.ForgetCache()
			before := f.Reads()
			t0 := time.Now()
			x, err := f.Get(path, ver)
			if err != nil {
				fail(fmt.Errorf("%s at %d: %w", path, ver, err))
			}
			if _, err := x.(trak.Leaf).Bytes(); err != nil {
				fail(err)
			}
			total += time.Since(t0)
			reads = append(reads, int(f.Reads()-before))
		}
		ms += float64(total.Microseconds()) / 1000 / float64(len(reads)) / 2
		sort.Ints(reads)
		sum := 0
		for _, x := range reads {
			sum += x
		}
		return fmt.Sprintf("%.1f / %d", float64(sum)/float64(len(reads)), reads[len(reads)-1])
	}
	turnAt := func(ver uint64) string {
		ss, _ := f.Get("sessions", ver)
		sessions := ss.(*trak.List)
		if sessions.Len() == 0 {
			return ""
		}
		s := r.Uint64N(sessions.Len())
		tl, _ := f.Get(fmt.Sprintf("sessions.%d.turns", s), ver)
		turns := tl.(*trak.List)
		if turns.Len() == 0 {
			return ""
		}
		return fmt.Sprintf("sessions.%d.turns.%d.reply", s, r.Uint64N(turns.Len()))
	}
	latest = measure(func() (uint64, string) { return v, turnAt(v) })
	old = measure(func() (uint64, string) {
		ver := 2 + r.Uint64N(v-2)
		return ver, turnAt(ver)
	})
	return latest, old, ms
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "bench:", err)
	os.Exit(1)
}
