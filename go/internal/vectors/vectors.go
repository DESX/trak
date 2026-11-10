// Package vectors decodes and replays conformance vectors (see
// conformance/README.md). It is shared by the tests and the interop tool.
package vectors

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/desx/trak/go"
)

func annotations(as []trak.Annotation) []any {
	out := []any{}
	for _, a := range as {
		var text any
		if a.HasText {
			text = a.Text
		}
		out = append(out, map[string]any{"name": a.Name, "text": text})
	}
	return out
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Native converts an Event.Native tree to the expect.json shapes.
func Native(v any) any {
	switch v := v.(type) {
	case *trak.Object:
		out := map[string]any{}
		for i, k := range v.Keys {
			out[k] = Native(v.Values[i])
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = Native(x)
		}
		return out
	case []trak.MapEntry:
		out := make([]any, len(v))
		for i, e := range v {
			var key any
			if e.Known {
				key = e.Key
			}
			out[i] = []any{key, Native(e.Value)}
		}
		return out
	}
	return v
}

// Decode reads a file into the expect.json structure. It also checks that
// navigation agrees with a file-order scan.
func Decode(path string) (map[string]any, error) {
	r, err := trak.Open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	types := []any{}
	for _, s := range r.Types() {
		fields := []any{}
		for _, f := range s.Fields {
			enum := []any{}
			for _, w := range f.Enum {
				enum = append(enum, w)
			}
			fields = append(fields, map[string]any{
				"path": f.Path, "type": f.Type, "enum": enum, "unknown": nullable(f.Unknown),
				"block": f.Block, "optional": f.Optional, "annotations": annotations(f.Annotations),
				"description": nullable(f.Description),
			})
		}
		types = append(types, map[string]any{
			"name": s.Name, "index": s.Index, "version": s.Version, "parent": nullable(s.Parent),
			"description": nullable(s.Description), "parsed": s.Root != nil,
			"annotations": annotations(s.Annotations()), "fields": fields,
		})
	}
	events := []any{}
	var scanned []scan
	counts := map[string]int64{}
	var werr error
	err = r.Events(func(e *trak.Event) bool {
		var value any
		if e.Schema.Root != nil {
			obj, err := e.Native()
			if err != nil {
				werr = err
				return false
			}
			value = Native(obj)
		}
		families := map[string]any{}
		for _, a := range e.Schema.Lineage {
			num, err := e.FamilyNumber(a)
			if err != nil {
				werr = err
				return false
			}
			families[a] = num
		}
		events = append(events, map[string]any{
			"type": e.Type(), "n": e.N(), "timestamp": e.Timestamp(), "families": families, "value": value,
		})
		scanned = append(scanned, scan{e.Type(), e.N(), e.Schema.Lineage, families})
		counts[e.Type()]++
		return true
	})
	if err == nil {
		err = werr
	}
	if err != nil {
		return nil, err
	}
	for _, s := range r.Types() {
		table, _ := r.Table(s.Name)
		count, err := table.Count()
		if err != nil {
			return nil, err
		}
		if count != counts[s.Name] {
			return nil, fmt.Errorf("%s: count %d, scanned %d", s.Name, count, counts[s.Name])
		}
		for n := int64(1); n <= count; n += 1 + count/50 {
			e, err := table.Get(n)
			if err != nil {
				return nil, err
			}
			if e.N() != n {
				return nil, fmt.Errorf("%s: get(%d) returned %d", s.Name, n, e.N())
			}
		}
		if err := checkFamily(r, s, scanned); err != nil {
			return nil, err
		}
	}
	if err := checkPages(r, scanned); err != nil {
		return nil, err
	}
	return map[string]any{"types": types, "events": events}, nil
}

// scan is one event as seen by the file-order scan.
type scan struct {
	typ      string
	n        int64
	lineage  []string
	families map[string]any
}

func (s scan) String() string { return fmt.Sprintf("%s %d", s.typ, s.n) }

func same(e *trak.Event, s scan) bool { return e.Type() == s.typ && e.N() == s.n }

// checkFamily checks that a type's family table agrees with the scan.
func checkFamily(r *trak.Reader, s *trak.Schema, scanned []scan) error {
	fam, err := r.Family(s.Name)
	if err != nil {
		return err
	}
	var members []scan
	for _, e := range scanned {
		if slices.Contains(e.lineage, s.Name) {
			members = append(members, e)
		}
	}
	count, err := fam.Count()
	if err != nil {
		return err
	}
	if count != int64(len(members)) {
		return fmt.Errorf("family %s: count %d, scanned %d", s.Name, count, len(members))
	}
	for i, row := range members {
		m := int64(i + 1)
		if row.families[s.Name] != m {
			return fmt.Errorf("family %s: %v has number %v, expected %d", s.Name, row, row.families[s.Name], m)
		}
		e, err := fam.Get(m)
		if err != nil {
			return err
		}
		if !same(e, row) {
			return fmt.Errorf("family %s: get(%d) returned %s %d, expected %v", s.Name, m, e.Type(), e.N(), row)
		}
	}
	if count == 0 {
		return nil
	}
	i := 0
	err = fam.Range(1, count, func(e *trak.Event) bool {
		if i >= len(members) || !same(e, members[i]) {
			err = fmt.Errorf("family %s: row %d of the range is %s %d", s.Name, i+1, e.Type(), e.N())
			return false
		}
		i++
		return true
	})
	if err == nil && i != len(members) {
		err = fmt.Errorf("family %s: range yielded %d rows, expected %d", s.Name, i, len(members))
	}
	return err
}

// checkPages checks that paging with cursors visits every event once, in
// file order.
func checkPages(r *trak.Reader, scanned []scan) error {
	for _, forward := range []bool{true, false} {
		var got []*trak.Event
		cursor := int64(-1)
		for {
			var page []*trak.Event
			var err error
			if forward {
				page, err = r.After(cursor, 3)
			} else {
				page, err = r.Before(cursor, 4)
			}
			if err != nil {
				return err
			}
			if len(page) == 0 {
				break
			}
			got = append(got, page...)
			cursor = page[len(page)-1].Offset()
		}
		if len(got) != len(scanned) {
			return fmt.Errorf("paging (forward %v) found %d events, scanned %d", forward, len(got), len(scanned))
		}
		for i, e := range got {
			want := scanned[i]
			if !forward {
				want = scanned[len(scanned)-1-i]
			}
			if !same(e, want) {
				return fmt.Errorf("paging (forward %v): event %d is %s %d, expected %v", forward, i, e.Type(), e.N(), want)
			}
		}
	}
	return nil
}

type op struct {
	Op        string `json:"op"`
	Name      string `json:"name"`
	Schema    string `json:"schema"`
	Type      string `json:"type"`
	Value     any    `json:"value"`
	Timestamp string `json:"timestamp"`
}

// Replay applies an input.json to a new file at path.
func Replay(path string, input []byte) error {
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	var ops []op
	if err := dec.Decode(&ops); err != nil {
		return err
	}
	w, err := trak.Create(path, trak.Options{Overwrite: true})
	if err != nil {
		return err
	}
	for _, o := range ops {
		if o.Op == "define" {
			_, err = w.Define(o.Name, o.Schema)
		} else {
			ts, perr := trak.ParseTimestamp(o.Timestamp)
			if perr != nil {
				err = perr
			} else {
				_, err = w.WriteAt(o.Type, o.Value, ts)
			}
		}
		if err != nil {
			w.Close()
			return err
		}
	}
	return w.Close()
}
