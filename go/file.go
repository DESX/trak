package trak

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Magic is the file's first line.
const Magic = "trak 1.2\n"

// File reads a trak file. It is safe for one goroutine at a time.
type File struct {
	r         io.ReaderAt
	closer    io.Closer
	schema    *Schema
	schemaEnd int64 // where the first append starts
	size      int64 // the end of the last valid append
	latest    *footer
	cache     footerCache
	buf       []byte
}

// Open opens a trak file for reading.
func Open(name string) (*File, error) {
	fh, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	st, err := fh.Stat()
	if err != nil {
		fh.Close()
		return nil, err
	}
	f, err := NewFile(fh, st.Size())
	if err != nil {
		fh.Close()
		return nil, err
	}
	f.closer = fh
	return f, nil
}

// NewFile reads a trak file of the given size from r.
func NewFile(r io.ReaderAt, size int64) (*File, error) {
	f := &File{r: r, cache: newFooterCache(1 << 14)}
	if err := f.readHeader(size); err != nil {
		return nil, err
	}
	if err := f.findLatest(size); err != nil {
		return nil, err
	}
	return f, nil
}

// Close closes the underlying file, if Open opened it.
func (f *File) Close() error {
	if f.closer != nil {
		return f.closer.Close()
	}
	return nil
}

// Schema returns the file's schema.
func (f *File) Schema() *Schema { return f.schema }

// Size returns the end of the last valid append. Bytes after it are a torn tail.
func (f *File) Size() int64 { return f.size }

// SchemaEnd returns where the first append starts.
func (f *File) SchemaEnd() int64 { return f.schemaEnd }

// Version returns the latest version: the number of appends.
func (f *File) Version() uint64 {
	if f.latest == nil {
		return 0
	}
	return f.latest.version
}

