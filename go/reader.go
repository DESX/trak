package trak

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

const footerCacheSize = 4096

// Reader is a read-only, memory-mapped view of a trak file. It takes no lock.
type Reader struct {
	path      string
	file      *os.File
	buf       []byte
	size      int64
	magicEnd  int64
	end       int64
	version   string
	cache     map[int64]*footer // recent footers, by end offset
	oldCache  map[int64]*footer // the previous generation; hits move back to cache
	latest    map[int]*footer
	schemas   []*Schema
	byName    map[string]*Schema
	schemaFts []*footer

	strings map[string]string // interned type names and leaf paths

	// TailErr says why the last complete footer line after End is invalid,
	// when there is one (see Check).
	TailErr error
	// FooterReads counts footer lookups, for navigation bound tests.
	FooterReads int
}

// Open opens a trak file for reading. It validates the magic line and finds
// the last valid footer (SPEC §11).
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r := &Reader{path: path, file: f, cache: map[int64]*footer{}, strings: map[string]string{}}
	if err := r.load(); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

func (r *Reader) load() error {
	st, err := r.file.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	if size == 0 {
		return formatErr(0, "empty file: missing magic line")
	}
	buf, err := mmapFile(r.file, size)
	if err != nil {
		return err
	}
	if r.buf != nil {
		_ = munmap(r.buf)
	}
	r.buf, r.size = buf, size
	head := buf
	if len(head) > 64 {
		head = head[:64]
	}
	nl := bytes.IndexByte(head, '\n')
	if nl < 0 || !bytes.HasPrefix(head, []byte("trak ")) || !canonicalVersion(head[5:nl]) {
		return formatErr(0, "bad magic line, expected 'trak MAJOR.MINOR'")
	}
	version := string(head[5:nl])
	major, _, _ := bytes.Cut(head[5:nl], []byte("."))
	if string(major) != "1" {
		return &UnsupportedVersionError{Version: string(major)}
	}
	r.version = version
	r.magicEnd = int64(nl) + 1
	r.end, r.schemaFts = r.recover(size)
	r.latest = map[int]*footer{}
	r.schemas = make([]*Schema, len(r.schemaFts))
	r.byName = make(map[string]*Schema, len(r.schemaFts))
	for i, f := range r.schemaFts {
		r.schemas[i], _ = buildSchema(f.name, i+1, f.stamp, string(r.buf[f.start:f.fstart]), false)
		r.byName[f.name] = r.schemas[i]
	}
	return nil
}

// Refresh picks up objects appended since Open. It reports whether anything
// changed.
func (r *Reader) Refresh() (bool, error) {
	if r.file == nil {
		return false, ErrClosed
	}
	st, err := r.file.Stat()
	if err != nil {
		return false, err
	}
	if st.Size() == r.size {
		return false, nil
	}
	oldEnd := r.end
	for _, c := range []map[int64]*footer{r.cache, r.oldCache} {
		for end := range c {
			if end > oldEnd {
				delete(c, end)
			}
		}
	}
	if err := r.load(); err != nil {
		return false, err
	}
	return r.end != oldEnd, nil
}

// Close unmaps and closes the file. Byte slices from Event.Raw become invalid.
func (r *Reader) Close() error {
	var err error
	if r.buf != nil {
		err = munmap(r.buf)
		r.buf = nil
	}
	if r.file != nil {
		if e := r.file.Close(); err == nil {
			err = e
		}
		r.file = nil
	}
	return err
}

// Version is the magic line's MAJOR.MINOR.
func (r *Reader) Version() string { return r.version }

// End is the offset just past the last valid object. Bytes after it are a
// torn tail.
func (r *Reader) End() int64 { return r.end }

// Size is the file size when last loaded.
func (r *Reader) Size() int64 { return r.size }

// Bytes returns the mapped file. It is valid until Close or Refresh.
func (r *Reader) Bytes() []byte { return r.buf }

// Types returns every schema in type index order.
func (r *Reader) Types() []*Schema { return append([]*Schema(nil), r.schemas...) }

// Schema returns the schema of the named type.
func (r *Reader) Schema(name string) (*Schema, error) {
	s, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("%w: no type named %q", ErrNotFound, name)
	}
	return s, nil
}

