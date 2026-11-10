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

func serveDir(t *testing.T) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "valid", "027-family-deep.trak"))
	if err != nil {
		t.Fatal(err)
	}
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

func get(t *testing.T, h http.Handler, method, url string, header map[string]string) *http.Response {
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
	res := get(t, s, "GET", "/viewer/api/files", nil)
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
	res = get(t, s, "GET", "/viewer/api/files?dir=sub", nil)
	l = listing{}
	_ = json.NewDecoder(res.Body).Decode(&l)
	want := []string{"b.trak"}
	if runtime.GOOS != "windows" {
		want = []string{"b.trak", "link.trak"}
	}
	var got []string
	for _, f := range l.Files {
		got = append(got, f.Name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") || len(l.Dirs) != 1 {
		t.Errorf("sub: files %v dirs %v", got, l.Dirs)
	}
	for _, bad := range []string{"..", "../..", "/etc", "sub/../..", ".git", "sub/./deeper", "nope"} {
		if res := get(t, s, "GET", "/viewer/api/files?dir="+bad, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("dir=%q: status %d", bad, res.StatusCode)
		}
	}
}

func TestServeFiles(t *testing.T) {
	dir, data := serveDir(t)
	s, _ := newServer(dir)
	res := get(t, s, "GET", "/viewer/files/a.trak", map[string]string{"Range": "bytes=0-8"})
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusPartialContent || string(body) != "trak 1.1\n" {
		t.Errorf("range: %d %q", res.StatusCode, body)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	res = get(t, s, "HEAD", "/viewer/files/a.trak", nil)
	if res.Header.Get("Content-Length") != itoa(len(data)) || res.Header.Get("Accept-Ranges") != "bytes" {
		t.Errorf("HEAD headers: %v", res.Header)
	}
	res = get(t, s, "GET", "/viewer/files/sub/b.trak", nil)
	body, _ = io.ReadAll(res.Body)
	if !bytes.Equal(body, data[:100]) {
		t.Error("sub/b.trak")
	}
	bad := []string{"notes.txt", ".hidden.trak", "../a.trak", "sub/../a.trak", "sub", "escape.trak", "missing.trak", "sub%5Cb.trak"}
	for _, p := range bad {
		if res := get(t, s, "GET", "/viewer/files/"+p, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d", p, res.StatusCode)
		}
	}
	if res := get(t, s, "POST", "/viewer/files/a.trak", nil); res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", res.StatusCode)
	}
	if runtime.GOOS != "windows" {
		res := get(t, s, "GET", "/viewer/files/sub/link.trak", nil)
		body, _ := io.ReadAll(res.Body)
		if !bytes.Equal(body, data) {
			t.Error("a symlink inside the root is served")
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
	res, err := client.Get(strings.TrimSuffix(base, "/"))
	if err != nil || res.StatusCode != http.StatusFound || !strings.HasSuffix(res.Header.Get("Location"), "/trak/") {
		t.Fatalf("/trak: %v %v", err, res)
	}
	res, _ = client.Get(base)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "viewer/" {
		t.Errorf("/trak/ redirects to %q", res.Header.Get("Location"))
	}
	for path, kind := range map[string]string{"viewer/": "text/html", "viewer/app.js": "javascript", "js/src/index.js": "javascript", "viewer/style.css": "text/css"} {
		res, err := http.Get(base + path)
		if err != nil || res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), kind) {
			t.Errorf("%s: %v %v %s", path, err, res.StatusCode, res.Header.Get("Content-Type"))
		}
		if !strings.Contains(res.Header.Get("Content-Security-Policy"), "default-src 'self'") {
			t.Errorf("%s: no CSP", path)
		}
	}
	res, _ = http.Get(base + "viewer/files/a.trak")
	if res.StatusCode != 200 {
		t.Errorf("file under prefix: %d", res.StatusCode)
	}
	if res, _ := http.Get(strings.TrimSuffix(base, "trak/") + "viewer/files/a.trak"); res.StatusCode != 404 {
		t.Errorf("outside the prefix: %d", res.StatusCode)
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"serve"}, &out, &errOut); code != 2 {
		t.Errorf("no dir: exit %d", code)
	}
	if code := run([]string{"serve", t.TempDir(), "--prefix", "x"}, &out, &errOut); code != 2 {
		t.Errorf("bad prefix: exit %d", code)
	}
	f := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(f, nil, 0o644)
	if code := run([]string{"serve", f}, &out, &errOut); code != 1 {
		t.Errorf("not a dir: exit %d", code)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
