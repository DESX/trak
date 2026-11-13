package sophie

import (
	"bytes"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
)

// Stand-in content. Pools of plausible lines are built once from the seed;
// values are slices of them, cut at line ends, so generating gigabytes is
// mostly copying.

var words = strings.Fields(`the a to of and in for is on that with this it as be at by from or
build test file line error module function value check output script session agent prompt
reply context token limit watchdog compaction handover login quota backend model effort
directory config default setting approve reject edit kill timeout retry result message
user task branch commit merge patch diff review deploy server client request response
cache index table column row schema version path list item count trie node footer body
fast slow large small first last next previous latest older newer clean broken fixed
should could would will must can may need want try run start stop wait read write open close`)

var commands = []string{
	"ls -la %s", "cat %s", "grep -rn %q %s", "sed -n '%d,%dp' %s", "go test ./... -run %s",
	"go build ./...", "git status --short", "git diff --stat", "git log --oneline -%d",
	"find %s -name '*.go' | head -%d", "wc -l %s", "head -%d %s", "tail -%d %s",
	"python3 -m pytest -q %s", "npm test -- %s", "make %s", "curl -s localhost:%d/health",
	"jq '.items | length' %s", "rg -n %q %s", "du -sh %s", "awk '{print $%d}' %s | sort | uniq -c",
}

var dirs = []string{"src", "internal/store", "cmd/server", "web/app", "pkg/api", "docs", "scripts", "test/e2e"}
var files = []string{"main.go", "store.go", "handler.go", "config.yaml", "README.md", "app.ts", "index.html", "Makefile", "schema.sql", "server_test.go"}
var names = []string{"TestParse", "TestStore", "TestHandler", "build", "lint", "release", "TestE2E", "check"}

type text struct {
	prose, bash, logs []byte
}

func newText(r *rand.Rand) *text {
	t := &text{}
	var b bytes.Buffer
	for b.Len() < 1<<20 {
		n := 6 + r.IntN(18)
		for i := 0; i < n; i++ {
			w := words[r.IntN(len(words))]
			if i == 0 {
				w = strings.ToUpper(w[:1]) + w[1:]
			}
			b.WriteString(w)
			if i < n-1 {
				b.WriteByte(' ')
			}
		}
		b.WriteString(".\n")
	}
	t.prose = append([]byte(nil), b.Bytes()...)
	b.Reset()
	for b.Len() < 1<<20 {
		b.WriteString(t.command(r))
		b.WriteByte('\n')
	}
	t.bash = append([]byte(nil), b.Bytes()...)
	b.Reset()
	for b.Len() < 2<<20 {
		f := dirs[r.IntN(len(dirs))] + "/" + files[r.IntN(len(files))]
		switch r.IntN(6) {
		case 0:
			fmt.Fprintf(&b, "%s:%d:%d: %s %s %s\n", f, 1+r.IntN(900), 1+r.IntN(80), words[r.IntN(len(words))], words[r.IntN(len(words))], words[r.IntN(len(words))])
		case 1:
			fmt.Fprintf(&b, "ok  \texample.com/%s\t%d.%03ds\n", dirs[r.IntN(len(dirs))], r.IntN(9), r.IntN(1000))
		case 2:
			fmt.Fprintf(&b, "-rw-r--r-- 1 sophie sophie %7d Nov %2d %02d:%02d %s\n", r.IntN(200000), 1+r.IntN(28), r.IntN(24), r.IntN(60), f)
		case 3:
			fmt.Fprintf(&b, "%4d  %s\n", 1+r.IntN(4000), bytes.TrimSpace(t.line(r, t.prose)))
		case 4:
			fmt.Fprintf(&b, "--- FAIL: %s (%d.%02ds)\n    %s:%d: got %d, want %d\n", names[r.IntN(len(names))], r.IntN(5), r.IntN(100), files[r.IntN(len(files))], 1+r.IntN(400), r.IntN(100), r.IntN(100))
		default:
			fmt.Fprintf(&b, "[%02d:%02d:%02d] %s %s %s\n", r.IntN(24), r.IntN(60), r.IntN(60), words[r.IntN(len(words))], words[r.IntN(len(words))], f)
		}
	}
	t.logs = append([]byte(nil), b.Bytes()...)
	return t
}

func (t *text) command(r *rand.Rand) string {
	c := commands[r.IntN(len(commands))]
	var args []any
	for i := 0; i+1 < len(c); i++ {
		if c[i] != '%' {
			continue
		}
		if c[i+1] == 'd' {
			args = append(args, 1+r.IntN(200))
		} else {
			args = append(args, dirs[r.IntN(len(dirs))]+"/"+files[r.IntN(len(files))])
		}
	}
	return fmt.Sprintf(c, args...)
}

// line returns one random line of pool.
func (t *text) line(r *rand.Rand, pool []byte) []byte {
	i := r.IntN(len(pool) - 200)
	i += bytes.IndexByte(pool[i:], '\n') + 1
	j := bytes.IndexByte(pool[i:], '\n')
	return pool[i : i+j+1]
}

// slice returns about n bytes of whole lines from pool, without a final LF.
func (t *text) slice(r *rand.Rand, pool []byte, n int) []byte {
	if n <= 0 {
		return nil
	}
	var out []byte
	for len(out) < n {
		i := r.IntN(len(pool) - 1)
		i += bytes.IndexByte(pool[i:], '\n') + 1
		if i >= len(pool) {
			continue
		}
		end := i + (n - len(out))
		if end > len(pool) {
			end = len(pool)
		}
		if k := bytes.LastIndexByte(pool[i:end], '\n'); k > 0 {
			end = i + k + 1
		} else if k2 := bytes.IndexByte(pool[i:], '\n'); k2 >= 0 {
			end = i + k2 + 1
		}
		out = append(out, pool[i:end]...)
	}
	return bytes.TrimRight(out, "\n")
}

// lognormal returns a size with the given median, clamped to [lo, hi].
func lognormal(r *rand.Rand, median, sigma float64, lo, hi int) int {
	v := int(median * math.Exp(sigma*r.NormFloat64()))
	if v < lo {
		v = lo
	}
	if v > hi {
		v = hi
	}
	return v
}
