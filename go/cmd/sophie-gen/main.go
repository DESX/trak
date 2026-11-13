// Command sophie-gen writes a synthetic Sophie log (docs/sophie.md): hundreds
// of interleaved sessions with stand-in prompts, replies and outputs, and
// every event list of the schema exercised. Output depends only on the flags.
//
//	sophie-gen [--size 5GB] [--seed 1] [--policy reference|delta] OUT.trak
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	trak "github.com/desx/trak/go"
)

//go:embed sophie.schema
var schema string

func main() {
	size := flag.String("size", "100MB", "target size: about this many bytes with the reference policy")
	seed := flag.Uint64("seed", 1, "random seed")
	policy := flag.String("policy", "reference", "writer policy: reference or delta")
	quiet := flag.Bool("q", false, "no progress")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: sophie-gen [--size 5GB] [--seed 1] [--policy reference|delta] OUT.trak")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	target, err := parseSize(*size)
	if err != nil {
		fatal(err)
	}
	pol, err := trak.ParsePolicy(*policy)
	if err != nil {
		fatal(err)
	}
	var progress func(st Stats)
	if !*quiet {
		start := time.Now()
		progress = func(st Stats) {
			fmt.Fprintf(os.Stderr, "\r%6.0f MB  %8d appends  %6d sessions  %7d turns  %5.0fs",
				float64(st.Bytes)/1e6, st.Appends, st.Sessions, st.Turns, time.Since(start).Seconds())
		}
	}
	st, err := Generate(flag.Arg(0), Config{Size: target, Seed: *seed, Policy: pol, Progress: progress})
	if progress != nil {
		fmt.Fprintln(os.Stderr)
	}
	if err != nil {
		fatal(err)
	}
	fmt.Printf("%s: %d bytes, %d appends, %d sessions, %d turns (largest session %d), %d default changes, %.1f appends/s\n",
		flag.Arg(0), st.Bytes, st.Appends, st.Sessions, st.Turns, st.MaxTurns, st.Defaults, float64(st.Appends)/st.Elapsed.Seconds())
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "sophie-gen:", err)
	os.Exit(1)
}

// parseSize parses sizes like 5GB, 200MB, 64KB or 1000.
func parseSize(s string) (int64, error) {
	units := []struct {
		suffix string
		mult   int64
	}{{"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}, {"B", 1}}
	t := strings.ToUpper(strings.TrimSpace(s))
	for _, u := range units {
		if strings.HasSuffix(t, u.suffix) {
			f, err := strconv.ParseFloat(strings.TrimSuffix(t, u.suffix), 64)
			if err != nil || f <= 0 {
				return 0, fmt.Errorf("bad size %q", s)
			}
			return int64(f * float64(u.mult)), nil
		}
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return n, nil
}
