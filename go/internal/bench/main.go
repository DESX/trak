// Command bench measures the Go library on the shared 1M-event workload
// described in bench/README.md.
//
//	go run ./internal/bench -file /tmp/bench.trak
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/buk/trak/go"
)

const base = 1_794_000_000_000 // 2026-11-06, in ms

var defs = [][2]string{
	{"build", "{ user: str, status: enum(ok, failed), duration: num, tags: [] str }"},
	{"deploy", "{ target: str, version: str }"},
	{"log", "{ msg: str }"},
	{"metric", "{ name: str, value: num }"},
}

func event(i int) (string, any) {
	switch i % 4 {
	case 0:
		status, tags := "ok", []any{"ci"}
		if i%3 == 0 {
			status = "failed"
		}
		if i%2 == 1 {
			tags = append(tags, "nightly")
		}
		return "build", map[string]any{"user": "user" + strconv.Itoa(i%100), "status": status, "duration": i % 5000, "tags": tags}
	case 1:
		target := "prod"
		if i%2 == 1 {
			target = "staging"
		}
		return "deploy", map[string]any{"target": target, "version": "1." + strconv.Itoa(i%50) + ".0"}
	case 2:
		return "log", map[string]any{"msg": "request " + strconv.Itoa(i) + " served"}
	}
	return "metric", map[string]any{"name": "latency", "value": i % 1000}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func main() {
	path := flag.String("file", "bench.trak", "file to write and read")
	n := flag.Int("n", 1_000_000, "events to write")
	flag.Parse()

	start := time.Now()
	w, err := trak.Create(*path, trak.Options{Overwrite: true})
	check(err)
	for _, d := range defs {
		_, err := w.Define(d[0], d[1])
		check(err)
	}
	for i := 0; i < *n; i++ {
		typ, v := event(i)
		_, err := w.WriteAt(typ, v, time.UnixMilli(base+int64(i)))
		check(err)
	}
	check(w.Close())
	write := time.Since(start)
	fmt.Printf("write              %8.0f events/s\n", float64(*n)/write.Seconds())

	const opens = 1000
	start = time.Now()
	for i := 0; i < opens; i++ {
		r, err := trak.Open(*path)
		check(err)
		t, _ := r.Table("build")
		_, err = t.Count()
		check(err)
		r.Close()
	}
	fmt.Printf("open + count()     %8.1f µs\n", float64(time.Since(start).Microseconds())/opens)

	r, err := trak.Open(*path)
	check(err)
	defer r.Close()
	t, _ := r.Table("build")
	count, _ := t.Count()
	rng := rand.New(rand.NewSource(1))
	const gets = 20000
	start = time.Now()
	for i := 0; i < gets; i++ {
		e, err := t.Get(1 + rng.Int63n(count))
		check(err)
		_, _, err = e.Get("user")
		check(err)
	}
	fmt.Printf("get(random n)      %8.1f µs\n", float64(time.Since(start).Nanoseconds())/gets/1000)

	const scans = 2000
	start = time.Now()
	for i := 0; i < scans; i++ {
		a := 1 + rng.Int63n(count-40)
		check(t.Range(a, a+39, func(e *trak.Event) bool {
			_, err := e.Leaves()
			check(err)
			return true
		}))
	}
	fmt.Printf("sequential 40 rows %8.1f µs\n", float64(time.Since(start).Nanoseconds())/scans/1000)
}
