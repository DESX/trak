// Package vectors decodes and replays conformance vectors (see
// conformance/README.md). It is shared by the tests and the interop tool.
package vectors

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/buk/trak/go"
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
			"name": s.Name, "index": s.Index, "version": s.Version,
			"description": nullable(s.Description), "parsed": s.Root != nil,
			"annotations": annotations(s.Annotations()), "fields": fields,
		})
	}
	events := []any{}
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
		events = append(events, map[string]any{
			"type": e.Type(), "n": e.N(), "timestamp": e.Timestamp(), "value": value,
		})
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
	}
	return map[string]any{"types": types, "events": events}, nil
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
