package trak

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	magic        = "trak 1.0\n"
	writeVersion = "1.0"
	unset        = -(1 << 62)
)

// Options configures a Writer.
type Options struct {
	// Overwrite lets Create replace an existing file.
	Overwrite bool
	// Sync calls fsync after each object.
	Sync bool
}

type spineNode struct {
	node uint64
	end  int64
}

type typeState struct {
	schema *Schema
	n      uint64
	// spine holds node n, J(n), J(J(n)), ..., 0 with the latest last.
	spine  []spineNode
	lastMs int64
	hasTs  bool
}

// Writer appends schemas and events to a trak file. It holds an exclusive
// advisory lock on the file until Close.
type Writer struct {
	file          *os.File
	sync          bool
	offset        int64
	types         map[string]*typeState
	order         []*typeState
	lastSchemaEnd int64
	cov           []int64
	seq           int64
	buf           []byte
	now           func() time.Time
}

// Create creates a new trak file and writes its magic line. It fails if the
// file exists, unless opts.Overwrite is set.
func Create(path string, opts Options) (*Writer, error) {
	flags := os.O_RDWR | os.O_CREATE
	if !opts.Overwrite {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o666)
	if err != nil {
		return nil, err
	}
	w := newWriter(f, opts)
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	if err := w.writeObject([]byte(magic)); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

// OpenAppend opens an existing trak file for appending. A torn tail (SPEC §11)
// is truncated.
func OpenAppend(path string, opts Options) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o666)
	if err != nil {
		return nil, err
	}
	w := newWriter(f, opts)
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	st, err := f.Stat()
	if err == nil && st.Size() == 0 {
		err = w.writeObject([]byte(magic))
	} else if err == nil {
		err = w.restore(path, st.Size())
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

func newWriter(f *os.File, opts Options) *Writer {
	return &Writer{file: f, sync: opts.Sync, types: map[string]*typeState{}, cov: []int64{-1}, now: time.Now}
}

// Flush makes written objects durable with fsync. Objects are handed to the
// OS as they are written, one write call each.
func (w *Writer) Flush() error {
	if w.file == nil {
		return ErrClosed
	}
	return w.file.Sync()
}

// Close releases the lock and closes the file.
func (w *Writer) Close() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *Writer) writeObject(data []byte) error {
	if w.file == nil {
		return ErrClosed
	}
	n, err := w.file.WriteAt(data, w.offset)
	w.offset += int64(n)
	if err != nil {
		return err
	}
	if w.sync {
		return w.file.Sync()
	}
	return nil
}

// Types returns the defined schemas in index order.
func (w *Writer) Types() []*Schema {
	out := make([]*Schema, len(w.order))
	for i, t := range w.order {
		out[i] = t.schema
	}
	return out
}

// Define parses and validates a definition, then writes its schema object.
// A final LF is added if the definition lacks one.
func (w *Writer) Define(name, definition string) (*Schema, error) {
	if !validTypeName(name) {
		return nil, &SchemaError{Msg: fmt.Sprintf("bad type name %q", name)}
	}
	if strings.HasPrefix(name, "trak.") {
		return nil, &SchemaError{Msg: "type names starting with 'trak.' are reserved"}
	}
	if w.types[name] != nil {
		return nil, &SchemaError{Msg: fmt.Sprintf("type %q is already defined", name)}
	}
	if !strings.HasSuffix(definition, "\n") {
		definition += "\n"
	}
	index := len(w.order) + 1
	schema, err := buildSchema(name, index, writeVersion, definition, true)
	if err != nil {
		return nil, err
	}
	start := w.offset
	var prev uint64
	if len(w.order) > 0 {
		prev = uint64(start - w.lastSchemaEnd + 1)
	}
	obj := append(w.buf[:0], definition...)
	obj = appendSchemaFooter(obj, name, index, writeVersion, []uint64{uint64(len(definition)), prev, 0})
	w.buf = obj
	if err := w.writeObject(obj); err != nil {
		return nil, err
	}
	w.lastSchemaEnd = w.offset
	t := &typeState{schema: schema, spine: []spineNode{{0, w.offset}}}
	w.types[name] = t
	w.order = append(w.order, t)
	w.cov = append(w.cov, -1)
	return schema, nil
}

// Write writes one event timestamped now and returns its number n. If now is
// earlier than the type's last timestamp, that timestamp is reused.
func (w *Writer) Write(typ string, value any) (int64, error) {
	return w.write(typ, value, time.Time{}, false)
}

// WriteAt writes one event with the given timestamp, truncated to the
// millisecond. A timestamp earlier than the type's last one is an error.
func (w *Writer) WriteAt(typ string, value any, ts time.Time) (int64, error) {
	return w.write(typ, value, ts, true)
}

