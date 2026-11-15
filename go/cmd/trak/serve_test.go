package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer is a bytes.Buffer safe to read while serve writes to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func vector(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "valid", "004-nested.trak"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func serveDir(t *testing.T) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	data := vector(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "a.trak"), data, 0o644))
	must(os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not trak"), 0o644))
	must(os.WriteFile(filepath.Join(dir, ".hidden.trak"), data, 0o644))
	must(os.MkdirAll(filepath.Join(dir, "sub", "deeper"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "sub", "b.trak"), data[:100], 0o644))
	outside := filepath.Join(t.TempDir(), "secret.trak")
	must(os.WriteFile(outside, []byte("secret"), 0o644))
	if runtime.GOOS != "windows" {
		must(os.Symlink(outside, filepath.Join(dir, "escape.trak")))
		must(os.Symlink(filepath.Join(dir, "a.trak"), filepath.Join(dir, "sub", "link.trak")))
	}
	return dir, data
}

func fetch(t *testing.T, h http.Handler, method, url string, header map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, url, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func TestServeListing(t *testing.T) {
	dir, data := serveDir(t)
	s, err := newServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	res := fetch(t, s, "GET", "/viewer/api/files", nil)
	var l listing
	if err := json.NewDecoder(res.Body).Decode(&l); err != nil {
		t.Fatal(err)
	}
	if len(l.Dirs) != 1 || l.Dirs[0] != "sub" {
		t.Errorf("dirs = %v", l.Dirs)
	}
	if len(l.Files) != 1 || l.Files[0].Name != "a.trak" || l.Files[0].Size != int64(len(data)) {
		t.Errorf("files = %+v (hidden files, other files and escaping links are left out)", l.Files)
	}
	if real, _ := filepath.EvalSymlinks(dir); l.Root != real || l.Dir != real {
		t.Errorf("root = %q, dir = %q, want %q", l.Root, l.Dir, real)
	}
	if len(l.Files) == 1 && (l.Files[0].Desc == "" || l.Files[0].Error != "") {
		t.Errorf("a.trak: desc %q, error %q", l.Files[0].Desc, l.Files[0].Error)
	}
	for _, bad := range []string{"..", "../..", "/etc", "sub/../..", ".git", "sub/./deeper", "nope"} {
		if res := fetch(t, s, "GET", "/viewer/api/files?dir="+bad, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("dir=%q: status %d", bad, res.StatusCode)
		}
	}
}

func TestServeRead(t *testing.T) {
	dir, data := serveDir(t)
	s, _ := newServer(dir)
	res := fetch(t, s, "GET", "/viewer/api/read?path=a.trak&r=0+9,20+5,0+0,100+10", nil)
	body, _ := io.ReadAll(res.Body)
	want := string(data[0:9]) + string(data[20:25]) + string(data[100:110])
	if res.StatusCode != 200 || string(body) != want {
		t.Errorf("read: %d %q, want %q", res.StatusCode, body, want)
	}
	if res.Header.Get("X-Trak-Size") != itoa(len(data)) || res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", res.Header)
	}
	if res := fetch(t, s, "GET", "/viewer/api/read?path=a.trak&r=", nil); res.StatusCode != 200 || res.Header.Get("X-Trak-Size") != itoa(len(data)) {
		t.Errorf("size only: %d", res.StatusCode)
	}
	big := strings.Repeat("0+1,", maxRanges) + "0+1"
	for q, code := range map[string]int{
		"path=a.trak&r=0+99999":       http.StatusRequestedRangeNotSatisfiable,
		"path=a.trak&r=-1+2":          http.StatusRequestedRangeNotSatisfiable,
		"path=a.trak&r=x":             http.StatusRequestedRangeNotSatisfiable,
		"path=a.trak&r=" + big:        http.StatusRequestEntityTooLarge,
		"path=notes.txt&r=0+1":        http.StatusNotFound,
		"path=../a.trak&r=0+1":        http.StatusNotFound,
		"path=.hidden.trak&r=0+1":     http.StatusNotFound,
		"path=escape.trak&r=0+1":      http.StatusNotFound,
		"path=sub%5Cb.trak&r=0+1":     http.StatusNotFound,
		"path=sub/../a.trak&r=0+1":    http.StatusNotFound,
		"path=sub/missing.trak&r=0+1": http.StatusNotFound,
	} {
		if q == "path=escape.trak&r=0+1" && runtime.GOOS == "windows" {
			continue
		}
		if res := fetch(t, s, "GET", "/viewer/api/read?"+q, nil); res.StatusCode != code {
			t.Errorf("%s: %d, want %d", q, res.StatusCode, code)
		}
	}
	if res := fetch(t, s, "POST", "/viewer/api/read?path=a.trak&r=0+1", nil); res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", res.StatusCode)
	}
}