// Table returns the events of the named type.
func (r *Reader) Table(name string) (*Table, error) {
	s, err := r.Schema(name)
	if err != nil {
		return nil, err
	}
	return &Table{r: r, Schema: s}, nil
}

// Events calls yield for every event in file order, until yield returns false.
func (r *Reader) Events(yield func(*Event) bool) error {
	var footers []*footer
	f, err := r.last()
	for err == nil && f != nil {
		footers = append(footers, f)
		f, err = r.prev(f)
	}
	if err != nil {
		return err
	}
	for i := len(footers) - 1; i >= 0; i-- {
		f := footers[i]
		if f.schema {
			continue
		}
		s := r.byName[f.name]
		if s == nil {
			return formatErr(f.fstart, "event of type %q has no schema", f.name)
		}
		if !yield(&Event{r: r, Schema: s, f: f}) {
			return nil
		}
	}
	return nil
}

// EventsBackward calls yield for every event from the last one back to the
// first, until yield returns false.
func (r *Reader) EventsBackward(yield func(*Event) bool) error {
	f, err := r.last()
	for err == nil && f != nil {
		if !f.schema {
			s := r.byName[f.name]
			if s == nil {
				return formatErr(f.fstart, "event of type %q has no schema", f.name)
			}
			if !yield(&Event{r: r, Schema: s, f: f}) {
				return nil
			}
		}
		f, err = r.prev(f)
	}
	return err
}

// intern returns a string equal to b, allocating only the first time. The
// set is bounded so a file full of distinct paths can't grow it forever.
func (r *Reader) intern(b []byte) string {
	if s, ok := r.strings[string(b)]; ok {
		return s
	}
	s := string(b)
	if len(r.strings) < 1<<16 {
		r.strings[s] = s
	}
	return s
}

// --- footers and navigation ---------------------------------------------------

func (r *Reader) footer(end int64) (*footer, error) {
	r.FooterReads++
	if f, ok := r.cache[end]; ok {
		return f, nil
	}
	if f, ok := r.oldCache[end]; ok {
		r.remember(end, f)
		return f, nil
	}
	if end <= r.magicEnd || end > r.size || r.buf[end-1] != '\n' {
		return nil, formatErr(end, "pointer doesn't land on the end of an object")
	}
	fstart := int64(bytes.LastIndexByte(r.buf[r.magicEnd-1:end-1], '\n')) + r.magicEnd
	f, err := parseFooter(r.buf, fstart, end, r.magicEnd, r.intern)
	if err != nil {
		return nil, err
	}
	r.remember(end, f)
	return f, nil
}

// remember caches a footer. When the cache fills it becomes the old
// generation, so footers still in use (like the upper levels of the jump
// structure) survive by being promoted on their next hit.
func (r *Reader) remember(end int64, f *footer) {
	if len(r.cache) >= footerCacheSize {
		r.oldCache, r.cache = r.cache, make(map[int64]*footer, footerCacheSize)
	}
	r.cache[end] = f
}

func (r *Reader) last() (*footer, error) {
	if r.end == r.magicEnd {
		return nil, nil
	}
	return r.footer(r.end)
}

func (r *Reader) prev(f *footer) (*footer, error) {
	if f.start == r.magicEnd {
		return nil, nil
	}
	return r.footer(f.start)
}

func (r *Reader) follow(f *footer, pointer uint64) (*footer, error) {
	end, err := f.target(pointer)
	if err != nil {
		return nil, err
	}
	return r.footer(end)
}

// recover finds the end of the last valid footer (SPEC §11) and the schema
// chain it sees.
func (r *Reader) recover(size int64) (int64, []*footer) {
	r.TailErr = nil
	pos := size
	if pos > r.magicEnd && r.buf[pos-1] != '\n' {
		pos = int64(bytes.LastIndexByte(r.buf[r.magicEnd-1:pos], '\n')) + r.magicEnd
	}
	for pos > r.magicEnd {
		fstart := int64(bytes.LastIndexByte(r.buf[r.magicEnd-1:pos-1], '\n')) + r.magicEnd
		line := r.buf[fstart:pos]
		if bytes.HasPrefix(line, schemaOpen) || bytes.HasPrefix(line, eventOpen) {
			chain, err := r.validateTail(pos)
			if err == nil {
				return pos, chain
			}
			delete(r.cache, pos)
			delete(r.oldCache, pos)
			if r.TailErr == nil {
				r.TailErr = err
			}
		}
		pos = fstart
	}
	return r.magicEnd, nil
}

