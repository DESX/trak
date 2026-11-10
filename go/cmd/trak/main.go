// Command trak inspects trak files.
//
//	trak types FILE
//	trak schema FILE TYPE
//	trak cat FILE TYPE [--family] [--from n] [--to n] [--last k] [--since ts]
//	trak export FILE [TYPE] [--format jsonl]
//	trak tail -f FILE [TYPE] [--format jsonl]
//	trak check FILE
//	trak serve DIR [--addr host:port] [--prefix /path]
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/desx/trak/go"
)

const usage = `usage:
  trak types FILE                       list types with counts and descriptions
  trak schema FILE TYPE                 print a type's definition and fields
  trak cat FILE TYPE [--family] [--from n] [--to n] [--last k] [--since ts]
                                        print events as an aligned table; with
                                        --family, the type and its descendants
  trak export FILE [TYPE] [--format jsonl]
                                        print one JSON object per event
  trak tail -f FILE [TYPE] [--format jsonl]
                                        follow a growing file
  trak check FILE                       validate a whole file
  trak serve DIR [--addr 127.0.0.1:8765] [--prefix /path]
                                        browse the .trak files under DIR in a
                                        web viewer; files are read-only
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// errUsage marks errors that should print the usage text.
var errUsage = errors.New("usage")

type args struct {
	pos   []string
	flags map[string]string
}

// parseArgs separates --name value, --name=value and bare --name flags from
// positional arguments, which may come in any order. flags maps each known
// flag name to whether it takes a value.
func parseArgs(in []string, flags map[string]bool) (args, error) {
	a := args{flags: map[string]string{}}
	for i := 0; i < len(in); i++ {
		s := in[i]
		if !strings.HasPrefix(s, "-") || s == "-" {
			a.pos = append(a.pos, s)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(s, "-"), "=")
		valued, known := flags[name]
		switch {
		case !known:
			return a, fmt.Errorf("%w: unknown flag %s", errUsage, s)
		case valued && !hasValue:
			if i+1 >= len(in) {
				return a, fmt.Errorf("%w: --%s needs a value", errUsage, name)
			}
			i++
			value = in[i]
		case !valued && hasValue:
			return a, fmt.Errorf("%w: --%s takes no value", errUsage, name)
		}
		a.flags[name] = value
	}
	return a, nil
}

func run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 || argv[0] == "-h" || argv[0] == "--help" || argv[0] == "help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	cmd, rest := argv[0], argv[1:]
	var err error
	switch cmd {
	case "types":
		err = withPositional(rest, 1, 1, nil, func(a args) error { return types(stdout, a.pos[0]) })
	case "schema":
		err = withPositional(rest, 2, 2, nil, func(a args) error { return schema(stdout, a.pos[0], a.pos[1]) })
	case "cat":
		err = withPositional(rest, 2, 2,
			map[string]bool{"from": true, "to": true, "last": true, "since": true, "family": false},
			func(a args) error { return cat(stdout, a) })
	case "export":
		err = withPositional(rest, 1, 2, map[string]bool{"format": true}, func(a args) error { return export(stdout, a) })
	case "tail":
		err = withPositional(rest, 1, 2, tailFlags, func(a args) error { return tail(stdout, a, nil) })
	case "serve":
		err = withPositional(rest, 1, 1, map[string]bool{"addr": true, "prefix": true},
			func(a args) error { return serve(stdout, a, nil) })
	case "check":
		var n int
		err = withPositional(rest, 1, 1, nil, func(a args) error {
			n, err = trak.Check(a.pos[0])
			if err == nil {
				fmt.Fprintf(stdout, "%s: ok, %d objects\n", a.pos[0], n)
			}
			return err
		})
		if err != nil && !errors.Is(err, errUsage) {
			fmt.Fprintf(stderr, "%s: %v\n", rest[len(rest)-1], err)
			return 1
		}
	default:
		err = fmt.Errorf("%w: unknown command %q", errUsage, cmd)
	}
	if errors.Is(err, errUsage) {
		fmt.Fprintf(stderr, "trak: %v\n%s", strings.TrimPrefix(err.Error(), "usage: "), usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "trak: %v\n", err)
		return 1
	}
	return 0
}

var tailFlags = map[string]bool{"format": true, "interval": true, "f": false, "follow": false}

func withPositional(argv []string, min, max int, valued map[string]bool, fn func(args) error) error {
	a, err := parseArgs(argv, valued)
	if err != nil {
		return err
	}
	if len(a.pos) < min || len(a.pos) > max {
		return fmt.Errorf("%w: wrong number of arguments", errUsage)
	}
	return fn(a)
}

// table returns a tabwriter whose lines are written to out without trailing
// spaces; call flush when done.
func table(out io.Writer) (*tabwriter.Writer, func() error) {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	return tw, func() error {
		if err := tw.Flush(); err != nil {
			return err
		}
		for _, line := range strings.SplitAfter(buf.String(), "\n") {
			if trimmed := strings.TrimRight(line, " \n"); line != "" {
				if _, err := io.WriteString(out, trimmed+"\n"); err != nil {
					return err
				}
			}
		}
		return nil
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func types(out io.Writer, path string) error {
	r, err := trak.Open(path)
	if err != nil {
		return err
	}
	defer r.Close()
	tw, flush := table(out)
	fmt.Fprintln(tw, "INDEX\tNAME\tVERSION\tPARENT\tEVENTS\tDESCRIPTION")
	for _, s := range r.Types() {
		t, _ := r.Table(s.Name)
		n, err := t.Count()
		if err != nil {
			return err
		}
		parent := s.Parent
		if parent == "" {
			parent = "-"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%d\t%s\n", s.Index, s.Name, s.Version, parent, n, firstLine(s.Description))
	}
	return flush()
}

func leafType(f trak.Field) string {
	switch {
	case f.Unknown != "":
		return f.Unknown + " (as str)"
	case f.Type == "enum":
		return "enum(" + strings.Join(f.Enum, ", ") + ")"
	}
	return f.Type
}

func schema(out io.Writer, path, name string) error {
	r, err := trak.Open(path)
	if err != nil {
		return err
	}
	defer r.Close()
	s, err := r.Schema(name)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s (type %d, version %s)\n\n%s\n", s.Name, s.Index, s.Version, s.Text)
	if s.Root == nil {
		fmt.Fprintf(out, "values unavailable: %v\n", s.Err)
		return nil
	}
	tw, flush := table(out)
	fmt.Fprintln(tw, "PATH\tTYPE\tFLAGS\tANNOTATIONS\tDESCRIPTION")
	for _, f := range s.Fields {
		var flags, anns []string
		if f.Block {
			flags = append(flags, "block")
		}
		if f.Optional {
			flags = append(flags, "optional")
		}
		for _, a := range f.Annotations {
			anns = append(anns, a.String())
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", f.Path, leafType(f), strings.Join(flags, ","),
			strings.Join(anns, " "), f.Description)
	}
	return flush()
}

func parseTime(s string) (time.Time, error) {
	if t, err := trak.ParseTimestamp(s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%w: bad time %q (use YYYYMMDDTHHMMSS.sssZ or RFC 3339)", errUsage, s)
}

func intFlag(a args, name string) (int64, bool, error) {
	v, ok := a.flags[name]
	if !ok {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, false, fmt.Errorf("%w: --%s needs a non-negative number", errUsage, name)
	}
	return n, true, nil
}

// rows works out the row range [a, b] that cat should print, given the row
// count and a seek by time that returns a row number (0 for none).
func rows(count int64, atTime func(time.Time) (int64, error), a args) (int64, int64, error) {
	lo, hi := int64(1), count
	if v, ok, err := intFlag(a, "from"); err != nil {
		return 0, 0, err
	} else if ok {
		lo = max(lo, v)
	}
	if v, ok, err := intFlag(a, "to"); err != nil {
		return 0, 0, err
	} else if ok {
		hi = min(hi, v)
	}
	if since, ok := a.flags["since"]; ok {
		ts, err := parseTime(since)
		if err != nil {
			return 0, 0, err
		}
		m, err := atTime(ts)
		if err != nil {
			return 0, 0, err
		}
		if m == 0 {
			return 1, 0, nil
		}
		lo = max(lo, m)
	}
	if v, ok, err := intFlag(a, "last"); err != nil {
		return 0, 0, err
	} else if ok {
		lo = max(lo, hi-v+1)
	}
	return lo, hi, nil
}

func cat(out io.Writer, a args) error {
	r, err := trak.Open(a.pos[0])
	if err != nil {
		return err
	}
	defer r.Close()
	if _, ok := a.flags["family"]; ok {
		return catFamily(out, r, a)
	}
	t, err := r.Table(a.pos[1])
	if err != nil {
		return err
	}
	count, err := t.Count()
	if err != nil {
		return err
	}
	lo, hi, err := rows(count, func(ts time.Time) (int64, error) {
		e, err := t.AtTime(ts)
		if e == nil || err != nil {
			return 0, err
		}
		return e.N(), nil
	}, a)
	if err != nil {
		return err
	}
	s := t.Schema
	tw, flush := table(out)
	header := []string{"N", "TIMESTAMP"}
	for _, f := range s.Fields {
		header = append(header, f.Path)
	}
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	if lo <= hi {
		err = t.Range(lo, hi, func(e *trak.Event) bool {
			row := []string{strconv.FormatInt(e.N(), 10), e.Timestamp()}
			if s.Root == nil {
				row = append(row, "(values unavailable)")
			} else {
				obj, werr := e.Native()
				if werr != nil {
					err = werr
					return false
				}
				row = append(row, cells(s.Root, obj, len(s.Fields))...)
			}
			fmt.Fprintln(tw, strings.Join(row, "\t"))
			return true
		})
		if err != nil {
			return err
		}
	}
	return flush()
}

// catFamily prints a family table (SPEC §12): its rows in file order, with
// the family type's leaf columns and each descendant's own leaves added.
func catFamily(out io.Writer, r *trak.Reader, a args) error {
	fam, err := r.Family(a.pos[1])
	if err != nil {
		return err
	}
	count, err := fam.Count()
	if err != nil {
		return err
	}
	lo, hi, err := rows(count, func(ts time.Time) (int64, error) {
		e, err := fam.AtTime(ts)
		if e == nil || err != nil {
			return 0, err
		}
		return e.FamilyNumber(fam.Name())
	}, a)
	if err != nil {
		return err
	}
	// Columns by path, in order of first appearance. A child's fields start
	// with its parent's, so only its own leaves add columns.
	var paths []string
	column := map[string]int{}
	columns := map[string][]int{} // per type: each field's column
	for _, s := range fam.Types() {
		cols := make([]int, len(s.Fields))
		for i, f := range s.Fields {
			c, ok := column[f.Path]
			if !ok {
				c = len(paths)
				column[f.Path] = c
				paths = append(paths, f.Path)
			}
			cols[i] = c
		}
		columns[s.Name] = cols
	}
	tw, flush := table(out)
	fmt.Fprintln(tw, strings.Join(append([]string{"N", "TYPE", "TIMESTAMP"}, paths...), "\t"))
	if lo <= hi {
		err = fam.Range(lo, hi, func(e *trak.Event) bool {
			m, ferr := e.FamilyNumber(fam.Name())
			if ferr != nil {
				err = ferr
				return false
			}
			row := []string{strconv.FormatInt(m, 10), e.Type(), e.Timestamp()}
			if s := e.Schema; s.Root == nil {
				row = append(row, "(values unavailable)")
			} else {
				obj, werr := e.Native()
				if werr != nil {
					err = werr
					return false
				}
				vals := make([]string, len(paths))
				for i, v := range cells(s.Root, obj, len(s.Fields)) {
					vals[columns[s.Name][i]] = v
				}
				row = append(row, vals...)
			}
			fmt.Fprintln(tw, strings.Join(row, "\t"))
			return true
		})
		if err != nil {
			return err
		}
	}
	return flush()
}

// leafCount is the number of leaf columns under t.
func leafCount(t *trak.Type) int {
	switch t.Kind {
	case trak.KindObject:
		n := 0
		for _, m := range t.Members {
			n += leafCount(m.Type)
		}
		return n
	case trak.KindList, trak.KindMap:
		return leafCount(t.Elem)
	}
	return 1
}

// cells renders one table row: every value of each leaf column, joined.
func cells(root *trak.Type, obj *trak.Object, n int) []string {
	cols := make([][]string, n)
	var walk func(t *trak.Type, v any, col int, prefix string)
	walk = func(t *trak.Type, v any, col int, prefix string) {
		if v == nil {
			return
		}
		switch t.Kind {
		case trak.KindObject:
			o := v.(*trak.Object)
			for i, m := range t.Members {
				walk(m.Type, o.Values[i], col, prefix)
				col += leafCount(m.Type)
			}
		case trak.KindList:
			for _, item := range v.([]any) {
				walk(t.Elem, item, col, prefix)
			}
		case trak.KindMap:
			for _, e := range v.([]trak.MapEntry) {
				key := "?"
				if e.Known {
					key = e.Key
				}
				walk(t.Elem, e.Value, col, prefix+cell(key, 20)+"=")
			}
		default:
			text := v.(string)
			if t.Block {
				text = blockCell(text)
			} else {
				text = cell(text, 40)
			}
			cols[col] = append(cols[col], prefix+text)
		}
	}
	walk(root, obj, 0, "")
	out := make([]string, n)
	for i, c := range cols {
		out[i] = strings.Join(c, ", ")
	}
	return out
}

func blockCell(v string) string {
	if v == "" {
		return "▸ empty"
	}
	lines := strings.Count(strings.TrimSuffix(v, "\n"), "\n") + 1
	if lines == 1 {
		return "▸ 1 line"
	}
	return fmt.Sprintf("▸ %d lines", lines)
}

// cell makes a value safe for one table cell: control characters are
// escaped and long values are cut.
func cell(v string, limit int) string {
	var b strings.Builder
	n := 0
	for _, r := range v {
		if n == limit {
			b.WriteString("…")
			break
		}
		switch {
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == utf8.RuneError || r < 0x20 || r == 0x7f:
			b.WriteString(strconv.QuoteRune(r)[1 : len(strconv.QuoteRune(r))-1])
		default:
			b.WriteRune(r)
		}
		n++
	}
	return b.String()
}

// appendValue writes a Native tree as JSON, keeping object members in
// schema order. Maps are [key, value] pairs; an unknown key is null.
func appendValue(dst []byte, v any) []byte {
	switch v := v.(type) {
	case nil:
		return append(dst, "null"...)
	case string:
		b, _ := json.Marshal(v)
		return append(dst, b...)
	case *trak.Object:
		dst = append(dst, '{')
		for i, k := range v.Keys {
			if i > 0 {
				dst = append(dst, ',')
			}
			b, _ := json.Marshal(k)
			dst = append(append(dst, b...), ':')
			dst = appendValue(dst, v.Values[i])
		}
		return append(dst, '}')
	case []any:
		dst = append(dst, '[')
		for i, x := range v {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendValue(dst, x)
		}
		return append(dst, ']')
	case []trak.MapEntry:
		dst = append(dst, '[')
		for i, e := range v {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = append(dst, '[')
			if e.Known {
				dst = appendValue(dst, e.Key)
			} else {
				dst = append(dst, "null"...)
			}
			dst = appendValue(append(dst, ','), e.Value)
			dst = append(dst, ']')
		}
		return append(dst, ']')
	}
	panic(fmt.Sprintf("unexpected value %T", v))
}

func eventJSON(e *trak.Event) ([]byte, error) {
	name, _ := json.Marshal(e.Type())
	dst := fmt.Appendf(nil, `{"type":%s,"n":%d,"timestamp":"%s","families":{`, name, e.N(), e.Timestamp())
	for i, a := range e.Schema.Lineage {
		num, err := e.FamilyNumber(a)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			dst = append(dst, ',')
		}
		b, _ := json.Marshal(a)
		dst = fmt.Appendf(dst, "%s:%d", b, num)
	}
	dst = append(dst, `},"value":`...)
	if e.Schema.Root == nil {
		dst = append(dst, "null"...)
	} else {
		obj, err := e.Native()
		if err != nil {
			return nil, err
		}
		dst = appendValue(dst, obj)
	}
	return append(dst, "}\n"...), nil
}

func eventLine(e *trak.Event) ([]byte, error) {
	dst := fmt.Appendf(nil, "%s %d %s", e.Type(), e.N(), e.Timestamp())
	if e.Schema.Root == nil {
		return append(dst, " (values unavailable)\n"...), nil
	}
	leaves, err := e.Leaves()
	if err != nil {
		return nil, err
	}
	for _, l := range leaves {
		v := cell(string(l.Value), 60)
		if l.Type.Block {
			v = blockCell(string(l.Value))
		}
		dst = fmt.Appendf(dst, " %s=%s", cell(l.Path, 60), v)
	}
	return append(dst, '\n'), nil
}

func formatter(a args) (func(*trak.Event) ([]byte, error), error) {
	switch f, ok := a.flags["format"]; {
	case !ok:
		return nil, nil
	case f == "jsonl":
		return eventJSON, nil
	default:
		return nil, fmt.Errorf("%w: unknown format %q (only jsonl)", errUsage, f)
	}
}

func export(out io.Writer, a args) error {
	format, err := formatter(a)
	if err != nil {
		return err
	}
	if format == nil {
		format = eventJSON
	}
	r, err := trak.Open(a.pos[0])
	if err != nil {
		return err
	}
	defer r.Close()
	emit := func(e *trak.Event) bool {
		var line []byte
		if line, err = format(e); err == nil {
			_, err = out.Write(line)
		}
		return err == nil
	}
	if len(a.pos) == 1 {
		if ierr := r.Events(emit); ierr != nil {
			return ierr
		}
		return err
	}
	t, err := r.Table(a.pos[1])
	if err != nil {
		return err
	}
	count, err := t.Count()
	if err != nil || count == 0 {
		return err
	}
	if ierr := t.Range(1, count, emit); ierr != nil {
		return ierr
	}
	return err
}

// tail prints the last 10 events, then follows the file. stop, when not nil,
// ends the loop (for tests).
func tail(out io.Writer, a args, stop <-chan struct{}) error {
	_, follow := a.flags["f"]
	if _, ok := a.flags["follow"]; ok {
		follow = true
	}
	format, err := formatter(a)
	if err != nil {
		return err
	}
	if format == nil {
		format = eventLine
	}
	interval := 250 * time.Millisecond
	if v, ok := a.flags["interval"]; ok {
		if interval, err = time.ParseDuration(v); err != nil {
			return fmt.Errorf("%w: bad --interval", errUsage)
		}
	}
	r, err := trak.Open(a.pos[0])
	if err != nil {
		return err
	}
	defer r.Close()
	want := ""
	if len(a.pos) == 2 {
		if _, err := r.Schema(a.pos[1]); err != nil {
			return err
		}
		want = a.pos[1]
	}
	// print writes matching events with offsets >= from, at most limit of them.
	print := func(from int64, limit int) error {
		var batch []*trak.Event
		err := r.EventsBackward(func(e *trak.Event) bool {
			if e.Offset() < from || (limit > 0 && len(batch) == limit) {
				return false
			}
			if want == "" || e.Type() == want {
				batch = append(batch, e)
			}
			return true
		})
		if err != nil {
			return err
		}
		for i := len(batch) - 1; i >= 0; i-- {
			line, err := format(batch[i])
			if err != nil {
				return err
			}
			if _, err := out.Write(line); err != nil {
				return err
			}
		}
		return nil
	}
	if err := print(0, 10); err != nil {
		return err
	}
	for follow {
		from := r.End()
		select {
		case <-stop:
			return nil
		case <-time.After(interval):
		}
		changed, err := r.Refresh()
		if err != nil {
			return err
		}
		if changed {
			if r.End() < from {
				from = 0 // the file was truncated and rewritten
			}
			if err := print(from, 0); err != nil {
				return err
			}
		}
	}
	return nil
}
