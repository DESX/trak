package trak

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"time"
)

// Options configure a Writer.
type Options struct {
	Policy Policy
	Sync   bool // fsync after every append, and the directory on create
}

// Writer appends to a trak file. It holds the file's lock until Close. It is
// safe for one goroutine at a time.
type Writer struct {
	f       *File
	fh      *os.File
	opt     Options
	buf     []byte
	body    []byte
	lens    []byte
	scratch []byte
}

// Create creates a new file with the schema and writes version 1, the root's
// leaves.
func Create(name, schema string, root map[string]any, t time.Time, opt *Options) (*Writer, error) {
	s, err := ParseSchema(schema)
	if err != nil {
		return nil, err
	}
	if len(schema) == 0 || schema[len(schema)-1] != '\n' {
		return nil, &SchemaError{Msg: "the schema text must end with LF"}
	}
	fh, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	w := &Writer{fh: fh}
	if opt != nil {
		w.opt = *opt
	}
	fail := func(err error) (*Writer, error) {
		fh.Close()
		os.Remove(name)
		return nil, err
	}
	if err := lockFile(fh); err != nil {
		return fail(err)
	}
	head := []byte(Magic + "##[ schema ]##")
	head = AppendVLQ(head, uint64(len(schema)))
	head = append(AppendVLQ(head, 0), '\n')
	head = append(head, schema...)
	// A File over just the header, so the root append can be planned.
	w.f = &File{r: fh, schema: s, schemaEnd: int64(len(head)), size: int64(len(head)), cache: newFooterCache(1 << 14)}
	p := planRoot(s)
	rec, err := w.encode(p, root, t, int64(len(head)))
	if err != nil {
		return fail(err)
	}
	if _, err := fh.Write(append(head, rec...)); err != nil {
		return fail(err)
	}
	if err := w.committed(int64(len(head) + len(rec))); err != nil {
		return fail(err)
	}
	if w.opt.Sync {
		if err := syncDir(name); err != nil {
			return fail(err)
		}
	}
	return w, nil
}