// validateTail checks that the footer ending at end is valid by SPEC §11: it
// parses, its type is known, and its VLQ count and walk match its schema.
func (r *Reader) validateTail(end int64) ([]*footer, error) {
	f, err := r.footer(end)
	if err != nil {
		return nil, err
	}
	chain, err := r.schemaChain(f)
	if err != nil || f.schema {
		return chain, err
	}
	for _, s := range chain {
		if s.name != f.name {
			continue
		}
		schema, _ := buildSchema(s.name, int(s.num), s.stamp, string(r.buf[s.start:s.fstart]), false)
		if schema.Root != nil {
			if _, err := newWalk(schema.Root, f.values(), f.start, f.fstart); err != nil {
				return nil, err
			}
		}
		return chain, nil
	}
	return nil, formatErr(f.fstart, "event of type %q has no schema", f.name)
}

// schemaChain returns every schema footer visible from f, in index order:
// walk back to a schema or an event with dir type 0, then follow
// previous-schema pointers (SPEC §7).
func (r *Reader) schemaChain(f *footer) ([]*footer, error) {
	g := f
	var err error
	for g != nil && !g.schema {
		if g.vlqs[4] == 0 {
			if g, err = r.follow(g, g.vlqs[3]); err != nil {
				return nil, err
			}
			if !g.schema {
				return nil, formatErr(g.end, "directory pointer for the schema chain misses a schema")
			}
			break
		}
		if g, err = r.prev(g); err != nil {
			return nil, err
		}
	}
	if g == nil {
		return nil, formatErr(f.fstart, "event with no schema before it")
	}
	chain := []*footer{g}
	for g.vlqs[1] != 0 {
		if g, err = r.follow(g, g.vlqs[1]); err != nil {
			return nil, err
		}
		if !g.schema {
			return nil, formatErr(g.end, "previous-schema pointer misses a schema")
		}
		chain = append(chain, g)
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	names := make(map[string]bool, len(chain))
	for i, s := range chain {
		if s.num != uint64(i+1) {
			return nil, formatErr(s.fstart, "schema %s has index %d, expected %d", s.name, s.num, i+1)
		}
		if names[s.name] {
			return nil, formatErr(s.fstart, "duplicate schema name %s", s.name)
		}
		names[s.name] = true
	}
	return chain, nil
}

// latestFooter returns the latest node of a type: its last event, or its
// schema if it has none.
func (r *Reader) latestFooter(s *Schema) (*footer, error) {
	if f, ok := r.latest[s.Index]; ok {
		return f, nil
	}
	f, err := r.last()
	for err == nil && f != nil {
		if f.name == s.Name {
			break
		}
		if !f.schema && f.vlqs[4] == uint64(s.Index) {
			f, err = r.follow(f, f.vlqs[3])
			if err == nil && f.name != s.Name {
				err = formatErr(f.end, "directory pointer leads to the wrong type")
			}
			break
		}
		f, err = r.prev(f)
	}
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, formatErr(r.end, "type %s not found", s.Name)
	}
	r.latest[s.Index] = f
	return f, nil
}

// navigate walks from node f down to node m of the same type (SPEC §7).
func (r *Reader) navigate(f *footer, m uint64) (*footer, error) {
	name := f.name
	for f.node() > m {
		j := Jump(f.node())
		ptr, want := f.vlqs[1], f.node()-1
		if j >= m {
			ptr, want = f.vlqs[2], j
		}
		next, err := r.follow(f, ptr)
		if err != nil {
			return nil, err
		}
		if next.name != name || next.node() != want {
			return nil, formatErr(f.fstart, "pointer leads to the wrong node (wanted %s %d)", name, want)
		}
		f = next
	}
	return f, nil
}

// Table is the events of one type, numbered from 1.
type Table struct {
	r      *Reader
	Schema *Schema
}

// Name is the type name.
func (t *Table) Name() string { return t.Schema.Name }

