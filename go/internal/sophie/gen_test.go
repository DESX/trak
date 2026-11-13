package sophie

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	trak "github.com/desx/trak/go"
)

func TestSchemaMatchesTheDoc(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/sophie.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(string(doc), "```\n")
	block, _, _ := strings.Cut(rest, "```")
	if block != Schema {
		t.Error("sophie.schema differs from the schema in docs/sophie.md")
	}
}

func generate(t *testing.T, size int64, seed uint64, policy trak.Policy) (string, Stats) {
	t.Helper()
	name := filepath.Join(t.TempDir(), "s.trak")
	st, err := Generate(name, Config{Size: size, Seed: seed, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	return name, st
}

func TestReproducible(t *testing.T) {
	a, _ := generate(t, 3e6, 7, trak.Reference)
	b, _ := generate(t, 3e6, 7, trak.Reference)
	c, _ := generate(t, 3e6, 8, trak.Reference)
	ab, _ := os.ReadFile(a)
	bb, _ := os.ReadFile(b)
	cb, _ := os.ReadFile(c)
	if !bytes.Equal(ab, bb) {
		t.Error("the same seed wrote different files")
	}
	if bytes.Equal(ab, cb) {
		t.Error("different seeds wrote the same file")
	}
}

// TestEveryList generates a file under each policy, checks it, and requires every
// event list and every kind of event the schema describes.
func TestEveryList(t *testing.T) {
	for _, policy := range []trak.Policy{trak.Reference, trak.Delta} {
		t.Run(policy.String(), func(t *testing.T) {
			name, st := generate(t, 60e6, 1, policy)
			rep, err := trak.CheckReport(name, nil)
			if err != nil {
				t.Fatal(err)
			}
			if policy == trak.Delta && rep.DeltaNodes == 0 {
				t.Error("the delta policy wrote no deltas")
			}
			if st.Sessions < 100 || st.MaxTurns < 2*st.Turns/st.Sessions {
				t.Errorf("sessions %d, turns %d, largest %d", st.Sessions, st.Turns, st.MaxTurns)
			}
			f, err := trak.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			v := f.Version()
			count := func(path string) uint64 {
				x, err := f.Get(path, v)
				if err != nil {
					t.Fatalf("%s: %v", path, err)
				}
				return x.(*trak.List).Len()
			}
			for _, p := range []string{"logins", "quota", "sessions", "defaults.backend", "defaults.model", "defaults.effort",
				"defaults.workdir", "defaults.compact_at", "defaults.nanny", "defaults.retry_quota", "defaults.watchdog.w1",
				"defaults.watchdog.check", "defaults.prompts.base", "defaults.prompts.compaction"} {
				if count(p) == 0 {
					t.Errorf("%s is empty", p)
				}
			}
			if count("defaults.prompts.base") < 2 {
				t.Error("the base prompt never changed")
			}
			// Kinds of events, across every session.
			seen := map[string]bool{}
			sessions := count("sessions")
			for s := uint64(0); s < sessions; s++ {
				for _, l := range []string{"name", "nanny", "state", "messages", "compactions", "results"} {
					if count(pathf("sessions.%d.%s", s, l)) > 0 {
						seen[l] = true
					}
				}
				if count(pathf("sessions.%d.name", s)) > 1 {
					seen["rename"] = true
				}
				if count(pathf("sessions.%d.nanny", s)) > 1 {
					seen["nanny toggle"] = true
				}
				obj, _ := f.Get(pathf("sessions.%d", s), v)
				if l, _ := obj.(*trak.Object).Leaf("spawned_by"); l.Present {
					seen["spawned"] = true
				}
				if l, _ := obj.(*trak.Object).Leaf("setup.prompts.base"); l.String() != pathf("defaults.prompts.base.%d", count("defaults.prompts.base")-1) {
					seen["older prompt"] = true
				}
				turns := count(pathf("sessions.%d.turns", s))
				for i := uint64(0); i < turns && i < 400; i++ {
					tp := pathf("sessions.%d.turns.%d", s, i)
					seen["kind "+str(t, f, tp+".kind", v)] = true
					seen["check "+str(t, f, tp+".check", v)] = true
					if count(tp+".launch") > 0 {
						seen["approval "+str(t, f, tp+".launch.0.approval", v)] = true
					}
					if count(tp+".exit") > 0 {
						seen["exit "+str(t, f, tp+".exit.0.reason", v)] = true
					}
				}
			}
			for _, want := range []string{"messages", "compactions", "results", "rename", "nanny toggle", "spawned", "older prompt",
				"kind user", "kind auto", "kind watchdog", "kind timeout", "kind resume", "check ok", "check syntax", "check no_start",
				"approval auto", "approval approved", "approval edited", "approval rejected", "exit done", "exit killed", "exit timeout"} {
				if !seen[want] {
					t.Errorf("no %s", want)
				}
			}
			blocked := false
			for i := uint64(0); i < count("quota"); i++ {
				blocked = blocked || str(t, f, pathf("quota.%d.state", i), v) == "blocked"
			}
			failed := false
			for i := uint64(0); i < count("logins"); i++ {
				failed = failed || str(t, f, pathf("logins.%d.outcome", i), v) == "failed"
			}
			if !blocked || !failed {
				t.Errorf("quota blocked %v, failed login %v", blocked, failed)
			}
		})
	}
}

func str(t *testing.T, f *trak.File, path string, v uint64) string {
	t.Helper()
	x, err := f.Get(path, v)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return x.(trak.Leaf).String()
}

func pathf(format string, args ...any) string { return fmt.Sprintf(format, args...) }
