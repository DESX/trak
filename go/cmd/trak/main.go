// Command trak inspects and serves trak files.
//
//	trak check FILE                 validate every append
//	trak stat FILE                  sizes and node counts
//	trak schema FILE                print the schema
//	trak get FILE PATH [--at V]     print a value, a list's count or an object's fields
//	trak serve DIR [--addr A] [--prefix /p]   the browser viewer
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	trak "github.com/desx/trak/go"
)

var errUsage = errors.New("usage")

const usage = `usage:
  trak check FILE
  trak stat FILE
  trak schema FILE
  trak get FILE PATH [--at VERSION]
  trak serve DIR [--addr 127.0.0.1:8765] [--prefix /path]`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type args struct {
	pos   []string
	flags map[string]string
}

func parseArgs(in []string) (args, error) {
	a := args{flags: map[string]string{}}
	for i := 0; i < len(in); i++ {
		if s := in[i]; strings.HasPrefix(s, "--") {
			name, val, ok := strings.Cut(s[2:], "=")
			if !ok {
				if i+1 >= len(in) {
					return a, fmt.Errorf("%w: --%s needs a value", errUsage, name)
				}
				i++
				val = in[i]
			}
			a.flags[name] = val
		} else {
			a.pos = append(a.pos, s)
		}
	}
	return a, nil
}

func run(argv []string, out, errOut io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprintln(errOut, usage)
		return 2
	}
	a, err := parseArgs(argv[1:])
	if err == nil {
		switch argv[0] {
		case "check":
			err = need(a, 1, func() error { return check(out, a.pos[0]) })
		case "stat":
			err = need(a, 1, func() error { return stat(out, a.pos[0]) })
		case "schema":
			err = need(a, 1, func() error { return printSchema(out, a.pos[0]) })
		case "get":
			err = need(a, 2, func() error { return get(out, a) })
		case "serve":
			err = need(a, 1, func() error { return serve(out, a, nil) })
		default:
			err = fmt.Errorf("%w: unknown command %q", errUsage, argv[0])
		}
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprintf(errOut, "trak: %v\n%s\n", err, usage)
		return 2
	}
	fmt.Fprintln(errOut, "trak:", err)
	return 1
}

func need(a args, n int, fn func() error) error {
	if len(a.pos) != n {
		return fmt.Errorf("%w: expected %d arguments", errUsage, n)
	}
	return fn()
}

func check(out io.Writer, name string) error {
	start := time.Now()
	rep, err := trak.CheckReport(name, &trak.CheckOptions{Progress: func(done, total uint64) {
		fmt.Fprintf(os.Stderr, "\r%d / %d versions", done, total)
	}})
	if rep != nil && rep.Versions >= 65536 {
		fmt.Fprintln(os.Stderr)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: ok, %d versions, %d bytes, checked in %.1fs\n", name, rep.Versions, rep.Size, time.Since(start).Seconds())
	return nil
}

func stat(out io.Writer, name string) error {
	rep, err := trak.CheckReport(name, nil)
	if rep == nil {
		return err
	}
	mean, p99, max := rep.FooterStats()
	fmt.Fprintf(out, "versions      %d\n", rep.Versions)
	fmt.Fprintf(out, "size          %d\n", rep.Size)
	fmt.Fprintf(out, "bodies        %d (%.1f%%)\n", rep.BodyBytes, 100*float64(rep.BodyBytes)/float64(rep.Size))
	fmt.Fprintf(out, "footers       %d (%.1f%%)\n", rep.FooterBytes, 100*float64(rep.FooterBytes)/float64(rep.Size))
	fmt.Fprintf(out, "footer bytes  mean %.0f, p99 %d, max %d\n", mean, p99, max)
	fmt.Fprintf(out, "nodes         %d full, %d delta\n", rep.FullNodes, rep.DeltaNodes)
	return err
}

func printSchema(out io.Writer, name string) error {
	f, err := trak.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.WriteString(out, f.Schema().Text)
	return err
}

func get(out io.Writer, a args) error {
	f, err := trak.Open(a.pos[0])
	if err != nil {
		return err
	}
	defer f.Close()
	v := f.Version()
	if s, ok := a.flags["at"]; ok {
		if v, err = strconv.ParseUint(s, 10, 64); err != nil {
			return fmt.Errorf("%w: bad version %q", errUsage, s)
		}
	}
	path := a.pos[1]
	if path == "." {
		path = ""
	}
	x, err := f.Get(path, v)
	if err != nil {
		return err
	}
	switch x := x.(type) {
	case trak.Leaf:
		if !x.Present {
			return fmt.Errorf("%s is absent", x.Path)
		}
		b, err := x.Bytes()
		if err != nil {
			return err
		}
		out.Write(b)
		if len(b) == 0 || b[len(b)-1] != '\n' {
			fmt.Fprintln(out)
		}
	case *trak.List:
		fmt.Fprintf(out, "%s: %d items\n", x.Path(), x.Len())
	case *trak.Object:
		return printObject(out, x)
	}
	return nil
}

func printObject(out io.Writer, o *trak.Object) error {
	leaves, err := o.Leaves()
	if err != nil {
		return err
	}
	for _, l := range leaves {
		if !l.Present {
			continue
		}
		b, err := l.Bytes()
		if err != nil {
			return err
		}
		s := string(b)
		if len(s) > 100 || strings.Contains(s, "\n") {
			first, _, _ := strings.Cut(s, "\n")
			if len(first) > 80 {
				first = first[:80]
			}
			s = fmt.Sprintf("%s… (%d bytes)", first, len(b))
		}
		fmt.Fprintf(out, "%s: %s\n", l.Path, s)
	}
	for _, fld := range o.Type().Slots() {
		x, err := o.Field(fld.Name)
		if err != nil {
			return err
		}
		switch x := x.(type) {
		case *trak.List:
			fmt.Fprintf(out, "%s: %d items\n", x.Path(), x.Len())
		case *trak.Object:
			fmt.Fprintf(out, "%s: object\n", x.Path())
		}
	}
	return nil
}