// Count returns the number of events.
func (t *Table) Count() (int64, error) {
	f, err := t.r.latestFooter(t.Schema)
	if err != nil {
		return 0, err
	}
	return int64(f.node()), nil
}

// Latest returns the last event, or nil when there are none.
func (t *Table) Latest() (*Event, error) {
	f, err := t.r.latestFooter(t.Schema)
	if err != nil || f.schema {
		return nil, err
	}
	return &Event{r: t.r, Schema: t.Schema, f: f}, nil
}

func (t *Table) footerAt(n int64) (*footer, error) {
	top, err := t.r.latestFooter(t.Schema)
	if err != nil {
		return nil, err
	}
	if n < 1 || uint64(n) > top.node() {
		return nil, fmt.Errorf("%w: %s has no event %d", ErrNotFound, t.Schema.Name, n)
	}
	return t.r.navigate(top, uint64(n))
}

// Get returns event n.
func (t *Table) Get(n int64) (*Event, error) {
	f, err := t.footerAt(n)
	if err != nil {
		return nil, err
	}
	return &Event{r: t.r, Schema: t.Schema, f: f}, nil
}

// Range calls yield for events a to b inclusive, descending when a > b,
// until yield returns false.
func (t *Table) Range(a, b int64, yield func(*Event) bool) error {
	count, err := t.Count()
	if err != nil {
		return err
	}
	for _, x := range []int64{a, b} {
		if x < 1 || x > count {
			return fmt.Errorf("%w: %s has no event %d", ErrNotFound, t.Schema.Name, x)
		}
	}
	if a > b {
		f, err := t.footerAt(a)
		for err == nil {
			if !yield(&Event{r: t.r, Schema: t.Schema, f: f}) || int64(f.num) == b {
				return nil
			}
			f, err = t.r.follow(f, f.vlqs[1])
		}
		return err
	}
	for lo := a; lo <= b; {
		hi := min(b, lo+255)
		f, err := t.footerAt(hi)
		if err != nil {
			return err
		}
		chunk := []*footer{f}
		for int64(f.num) > lo {
			if f, err = t.r.navigate(f, f.num-1); err != nil {
				return err
			}
			chunk = append(chunk, f)
		}
		for i := len(chunk) - 1; i >= 0; i-- {
			if !yield(&Event{r: t.r, Schema: t.Schema, f: chunk[i]}) {
				return nil
			}
		}
		lo = hi + 1
	}
	return nil
}