// OpenWriter opens an existing file for appending. A torn tail is truncated.
func OpenWriter(name string, opt *Options) (*Writer, error) {
	fh, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	if err := lockFile(fh); err != nil {
		fh.Close()
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
	if f.latest == nil {
		fh.Close()
		return nil, formatErr(f.schemaEnd, "the file has no root append")
	}
	if f.size < st.Size() {
		if err := fh.Truncate(f.size); err != nil {
			fh.Close()
			return nil, err
		}
	}
	w := &Writer{f: f, fh: fh}
	if opt != nil {
		w.opt = *opt
	}
	return w, nil
}

// File returns a reader that sees every append written so far.
func (w *Writer) File() *File { return w.f }

// Close releases the lock and closes the file.
func (w *Writer) Close() error {
	if w.fh == nil {
		return ErrClosed
	}
	err := w.fh.Close()
	w.fh = nil
	return err
}

// Append adds one item to the event list at list (such as
// "sessions.12.turns") and returns the append. Object items are
// map[string]any, with nested maps for nested objects; leaves are strings,
// []byte, or numbers for num.
func (w *Writer) Append(list string, value any, t time.Time) (Append, error) {
	if w.fh == nil {
		return Append{}, ErrClosed
	}
	p, err := w.f.planAppend(w.f.latest, segments(list))
	if err != nil {
		return Append{}, err
	}
	start := w.f.size
	rec, err := w.encode(p, value, t, start)
	if err != nil {
		return Append{}, err
	}
	if _, err := w.fh.WriteAt(rec, start); err != nil {
		return Append{}, err
	}
	if err := w.committed(start + int64(len(rec))); err != nil {
		return Append{}, err
	}
	return w.f.latest.info(), nil
}

// committed makes the append ending at end the latest.
func (w *Writer) committed(end int64) error {
	if w.opt.Sync {
		if err := w.fh.Sync(); err != nil {
			return err
		}
	}
	ft, err := w.f.footer(end)
	if err != nil {
		return fmt.Errorf("trak: the writer wrote an unreadable footer: %w", err)
	}
	w.f.latest, w.f.size = ft, end
	return nil
}

// encode builds the body and footer of an append whose body starts at start.
func (w *Writer) encode(p *plan, value any, t time.Time, start int64) ([]byte, error) {
	var v uint64 = 1
	var vjump int64
	if w.f.latest != nil {
		prev := w.f.latest
		v = prev.version + 1
		ms := t.UnixMilli()
		if ms < prev.ms {
			return nil, valueErr("timestamp %s is before the latest append's %s", FormatTimestamp(t), prev.stamp)
		}
		jv := Jump(v)
		switch {
		case jv == 0:
		case jv == prev.version:
			vjump = prev.end
		default:
			ft, err := w.f.footerAt(jv)
			if err != nil {
				return nil, err
			}
			vjump = ft.end
		}
	}
	w.body, w.lens = w.body[:0], w.lens[:0]
	var err error
	if p.item.Kind == KindObject {
		m, ok := value.(map[string]any)
		if !ok && value != nil {
			return nil, valueErr("%s needs a map[string]any, not %T", joinSegs(p.segs), value)
		}
		err = w.object(p.item.Type, m, "")
	} else {
		err = w.leaf(p.item, p.item.Name, value)
	}
	if err != nil {
		return nil, err
	}
	b := append(w.buf[:0], w.body...)
	bodyLen := len(b)
	b = append(b, "==[ "...)
	for i, s := range p.segs {
		if i > 0 {
			b = append(b, '.')
		}
		b = append(b, s...)
	}
	b = append(b, ' ')
	b = strconv.AppendUint(b, v, 10)
	b = append(b, ' ')
	b = appendStamp(b, t.UnixMilli())
	b = append(b, " ]=="...)
	b = AppendVLQ(b, uint64(bodyLen))
	b = appendRef(b, start, vjump)
	b = append(b, 'A') // no extensions
	b = append(b, w.lens...)
	for i := range p.nodes {
		b = w.node(b, &p.nodes[i], start)
	}
	b = append(b, '\n')
	w.buf = b
	return b, nil
}

// node appends one node in the policy's form.
func (w *Writer) node(b []byte, n *planNode, start int64) []byte {
	switch n.kind {
	case nodeLeafItem:
		return b
	case nodeItem, nodeNested:
		b = AppendVLQ(b, n.k)
		b = appendPtr(b, start, n.prev)
		b = appendPtr(b, start, n.jump)
		if n.kind == nodeItem {
			if n.leaves == selfEnd {
				b = AppendVLQ(b, 0)
			} else {
				b = appendRef(b, start, n.leaves)
			}
		}
	}
	full := w.form(w.scratch[:0], n, start, nil, 0)
	if w.opt.Policy == Delta && n.old != nil {
		base, baseRef := n.old, n.oldRef
		if !base.full {
			baseRef = base.base
			var err error
			if base, _, err = w.f.resolve(baseRef); err != nil {
				base = nil
			}
		}
		if base != nil && base.full {
			var bitmap uint64
			for j, e := range n.entries {
				be, _ := base.written(j)
				if e != be {
					bitmap |= 1 << uint(j)
				}
			}
			if bitmap != 0 {
				delta := w.form(full[len(full):], n, start, &baseRef, bitmap)
				if 2*len(delta) < len(full) {
					w.scratch = full
					return append(b, delta...)
				}
			}
		}
	}
	w.scratch = full
	return append(b, full...)
}

// form appends n's entries, in full form, or as a delta when base is set.
func (w *Writer) form(b []byte, n *planNode, start int64, base *ref, bitmap uint64) []byte {
	one := func(j int) {
		e := n.entries[j]
		if n.kind != nodeTrie && n.typ.slots[j].Kind == KindEvent {
			b = AppendVLQ(b, e.count)
		}
		b = appendPtr(b, start, e.ptr)
	}
	if base == nil {
		b = AppendVLQ(AppendVLQ(b, 0), uint64(len(n.entries)))
		for j := range n.entries {
			one(j)
		}
		return b
	}
	b = appendPtr(AppendVLQ(b, 1), start, *base)
	b = AppendVLQ(b, bitmap)
	for bm := bitmap; bm != 0; bm &= bm - 1 {
		one(bitsTrailing(bm))
	}
	return b
}

// object encodes an object's leaves, depth first.
func (w *Writer) object(t *Type, m map[string]any, prefix string) error {
	used := 0
	for _, fld := range t.Fields {
		val, has := m[fld.Name]
		if has {
			used++
		}
		if has && val == nil {
			has = false
		}
		rel := prefix + fld.Name
		switch {
		case fld.Kind == KindEvent:
			if has {
				return valueErr("%s is an event list; append its items separately", rel)
			}
		case fld.Kind == KindObject:
			if !has {
				if fld.Optional {
					w.lens = AppendVLQ(w.lens, 0)
					continue
				}
				if len(fld.Type.Fields) > 0 && !onlyEvents(fld.Type) {
					return valueErr("%s is missing", rel)
				}
			}
			sub, ok := val.(map[string]any)
			if has && !ok {
				return valueErr("%s needs a map[string]any, not %T", rel, val)
			}
			if fld.Optional {
				w.lens = AppendVLQ(w.lens, 1)
			}
			if err := w.object(fld.Type, sub, rel+"."); err != nil {
				return err
			}
		case !has:
			if !fld.Optional {
				return valueErr("%s is missing", rel)
			}
			w.lens = AppendVLQ(w.lens, 0)
		default:
			if err := w.leaf(fld, rel, val); err != nil {
				return err
			}
		}
	}
	if used < len(m) {
		for k := range m {
			if t.Field(k) == nil {
				return valueErr("no field %s%s", prefix, k)
			}
		}
	}
	return nil
}

func onlyEvents(t *Type) bool {
	for _, f := range t.Fields {
		if f.Kind != KindEvent && (f.Kind != KindObject || !onlyEvents(f.Type)) {
			return false
		}
	}
	return true
}

// leaf encodes one present leaf.
func (w *Writer) leaf(fld *Field, rel string, val any) error {
	var s []byte
	switch x := val.(type) {
	case string:
		s = []byte(x)
	case []byte:
		s = x
	case int:
		s = strconv.AppendInt(nil, int64(x), 10)
	case int64:
		s = strconv.AppendInt(nil, x, 10)
	case uint64:
		s = strconv.AppendUint(nil, x, 10)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return valueErr("%s: %v is not a num", rel, x)
		}
		s = strconv.AppendFloat(nil, x, 'g', -1, 64)
	case nil:
		return valueErr("%s is missing", rel)
	default:
		return valueErr("%s: can't write a %T", rel, val)
	}
	if fld.Kind != KindNum {
		if _, ok := val.(string); !ok {
			if _, ok := val.([]byte); !ok {
				return valueErr("%s is a %s, not a number", rel, fld.Kind)
			}
		}
	}
	switch fld.Kind {
	case KindNum:
		if !ValidNum(s) {
			return valueErr("%s: %q is not a num", rel, s)
		}
	case KindEnum:
		ok := false
		for _, word := range fld.Words {
			if string(s) == word {
				ok = true
				break
			}
		}
		if !ok {
			return valueErr("%s: %q is not one of %v", rel, s, fld.Words)
		}
	case KindPath:
		if err := w.checkPath(string(s)); err != nil {
			return valueErr("%s: %v", rel, err)
		}
	}
	if fld.Optional {
		w.lens = AppendVLQ(w.lens, 1)
	}
	w.lens = AppendVLQ(w.lens, uint64(len(s)))
	if fld.Block {
		w.body = append(w.body, "--[ "...)
		w.body = append(w.body, rel...)
		w.body = append(w.body, " ]--\n"...)
		w.body = append(w.body, s...)
		w.body = append(w.body, "\n--[ /"...)
		w.body = append(w.body, rel...)
		w.body = append(w.body, " ]--\n"...)
	} else {
		w.body = append(w.body, rel...)
		w.body = append(w.body, ": "...)
		w.body = append(w.body, s...)
		w.body = append(w.body, '\n')
	}
	return nil
}

// checkPath requires a stored path to name a value that exists.
func (w *Writer) checkPath(p string) error {
	segs := segments(p)
	if len(segs) == 0 {
		return fmt.Errorf("empty path")
	}
	for _, s := range segs {
		if !IsName(s) && !isIndex(s) {
			return fmt.Errorf("bad path %q", p)
		}
	}
	if w.f.latest == nil {
		return fmt.Errorf("path %s: nothing exists yet", p)
	}
	v, err := w.f.rootOf(w.f.latest).Get(p)
	if err != nil {
		return err
	}
	if l, ok := v.(Leaf); ok && !l.Present {
		return fmt.Errorf("path %s names an absent value", p)
	}
	return nil
}

// ValidNum reports whether s matches the num grammar (SPEC §2).
func ValidNum(s []byte) bool {
	i := 0
	if i < len(s) && s[i] == '-' {
		i++
	}
	digits := func() int {
		j := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		return i - j
	}
	if i < len(s) && s[i] == '0' {
		i++
	} else if digits() == 0 {
		return false
	}
	if i < len(s) && s[i] == '.' {
		i++
		if digits() == 0 {
			return false
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if digits() == 0 {
			return false
		}
	}
	return i == len(s)
}