func TestServeFiles(t *testing.T) {
	dir, data := serveDir(t)
	s, _ := newServer(dir)
	res := fetch(t, s, "GET", "/viewer/files/a.trak", map[string]string{"Range": "bytes=0-8"})
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusPartialContent || string(body) != "trak 1.2\n" {
		t.Errorf("range: %d %q", res.StatusCode, body)
	}
	res = fetch(t, s, "GET", "/viewer/files/sub/b.trak", nil)
	body, _ = io.ReadAll(res.Body)
	if !bytes.Equal(body, data[:100]) {
		t.Error("sub/b.trak")
	}
	for _, p := range []string{"notes.txt", ".hidden.trak", "../a.trak", "sub", "escape.trak", "missing.trak"} {
		if res := fetch(t, s, "GET", "/viewer/files/"+p, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d", p, res.StatusCode)
		}
	}
}

func TestServeViewerAndPrefix(t *testing.T) {
	dir, _ := serveDir(t)
	stop := make(chan struct{})
	var out lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- serve(&out, args{pos: []string{dir}, flags: map[string]string{"addr": "127.0.0.1:0", "prefix": "/trak/"}}, stop)
	}()
	var base string
	for i := 0; i < 100 && base == ""; i++ {
		time.Sleep(20 * time.Millisecond)
		if s := out.String(); strings.Contains(s, "http://") {
			base = strings.TrimSpace(s[strings.Index(s, "http://"):])
		}
	}
	if !strings.HasSuffix(base, "/trak/") {
		t.Fatalf("serve printed %q", out.String())
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get(base)
	if err != nil || res.StatusCode != http.StatusFound || res.Header.Get("Location") != "viewer/" {
		t.Fatalf("/trak/: %v %v", err, res)
	}
	for path, kind := range map[string]string{"viewer/": "text/html", "viewer/app.js": "javascript", "js/src/index.js": "javascript", "viewer/style.css": "text/css"} {
		res, err := http.Get(base + path)
		if err != nil || res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), kind) {
			t.Errorf("%s: %v %v", path, err, res.Header.Get("Content-Type"))
			continue
		}
		if !strings.Contains(res.Header.Get("Content-Security-Policy"), "default-src 'self'") {
			t.Errorf("%s: no CSP", path)
		}
	}
	if res, _ := http.Get(base + "viewer/api/read?path=a.trak&r=0+4"); res.StatusCode != 200 {
		t.Errorf("read under prefix: %d", res.StatusCode)
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddedUIIsFresh(t *testing.T) {
	for _, dir := range []string{"viewer", "js/src"} {
		src := filepath.Join("..", "..", "..", dir)
		ents, err := os.ReadDir(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") && !strings.HasSuffix(e.Name(), ".html") && !strings.HasSuffix(e.Name(), ".css") {
				continue
			}
			a, _ := os.ReadFile(filepath.Join(src, e.Name()))
			b, err := uiFiles.ReadFile("ui/" + dir + "/" + e.Name())
			if err != nil || !bytes.Equal(a, b) {
				t.Errorf("ui/%s/%s is stale: run go generate ./cmd/trak", dir, e.Name())
			}
		}
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