// AtTime returns the first event with timestamp >= ts, or nil.
func (t *Table) AtTime(ts time.Time) (*Event, error) {
	want := toMillis(ts)
	count, err := t.Count()
	if err != nil {
		return nil, err
	}
	lo, hi := int64(1), count+1
	for lo < hi {
		mid := lo + (hi-lo)/2
		f, err := t.footerAt(mid)
		if err != nil {
			return nil, err
		}
		at, err := ParseTimestamp(f.stamp)
		if err != nil {
			return nil, formatErr(f.fstart, "%v", err)
		}
		if toMillis(at) >= want {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	if lo > count {
		return nil, nil
	}
	return t.Get(lo)
}

// Event is one event. Values are decoded lazily on first access.
type Event struct {
	r      *Reader
	Schema *Schema
	f      *footer
	w      *walk
	index  map[string]*leafRec
}

// Type is the type name.
func (e *Event) Type() string { return e.f.name }

// N is the per-type event number, from 1.
func (e *Event) N() int64 { return int64(e.f.num) }

// Timestamp is the footer's timestamp text, YYYYMMDDTHHMMSS.sssZ.
func (e *Event) Timestamp() string { return e.f.stamp }

// Time is the timestamp as a time.Time in UTC.
func (e *Event) Time() (time.Time, error) { return ParseTimestamp(e.f.stamp) }

// Offset is the event's first byte in the file.
func (e *Event) Offset() int64 { return e.f.start }

func (e *Event) walk() (*walk, error) {
	if e.w != nil {
		return e.w, nil
	}
	if e.Schema.Root == nil {
		return nil, &SchemaError{Msg: fmt.Sprintf("values of %s are unavailable: %v", e.Schema.Name, e.Schema.Err)}
	}
	w, err := newWalk(e.Schema.Root, e.f.values(), e.f.start, e.f.fstart)
	if err != nil {
		return nil, err
	}
	e.w = w
	return w, nil
}

func (e *Event) path(rec *leafRec) []byte {
	off := rec.pathOff()
	return e.r.buf[off : off+rec.plen]
}

func (e *Event) value(rec *leafRec) []byte { return e.r.buf[rec.voff : rec.voff+rec.length] }

func (e *Event) lookup(path string) (*leafRec, error) {
	if e.index == nil {
		w, err := e.walk()
		if err != nil {
			return nil, err
		}
		e.index = make(map[string]*leafRec, len(w.leaves))
		for i := range w.leaves {
			p := string(e.path(&w.leaves[i]))
			if _, dup := e.index[p]; !dup {
				e.index[p] = &w.leaves[i]
			}
		}
	}
	return e.index[path], nil
}

// Raw returns a zero-copy slice of a leaf's value bytes. ok is false when the
// leaf is absent. Paths are as written in the file, e.g. "tags.1"; when map
// keys contain '.', two leaves can share a path and the first one wins.
func (e *Event) Raw(path string) (value []byte, ok bool, err error) {
	rec, err := e.lookup(path)
	if err != nil || rec == nil {
		return nil, false, err
	}
	return e.value(rec), true, nil
}

// Get returns a leaf's value as a string (num as its decimal text).
func (e *Event) Get(path string) (string, bool, error) {
	v, ok, err := e.Raw(path)
	return string(v), ok, err
}

// Int parses a num leaf as an int64.
func (e *Event) Int(path string) (int64, bool, error) {
	v, ok, err := e.Get(path)
	if !ok || err != nil {
		return 0, ok, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return n, true, err
}

// Float parses a num leaf as a float64.
func (e *Event) Float(path string) (float64, bool, error) {
	v, ok, err := e.Get(path)
	if !ok || err != nil {
		return 0, ok, err
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, true, err
}

// Leaf is one present leaf of an event.
type Leaf struct {
	Path  string
	Value []byte
	Type  *Type
}

// Leaves returns every present leaf in file order.
func (e *Event) Leaves() ([]Leaf, error) {
	w, err := e.walk()
	if err != nil {
		return nil, err
	}
	out := make([]Leaf, len(w.leaves))
	for i := range w.leaves {
		rec := &w.leaves[i]
		out[i] = Leaf{e.r.intern(e.path(rec)), e.value(rec), rec.t}
	}
	return out, nil
}

// MapEntry is one decoded map entry. Known is false when the entry has no
// leaves, so its key isn't stored in the file (SPEC §6); KeyLen is then the
// only thing known about it.
type MapEntry struct {
	Key    string
	Known  bool
	KeyLen int
	Value  any
}

// Value is an event's value in schema order. Object holds an object's
// members in schema order.
type Object struct {
	Keys   []string
	Values []any
}

// Native returns the value as a tree: *Object for objects, []any for lists,
// []MapEntry for maps, string for leaves and nil for absent values.
func (e *Event) Native() (*Object, error) {
	w, err := e.walk()
	if err != nil {
		return nil, err
	}
	return e.native(e.Schema.Root, w.tree, w).(*Object), nil
}

func (e *Event) native(t *Type, node any, w *walk) any {
	switch n := node.(type) {
	case nil:
		return nil
	case leafRef:
		return string(e.value(&w.leaves[n]))
	case objNode:
		obj := &Object{Keys: make([]string, len(n)), Values: make([]any, len(n))}
		for i, m := range t.Members {
			obj.Keys[i] = m.Key
			obj.Values[i] = e.native(m.Type, n[i], w)
		}
		return obj
	case listNode:
		out := make([]any, len(n))
		for i, item := range n {
			out[i] = e.native(t.Elem, item, w)
		}
		return out
	case *mapNode:
		out := make([]MapEntry, len(n.entries))
		for i, ent := range n.entries {
			me := MapEntry{KeyLen: int(ent.klen), Value: e.native(t.Elem, ent.child, w)}
			if ent.first >= 0 {
				off := w.leaves[ent.first].pathOff() + n.plen + 1
				me.Key, me.Known = string(e.r.buf[off:off+int64(ent.klen)]), true
			}
			out[i] = me
		}
		return out
	}
	panic(errors.New("trak: unknown node"))
}
