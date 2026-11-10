package trak_test

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

	"github.com/desx/trak/go"
	"github.com/desx/trak/go/internal/vectors"
)

const vectorsDir = "../conformance"

func vectorNames(t *testing.T, sub string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(vectorsDir, sub, "*.trak"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no vectors in %s: %v", sub, err)
	}
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = strings.TrimSuffix(filepath.Base(p), ".trak")
	}
	return names
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
			path := filepath.Join(vectorsDir, "valid", name+".trak")
			data, err := os.ReadFile(filepath.Join(vectorsDir, "valid", name+".expect.json"))
			if err != nil {
				t.Fatal(err)
			}
			var want any
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatal(err)
			}
			decoded, err := vectors.Decode(path)
			if err != nil {
				t.Fatal(err)
			}
			got := normalize(t, decoded)
			if !reflect.DeepEqual(got, want) {
				g, _ := json.Marshal(got)
				t.Fatalf("decoded value differs:\n got %s\nwant %s", g, data)
			}
			if _, err := trak.Check(path); err != nil {
				t.Fatalf("check: %v", err)
			}
		})
	}
}

func TestConformanceInvalid(t *testing.T) {
	for _, name := range vectorNames(t, "invalid") {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(vectorsDir, "invalid", name+".trak")
			reason, _ := os.ReadFile(filepath.Join(vectorsDir, "invalid", name+".reason.txt"))
			_, err := trak.Check(path)
			var fe *trak.FormatError
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

func TestConformanceWrite(t *testing.T) {
	for _, name := range vectorNames(t, "write") {
		t.Run(name, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join(vectorsDir, "write", name+".input.json"))
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "out.trak")
			if err := vectors.Replay(out, input); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(out)
			want, _ := os.ReadFile(filepath.Join(vectorsDir, "write", name+".trak"))
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
