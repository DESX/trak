package trak

import (
	"bytes"
	"errors"
	"strconv"
)

// Check validates a whole file and returns its number of objects. It fails on
// the first problem with a *FormatError holding the byte offset.
//
// A torn tail (SPEC §11) is allowed, but only when it holds no complete
// footer line: an invalid footer after the last valid one is reported rather
// than ignored.
func Check(path string) (int, error) {
	r, err := Open(path)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	if r.TailErr != nil {
		var fe *FormatError
		if errors.As(r.TailErr, &fe) {
			return 0, formatErr(fe.Offset, "ignored tail has an invalid footer: %s", fe.Msg)
		}
		return 0, formatErr(r.End(), "ignored tail has an invalid footer: %v", r.TailErr)
	}
	c := &checker{r: r, buf: r.buf, types: map[string]*checkType{}}
	return c.run()
}

type checkType struct {
	schema *Schema
	ends   []int64 // end offset of node 0, 1, 2, ...
	lastMs int64
	hasTs  bool
}

type checker struct {
	r             *Reader
	buf           []byte
	types         map[string]*checkType
	order         []*checkType
	lastSchemaEnd int64
}

func (c *checker) run() (int, error) {
	var footers []*footer
	f, err := c.r.last()
	for err == nil && f != nil {
		footers = append(footers, f)
		f, err = c.r.prev(f)
	}
	if err != nil {
		return 0, err
	}
	for i := len(footers) - 1; i >= 0; i-- {
		if footers[i].schema {
			err = c.schema(footers[i])
		} else {
			err = c.event(footers[i])
		}
		if err != nil {
			return 0, err
		}
	}
	return len(footers), nil
}

func (c *checker) pointer(f *footer, value uint64, targetEnd int64, what string) error {
	want := uint64(f.start - targetEnd + 1)
	if value != want {
		return formatErr(f.fstart, "%s pointer is %d, expected %d", what, value, want)
	}
	return nil
}

func (c *checker) schema(f *footer) error {
	if !validTypeName(f.name) {
		return formatErr(f.fstart, "bad type name %q", f.name)
	}
	if c.types[f.name] != nil {
		return formatErr(f.fstart, "duplicate schema name %q", f.name)
	}
	if f.num != uint64(len(c.order)+1) {
		return formatErr(f.fstart, "schema index %d, expected %d", f.num, len(c.order)+1)
	}
	if len(c.order) == 0 {
		if f.vlqs[1] != 0 {
			return formatErr(f.fstart, "first schema's previous-schema pointer must be 0")
		}
	} else if err := c.pointer(f, f.vlqs[1], c.lastSchemaEnd, "previous-schema"); err != nil {
		return err
	}
	body := c.buf[f.start:f.fstart]
	if !bytes.HasSuffix(body, []byte("\n")) {
		return formatErr(f.fstart, "schema body must end with LF")
	}
	major, minor, _ := bytes.Cut([]byte(f.stamp), []byte("."))
	if string(major) != "1" {
		return formatErr(f.fstart, "unsupported schema version %s", f.stamp)
	}
	for i, b := range body {
		if b >= 0x80 {
			return formatErr(f.start+int64(i), "schema is not ASCII")
		}
	}
	schema, err := buildSchema(f.name, int(f.num), f.stamp, string(body), string(minor) == "0")
	if err != nil {
		return formatErr(f.start, "schema %s: %v", f.name, err)
	}
	t := &checkType{schema: schema, ends: []int64{f.end}}
	c.types[f.name] = t
	c.order = append(c.order, t)
	c.lastSchemaEnd = f.end
	return nil
}

func (c *checker) event(f *footer) error {
	t := c.types[f.name]
	if t == nil {
		return formatErr(f.fstart, "event of type %q before its schema", f.name)
	}
	n := uint64(len(t.ends))
	if f.num != n {
		return formatErr(f.fstart, "%s event number %d, expected %d", f.name, f.num, n)
	}
	ts, err := ParseTimestamp(f.stamp)
	if err != nil {
		return formatErr(f.fstart, "invalid timestamp %s", f.stamp)
	}
	ms := toMillis(ts)
	if t.hasTs && ms < t.lastMs {
		return formatErr(f.fstart, "%s timestamp goes backwards", f.name)
	}
	if err := c.pointer(f, f.vlqs[1], t.ends[n-1], "step"); err != nil {
		return err
	}
	if err := c.pointer(f, f.vlqs[2], t.ends[Jump(n)], "jump"); err != nil {
		return err
	}
	dt := f.vlqs[4]
	if dt > uint64(len(c.order)) {
		return formatErr(f.fstart, "dir type %d is not defined yet", dt)
	}
	dirEnd := c.lastSchemaEnd
	if dt > 0 {
		ends := c.order[dt-1].ends
		dirEnd = ends[len(ends)-1]
	}
	if err := c.pointer(f, f.vlqs[3], dirEnd, "dir"); err != nil {
		return err
	}
	if root := t.schema.Root; root != nil {
		w, err := newWalk(root, f.values(), f.start, f.fstart)
		if err != nil {
			return err
		}
		if err := c.framing(root, w.tree, nil, w); err != nil {
			return err
		}
	}
	t.ends = append(t.ends, f.end)
	t.lastMs, t.hasTs = ms, true
	return nil
}

// framing checks the bytes around each value: paths, separators and labels.
func (c *checker) framing(t *Type, node any, path []byte, w *walk) error {
	switch n := node.(type) {
	case nil:
		return nil
	case leafRef:
		return c.leaf(&w.leaves[n], path)
	case objNode:
		for i, m := range t.Members {
			if err := c.framing(m.Type, n[i], join(path, m.Key), w); err != nil {
				return err
			}
		}
	case listNode:
		for i, item := range n {
			if err := c.framing(t.Elem, item, join(path, strconv.Itoa(i)), w); err != nil {
				return err
			}
		}
	case *mapNode:
		for _, e := range n.entries {
			if e.first < 0 {
				continue
			}
			off := w.leaves[e.first].pathOff() + int64(len(path)) + 1
			key := c.buf[off : off+int64(e.klen)]
			if err := c.framing(t.Elem, e.child, join(path, string(key)), w); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *checker) leaf(rec *leafRec, path []byte) error {
	end := rec.voff + rec.length
	if rec.t.Block {
		label := append(append([]byte("\n--[ "), path...), " ]--\n"...)
		if !bytes.Equal(c.buf[end:end+int64(len(label))], label) {
			return formatErr(end, "block label for %q doesn't match", path)
		}
	} else {
		head := append(append([]byte(nil), path...), ": "...)
		if !bytes.Equal(c.buf[rec.start:rec.start+int64(len(head))], head) {
			return formatErr(rec.start, "inline leaf doesn't start with %q", head)
		}
		if c.buf[end] != '\n' {
			return formatErr(end, "inline leaf doesn't end with LF")
		}
	}
	value := c.buf[rec.voff:end]
	switch {
	case rec.t.Kind == KindNum && rec.t.Unknown == "" && !ValidNum(value):
		return formatErr(rec.voff, "bad num value %q", value)
	case rec.t.Kind == KindEnum:
		for _, w := range rec.t.Words {
			if string(value) == w {
				return nil
			}
		}
		return formatErr(rec.voff, "bad enum value %q", value)
	}
	return nil
}
