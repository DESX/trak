package trak_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/desx/trak/go"
	"github.com/desx/trak/go/internal/vectors"
)

func mustV[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestNativeNested(t *testing.T) {
	p := filepath.Join(t.TempDir(), "n.trak")
	w := mustV(trak.Create(p, trak.Options{}))
	mustV(w.Define("t", "{ jobs: [] { env: {} str, log: |str? }, labels: {} [] str, empty: [] {}, m: {} num }"))
	mustV(w.Write("t", map[string]any{
		"jobs": []any{
			map[string]any{"env": trak.Pairs{{"z", "1"}, {"a.b", "2"}}, "log": "x\n--[ log ]--"},
			map[string]any{"env": map[string]string{"b": "2", "a": "1"}},
		},
		"labels": []any{[]any{"none", []string{}}, []any{"some", []string{"1"}}},
		"empty":  []struct{}{{}, {}},
		"m":      map[string]uint8{"k": 7},
	}))
	w.Close()
	mustV(trak.Check(p))
	r := mustV(trak.Open(p))
	defer r.Close()
	tab := mustV(r.Table("t"))
	e := mustV(tab.Get(1))
	got, _ := json.Marshal(vectors.Native(mustV(e.Native())))
	want := `{"empty":[{},{}],"jobs":[{"env":[["z","1"],["a.b","2"]],"log":"x\n--[ log ]--"},{"env":[["a","1"],["b","2"]],"log":null}],"labels":[[null,[]],["some",["1"]]],"m":[["k","7"]]}`
	if string(got) != want {
		t.Fatalf("got %s", got)
	}
	if v, _, _ := e.Get("jobs.0.env.a.b"); v != "2" {
		t.Fatal(v)
	}
}