func (w *Writer) write(typ string, value any, ts time.Time, explicit bool) (int64, error) {
	t := w.types[typ]
	if t == nil {
		return 0, valueErr("type %q is not defined", typ)
	}
	if t.schema.Root == nil {
		return 0, &SchemaError{Msg: fmt.Sprintf("can't write %s: %v", typ, t.schema.Err)}
	}
	var ms int64
	if explicit {
		ms = toMillis(ts)
		if t.hasTs && ms < t.lastMs {
			return 0, valueErr("timestamp %s is earlier than %s's last event", FormatTimestamp(fromMillis(ms)), typ)
		}
	} else {
		ms = toMillis(w.now())
		if t.hasTs && ms < t.lastMs {
			ms = t.lastMs
		}
	}
	body, values, err := encodeValue(t.schema.Root, value)
	if err != nil {
		return 0, err
	}
	n := t.n + 1
	j := Jump(n)
	start := w.offset
	stepEnd := t.spine[len(t.spine)-1].end
	var jumpEnd int64
	for i := len(t.spine) - 1; i >= 0; i-- {
		if t.spine[i].node == j {
			jumpEnd = t.spine[i].end
			break
		}
	}
	index := t.schema.Index
	best := -1
	for i, c := range w.cov {
		if i != index && (best < 0 || c < w.cov[best]) {
			best = i
		}
	}
	dirEnd := w.lastSchemaEnd
	if best > 0 {
		s := w.order[best-1].spine
		dirEnd = s[len(s)-1].end
	}
	vlqs := make([]uint64, 0, 6+len(values))
	vlqs = append(vlqs, uint64(len(body)), uint64(start-stepEnd+1), uint64(start-jumpEnd+1),
		uint64(start-dirEnd+1), uint64(best), 0)
	vlqs = append(vlqs, values...)
	obj := append(w.buf[:0], body...)
	obj = appendEventFooter(obj, typ, n, FormatTimestamp(fromMillis(ms)), vlqs)
	w.buf = obj
	if err := w.writeObject(obj); err != nil {
		return 0, err
	}
	for t.spine[len(t.spine)-1].node > j {
		t.spine = t.spine[:len(t.spine)-1]
	}
	t.spine = append(t.spine, spineNode{n, w.offset})
	t.n, t.lastMs, t.hasTs = n, ms, true
	w.cov[index], w.cov[best] = w.seq, w.seq
	w.seq++
	return int64(n), nil
}

func (w *Writer) restore(path string, size int64) error {
	r, err := Open(path)
	if err != nil {
		return err
	}
	end := r.End()
	err = w.restoreFrom(r)
	// Close before truncating: Windows can't truncate a mapped file.
	if cerr := r.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if end < size {
		if err := w.file.Truncate(end); err != nil {
			return err
		}
	}
	w.offset = end
	return nil
}

func (w *Writer) restoreFrom(r *Reader) error {
	for _, sf := range r.schemaFts {
		t := &typeState{schema: r.byName[sf.name], spine: []spineNode{{0, sf.end}}}
		w.types[sf.name] = t
		w.order = append(w.order, t)
		w.lastSchemaEnd = sf.end
	}
	for _, t := range w.order {
		top, err := r.latestFooter(t.schema)
		if err != nil {
			return err
		}
		spine := []spineNode{{top.node(), top.end}}
		for f := top; f.node() > 0; {
			if f, err = r.navigate(f, Jump(f.node())); err != nil {
				return err
			}
			spine = append(spine, spineNode{f.node(), f.end})
		}
		for i, j := 0, len(spine)-1; i < j; i, j = i+1, j-1 {
			spine[i], spine[j] = spine[j], spine[i]
		}
		t.spine, t.n = spine, top.node()
		if !top.schema {
			ts, err := ParseTimestamp(top.stamp)
			if err != nil {
				return formatErr(top.fstart, "%v", err)
			}
			t.lastMs, t.hasTs = toMillis(ts), true
		}
	}
	// Rebuild coverage approximately from the last ~2K events.
	w.cov = make([]int64, len(w.order)+1)
	for i := range w.cov {
		w.cov[i] = unset
	}
	f, err := r.last()
	seq := int64(-1)
	for budget := 2*len(w.order) + 2; err == nil && f != nil && budget > 0; f, err = r.prev(f) {
		if f.schema {
			continue
		}
		for _, i := range []uint64{uint64(w.types[f.name].schema.Index), f.vlqs[4]} {
			if i < uint64(len(w.cov)) && w.cov[i] == unset {
				w.cov[i] = seq
			}
		}
		seq--
		budget--
	}
	return err
}

var _ io.Closer = (*Writer)(nil)
