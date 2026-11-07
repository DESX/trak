package trak

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const vectors = "../conformance"

func vectorNames(t *testing.T, sub string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(vectors, sub, "*.trak"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no vectors in %s: %v", sub, err)
	}
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = strings.TrimSuffix(filepath.Base(p), ".trak")
	}
	return names
}

func annotationsJSON(as []Annotation) []any {
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

// nativeJSON converts a Native() tree to the expect.json shapes.
func nativeJSON(v any) any {
	switch v := v.(type) {
	case *Object:
		out := map[string]any{}
		for i, k := range v.Keys {
			out[k] = nativeJSON(v.Values[i])
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = nativeJSON(x)
		}
		return out
	case []MapEntry:
		out := make([]any, len(v))
		for i, e := range v {
			var key any
			if e.Known {
				key = e.Key
			}
			out[i] = []any{key, nativeJSON(e.Value)}
		}
		return out
	}
	return v
}

func decodeVector(t *testing.T, path string) any {
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
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
				"block": f.Block, "optional": f.Optional, "annotations": annotationsJSON(f.Annotations),
				"description": nullable(f.Description),
			})
		}
		types = append(types, map[string]any{
			"name": s.Name, "index": s.Index, "version": s.Version,
			"description": nullable(s.Description), "parsed": s.Root != nil,
			"annotations": annotationsJSON(s.Annotations()), "fields": fields,
		})
	}
	events := []any{}
	counts := map[string]int64{}
	err = r.Events(func(e *Event) bool {
		var value any
		if e.Schema.Root != nil {
			obj, err := e.Native()
			if err != nil {
				t.Fatal(err)
			}
			value = nativeJSON(obj)
		}
		events = append(events, map[string]any{
			"type": e.Type(), "n": e.N(), "timestamp": e.Timestamp(), "value": value,
		})
		counts[e.Type()]++
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	// Navigation must agree with the file-order scan.
	for _, s := range r.Types() {
		table, _ := r.Table(s.Name)
		count, err := table.Count()
		if err != nil || count != counts[s.Name] {
			t.Fatalf("%s: count %d, scanned %d (%v)", s.Name, count, counts[s.Name], err)
		}
		for n := int64(1); n <= count; n += 1 + count/50 {
			if _, err := table.Get(n); err != nil {
				t.Fatalf("%s %d: %v", s.Name, n, err)
			}
		}
	}
	return map[string]any{"types": types, "events": events}
}

// normalize round-trips through JSON so both sides use the same Go types.
func normalize(t *testing.T, v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestConformanceValid(t *testing.T) {
	for _, name := range vectorNames(t, "valid") {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(vectors, "valid", name+".trak")
			data, err := os.ReadFile(filepath.Join(vectors, "valid", name+".expect.json"))
			if err != nil {
				t.Fatal(err)
			}
			var want any
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatal(err)
			}
			got := normalize(t, decodeVector(t, path))
			if !reflect.DeepEqual(got, want) {
				g, _ := json.Marshal(got)
				t.Fatalf("decoded value differs:\n got %s\nwant %s", g, data)
			}
			if _, err := Check(path); err != nil {
				t.Fatalf("check: %v", err)
			}
		})
	}
}

func TestConformanceInvalid(t *testing.T) {
	for _, name := range vectorNames(t, "invalid") {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(vectors, "invalid", name+".trak")
			reason, _ := os.ReadFile(filepath.Join(vectors, "invalid", name+".reason.txt"))
			_, err := Check(path)
			var fe *FormatError
			if !errors.As(err, &fe) {
				t.Fatalf("check: want a FormatError, got %v", err)
			}
			for _, line := range strings.Split(string(reason), "\n") {
				if off, ok := strings.CutPrefix(line, "offset: "); ok {
					if want, _ := strconv.ParseInt(off, 10, 64); fe.Offset != want {
						t.Errorf("offset %d, want %d (%v)", fe.Offset, want, err)
					}
				}
			}
		})
	}
}

type op struct {
	Op        string `json:"op"`
	Name      string `json:"name"`
	Schema    string `json:"schema"`
	Type      string `json:"type"`
	Value     any    `json:"value"`
	Timestamp string `json:"timestamp"`
}

func replay(t testing.TB, path string, input []byte) {
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	var ops []op
	if err := dec.Decode(&ops); err != nil {
		t.Fatal(err)
	}
	w, err := Create(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, o := range ops {
		if o.Op == "define" {
			if _, err := w.Define(o.Name, o.Schema); err != nil {
				t.Fatal(err)
			}
			continue
		}
		ts, err := ParseTimestamp(o.Timestamp)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.WriteAt(o.Type, o.Value, ts); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConformanceWrite(t *testing.T) {
	for _, name := range vectorNames(t, "write") {
		t.Run(name, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join(vectors, "write", name+".input.json"))
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "out.trak")
			replay(t, out, input)
			got, _ := os.ReadFile(out)
			want, _ := os.ReadFile(filepath.Join(vectors, "write", name+".trak"))
			if !bytes.Equal(got, want) {
				i := 0
				for i < len(got) && i < len(want) && got[i] == want[i] {
					i++
				}
				t.Fatalf("bytes differ at offset %d (got %d bytes, want %d)", i, len(got), len(want))
			}
		})
	}
}