func (f *File) read(off int64, n int) ([]byte, error) {
	if cap(f.buf) < n {
		f.buf = make([]byte, n)
	}
	b := f.buf[:n]
	m, err := f.r.ReadAt(b, off)
	if m == n {
		return b, nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return nil, err
}

// readNew reads into a new slice.
func (f *File) readNew(off int64, n int64) ([]byte, error) {
	b := make([]byte, n)
	m, err := f.r.ReadAt(b, off)
	if int64(m) == n {
		return b, nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return nil, err
}

func (f *File) readHeader(size int64) error {
	n := int64(64 << 10)
	if n > size {
		n = size
	}
	head, err := f.readNew(0, n)
	if err != nil {
		return err
	}
	nl := bytes.IndexByte(head, '\n')
	if nl < 0 || !bytes.HasPrefix(head, []byte("trak ")) {
		return formatErr(0, "not a trak file")
	}
	ver := string(head[5:nl])
	major, minor, ok := strings.Cut(ver, ".")
	mi, err := strconv.Atoi(minor)
	if !ok || err != nil {
		return formatErr(5, "bad version %q", ver)
	}
	if major != "1" || mi < 2 {
		return &UnsupportedVersionError{Version: ver}
	}
	line := head[nl+1:]
	end := bytes.IndexByte(line, '\n')
	const tag = "##[ schema ]##"
	if end < 0 || !bytes.HasPrefix(line, []byte(tag)) {
		return formatErr(int64(nl+1), "expected the schema header")
	}
	vstart := int64(nl+1) + int64(len(tag))
	vals, err := DecodeVLQs(line[len(tag):end], vstart)
	if err != nil {
		return err
	}
	if len(vals) < 2 || vals[1] != uint64(len(vals)-2) {
		return formatErr(vstart, "the schema header holds a length, then an extension count and its VLQs")
	}
	textStart := int64(nl+1) + int64(end) + 1
	if vals[0] == 0 || vals[0] > uint64(size-textStart) {
		return formatErr(vstart, "schema length %d runs past the end of the file", vals[0])
	}
	text, err := f.readNew(textStart, int64(vals[0]))
	if err != nil {
		return err
	}
	if text[len(text)-1] != '\n' {
		return formatErr(textStart+int64(len(text))-1, "the schema text must end with LF")
	}
	if f.schema, err = ParseSchema(string(text)); err != nil {
		return err
	}
	f.schemaEnd = textStart + int64(len(text))
	return nil
}

// findLatest finds the last valid footer (SPEC §8).
func (f *File) findLatest(size int64) error {
	f.size = f.schemaEnd
	end := size
	const chunk = 64 << 10
	for end > f.schemaEnd {
		lo := end - chunk
		if lo < f.schemaEnd {
			lo = f.schemaEnd
		}
		data, err := f.readNew(lo, end-lo)
		if err != nil {
			return err
		}
		// Try each line that ends in this chunk, last first.
		for e := len(data); e > 0; {
			if data[e-1] != '\n' {
				e = bytes.LastIndexByte(data[:e], '\n') + 1
				continue
			}
			s := bytes.LastIndexByte(data[:e-1], '\n') + 1
			if s == 0 && lo > f.schemaEnd {
				break // the line may start in the previous chunk
			}
			if bytes.HasPrefix(data[s:e], []byte("==[ ")) {
				if ft, err := f.validFooter(lo + int64(e)); err == nil {
					f.latest, f.size = ft, ft.end
					return nil
				}
			}
			e = s
		}
		next := lo + int64(bytes.IndexByte(data, '\n')) + 1
		if lo == f.schemaEnd || next <= lo {
			break
		}
		end = next
	}
	return nil
}

// validFooter parses the footer ending at end and walks its body.
func (f *File) validFooter(end int64) (*footer, error) {
	ft, err := f.footer(end)
	if err != nil {
		return nil, err
	}
	if err := f.walkBody(ft, nil); err != nil {
		return nil, err
	}
	if ft.version == 1 {
		if ft.body != f.schemaEnd {
			return nil, formatErr(ft.body, "version 1 must follow the schema")
		}
		return ft, nil
	}
	if ft.body <= f.schemaEnd {
		return nil, formatErr(ft.body, "version %d has no append before it", ft.version)
	}
	prev, err := f.footer(ft.body)
	if err != nil {
		return nil, err
	}
	if prev.version != ft.version-1 {
		return nil, formatErr(ft.start, "version %d follows version %d", ft.version, prev.version)
	}
	return ft, nil
}

// walkBody checks the body's framing against the leaf lengths and returns the
// body if keep is set.
func (f *File) walkBody(ft *footer, keep *[]byte) error {
	body, err := f.readNew(ft.body, ft.start-ft.body)
	if err != nil {
		return err
	}
	var pos int64
	bad := func(what string) error { return formatErr(ft.body+pos, "body: %s", what) }
	for _, p := range ft.leaves {
		if !p.present {
			continue
		}
		if p.field.Block {
			open := "--[ " + p.rel + " ]--\n"
			if !bytes.HasPrefix(body[pos:], []byte(open)) {
				return bad("expected " + strings.TrimSpace(open))
			}
			pos = p.off + p.n
			close := "\n--[ /" + p.rel + " ]--\n"
			if !bytes.HasPrefix(body[pos:], []byte(close)) {
				return bad("expected the end of block " + p.rel)
			}
			pos += int64(len(close))
		} else {
			if !bytes.HasPrefix(body[pos:], []byte(p.rel+": ")) {
				return bad("expected " + p.rel + ":")
			}
			pos = p.off + p.n
			if body[pos] != '\n' {
				return bad("expected LF after " + p.rel)
			}
			pos++
		}
	}
	if keep != nil {
		*keep = body
	}
	return nil
}

// footer reads and parses the footer of the append ending at end.
func (f *File) footer(end int64) (*footer, error) {
	if ft := f.cache.get(end); ft != nil {
		return ft, nil
	}
	if end <= f.schemaEnd {
		return nil, formatErr(end, "no append ends here")
	}
	n := int64(4096)
	for {
		lo := end - n
		if lo < f.schemaEnd {
			lo = f.schemaEnd
		}
		data, err := f.read(lo, int(end-lo))
		if err != nil {
			return nil, err
		}
		if data[len(data)-1] != '\n' {
			return nil, formatErr(end, "no append ends here")
		}
		if s := bytes.LastIndexByte(data[:len(data)-1], '\n'); s >= 0 || lo == f.schemaEnd {
			ft, err := parseFooterLine(data[s+1:], end, f.schema, f.schemaEnd)
			if err != nil {
				return nil, err
			}
			f.cache.put(end, ft)
			return ft, nil
		}
		if n >= 64<<20 {
			return nil, formatErr(end, "footer line too long")
		}
		n *= 4
	}
}

// resolve returns the node a ref names.
func (f *File) resolve(r ref) (*node, *footer, error) {
	ft, err := f.footer(r.end)
	if err != nil {
		return nil, nil, err
	}
	if int(r.i) > len(ft.nodes) {
		return nil, nil, formatErr(ft.start, "no node %d in a footer of %d nodes", r.i, len(ft.nodes))
	}
	return ft.node(r.i), ft, nil
}

// entryAt returns entry j of n, following a delta to its base.
func (f *File) entryAt(n *node, j int) (entry, error) {
	if e, ok := n.written(j); ok || n.full {
		return e, nil
	}
	b, _, err := f.resolve(n.base)
	if err != nil {
		return entry{}, err
	}
	if !b.full || b.kind != n.kind || b.typ != n.typ {
		return entry{}, formatErr(n.base.end, "a delta's base must be a full node of the same kind")
	}
	e, _ := b.written(j)
	return e, nil
}

// entriesOf returns every entry of n, at least min of them, resolving deltas.
func (f *File) entriesOf(n *node, min int) ([]entry, error) {
	var out []entry
	if n.full {
		out = append(out, n.entries...)
	} else {
		b, _, err := f.resolve(n.base)
		if err != nil {
			return nil, err
		}
		if !b.full || b.kind != n.kind || b.typ != n.typ {
			return nil, formatErr(n.base.end, "a delta's base must be a full node of the same kind")
		}
		out = append(out, b.entries...)
		for bm, k := n.bitmap, 0; bm != 0; bm &= bm - 1 {
			j := bitsTrailing(bm)
			for len(out) <= j {
				out = append(out, entry{})
			}
			out[j] = n.entries[k]
			k++
		}
	}
	for len(out) < min {
		out = append(out, entry{})
	}
	return out, nil
}

// footerAt returns the footer of version v.
func (f *File) footerAt(v uint64) (*footer, error) {
	if v == 0 || f.latest == nil || v > f.latest.version {
		return nil, fmt.Errorf("%w: version %d of %d", ErrNotFound, v, f.Version())
	}
	cur := f.latest
	for cur.version > v {
		jv := Jump(cur.version)
		var err error
		if jv >= v && cur.vjump != 0 {
			cur, err = f.footer(cur.vjump)
		} else {
			cur, err = f.footer(cur.body)
		}
		if err != nil {
			return nil, err
		}
	}
	return cur, nil
}

// footerAtTime returns the footer of the first version with a timestamp at
// or after t, or nil if there is none.
func (f *File) footerAtTime(t time.Time) (*footer, error) {
	ms := t.UnixMilli()
	if f.latest == nil || f.latest.ms < ms {
		return nil, nil
	}
	// Find the last version whose timestamp is before t; the answer follows it.
	cur := f.latest
	for cur.version > 1 {
		var next *footer
		var err error
		if cur.vjump != 0 {
			if next, err = f.footer(cur.vjump); err != nil {
				return nil, err
			}
			if next.ms >= ms {
				cur = next
				continue
			}
		}
		if next, err = f.footer(cur.body); err != nil {
			return nil, err
		}
		if next.ms < ms {
			return cur, nil
		}
		cur = next
	}
	return cur, nil
}

// Append describes one append.
type Append struct {
	Version uint64
	Path    string // the item it wrote
	Time    time.Time
	Start   int64 // where its body starts
	Footer  int64 // where its footer line starts
	End     int64 // after its footer line
}

func (ft *footer) info() Append {
	return Append{Version: ft.version, Path: ft.path, Time: time.UnixMilli(ft.ms).UTC(), Start: ft.body, Footer: ft.start, End: ft.end}
}

// AppendAt describes the append of version v.
func (f *File) AppendAt(v uint64) (Append, error) {
	ft, err := f.footerAt(v)
	if err != nil {
		return Append{}, err
	}
	return ft.info(), nil
}

// AppendEndingAt describes the append whose footer ends at end.
func (f *File) AppendEndingAt(end int64) (Append, error) {
	ft, err := f.footer(end)
	if err != nil {
		return Append{}, err
	}
	return ft.info(), nil
}

// VersionAt returns the first version written at or after t, or 0 if none.
func (f *File) VersionAt(t time.Time) (uint64, error) {
	ft, err := f.footerAtTime(t)
	if err != nil || ft == nil {
		return 0, err
	}
	return ft.version, nil
}

// Root returns the root object at version v (1 ≤ v ≤ Version()).
func (f *File) Root(v uint64) (*Object, error) {
	ft, err := f.footerAt(v)
	if err != nil {
		return nil, err
	}
	return f.rootOf(ft), nil
}

func (f *File) rootOf(ft *footer) *Object {
	leaves := ft.end
	if ft.version > 1 {
		leaves = 0 // found lazily: the root's leaves are in version 1
	}
	return &Object{f: f, typ: f.schema.Root, node: ref{end: ft.end, i: 1}, leaves: leaves, version: ft.version}
}

// Get resolves a path at version v. Negative item numbers count back from the
// latest item. The result is an *Object, a *List or a Leaf.
func (f *File) Get(path string, v uint64) (any, error) {
	root, err := f.Root(v)
	if err != nil {
		return nil, err
	}
	return root.Get(path)
}

var errNoRoot = errors.New("trak: the file has no root append")

func bitsTrailing(x uint64) int {
	n := 0
	for x&1 == 0 {
		x >>= 1
		n++
	}
	return n
}

// footerCache keeps recently parsed footers, in two generations.
type footerCache struct {
	max      int
	cur, old map[int64]*footer
}

func newFooterCache(max int) footerCache {
	return footerCache{max: max, cur: map[int64]*footer{}, old: map[int64]*footer{}}
}

func (c *footerCache) get(end int64) *footer {
	if ft := c.cur[end]; ft != nil {
		return ft
	}
	if ft := c.old[end]; ft != nil {
		c.put(end, ft)
		return ft
	}
	return nil
}

func (c *footerCache) put(end int64, ft *footer) {
	if len(c.cur) >= c.max {
		c.old, c.cur = c.cur, make(map[int64]*footer, c.max)
	}
	c.cur[end] = ft
}
