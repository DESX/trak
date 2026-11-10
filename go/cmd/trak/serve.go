package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The viewer (../../../viewer) and the JavaScript reader it imports (../../../js/src),
// copied here by go generate so that go install ships them.
//
//go:generate sh -c "rm -rf ui && mkdir -p ui/viewer ui/js && cp ../../../viewer/*.html ../../../viewer/*.js ../../../viewer/*.css ui/viewer/ && cp -R ../../../js/src ui/js/src"
//go:embed ui
var uiFiles embed.FS

// server serves the viewer and read-only byte ranges of the .trak files under root.
//
//	/                     redirects to viewer/
//	/viewer/...           the viewer
//	/js/src/...           the JavaScript reader
//	/viewer/api/files     a JSON listing of one directory: ?dir=sub/dir
//	/viewer/files/PATH    a .trak file, with HEAD and Range support
type server struct {
	root string // absolute, symlinks resolved
	name string // shown as the listing's title
	ui   http.Handler
}

func newServer(root string) (*server, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(real); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	sub, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		return nil, err
	}
	return &server{root: real, name: filepath.Base(real), ui: http.FileServer(http.FS(sub))}, nil
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "read-only", http.StatusMethodNotAllowed)
		return
	}
	switch p := r.URL.Path; {
	case p == "/" || p == "":
		// A relative Location, so that it works behind --prefix (http.Redirect would
		// resolve it against the stripped path).
		h.Set("Location", "viewer/")
		w.WriteHeader(http.StatusFound)
	case p == "/viewer/api/files":
		s.list(w, r)
	case strings.HasPrefix(p, "/viewer/files/"):
		s.file(w, r, strings.TrimPrefix(p, "/viewer/files/"))
	case strings.HasPrefix(p, "/viewer/") || strings.HasPrefix(p, "/js/src/"):
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'")
		h.Set("Cache-Control", "no-cache")
		s.ui.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

// resolve maps a slash-separated path under root to a real path, refusing anything that
// leaves root, including through symlinks, and hidden files.
func (s *server) resolve(rel string) (string, error) {
	if rel == "" {
		return s.root, nil
	}
	clean := path.Clean("/" + rel)[1:]
	if clean != rel || strings.Contains(rel, "\\") {
		return "", errors.New("bad path")
	}
	for _, part := range strings.Split(clean, "/") {
		if strings.HasPrefix(part, ".") {
			return "", errors.New("bad path")
		}
	}
	real, err := filepath.EvalSymlinks(filepath.Join(s.root, filepath.FromSlash(clean)))
	if err != nil {
		return "", err
	}
	if real != s.root && !strings.HasPrefix(real, s.root+string(filepath.Separator)) {
		return "", errors.New("path leaves the served directory")
	}
	return real, nil
}

type listing struct {
	Root  string      `json:"root"`
	Dirs  []string    `json:"dirs"`
	Files []fileEntry `json:"files"`
}

type fileEntry struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

func (s *server) list(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("dir")
	dir, err := s.resolve(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	out := listing{Root: s.name, Dirs: []string{}, Files: []fileEntry{}}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		real, err := s.resolve(path.Join(rel, name)) // skips symlinks that leave root
		if err != nil {
			continue
		}
		info, err := os.Stat(real)
		if err != nil {
			continue
		}
		switch {
		case info.IsDir():
			out.Dirs = append(out.Dirs, name)
		case info.Mode().IsRegular() && strings.HasSuffix(name, ".trak"):
			out.Files = append(out.Files, fileEntry{name, info.Size(), info.ModTime().UTC().Format(time.RFC3339)})
		}
	}
	sort.Strings(out.Dirs)
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Name < out.Files[j].Name })
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *server) file(w http.ResponseWriter, r *http.Request, rel string) {
	real, err := s.resolve(rel)
	if err != nil || !strings.HasSuffix(real, ".trak") {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(real)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	// Files grow while being followed, so never cache and never send a stale length.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", time.Time{}, io.NewSectionReader(f, 0, info.Size()))
}

// serve runs the viewer until stop is closed (tests) or forever.
func serve(out io.Writer, a args, stop <-chan struct{}) error {
	addr := a.flags["addr"]
	if addr == "" {
		addr = "127.0.0.1:8765"
	}
	prefix := strings.TrimSuffix(a.flags["prefix"], "/")
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		return fmt.Errorf("%w: --prefix must start with /", errUsage)
	}
	s, err := newServer(a.pos[0])
	if err != nil {
		return err
	}
	var h http.Handler = s
	if prefix != "" {
		mux := http.NewServeMux()
		mux.Handle(prefix+"/", http.StripPrefix(prefix, s))
		mux.Handle(prefix, http.RedirectHandler(prefix+"/", http.StatusFound))
		h = mux
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	fmt.Fprintf(out, "serving %s at http://%s%s/\n", s.root, ln.Addr(), prefix)
	if stop != nil {
		go func() {
			<-stop
			_ = srv.Shutdown(context.Background())
		}()
	}
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
