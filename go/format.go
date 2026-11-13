package trak

import (
	"bytes"
	"math/bits"
	"strconv"
	"strings"
)

// ref names a node: node i (from 1) of the append whose footer line ends at
// end. The zero ref is none.
type ref struct {
	end int64
	i   uint32
}

func (r ref) none() bool { return r.i == 0 }

// entry is one slot or trie entry. count is used only by event-list slots.
type entry struct {
	count uint64
	ptr   ref
}

type nodeKind uint8

const (
	nodeRoot     nodeKind = iota // the root object: slots only
	nodeItem                     // an object item: k, prev, jump, leaves, slots
	nodeNested                   // a nested object with event lists: k, prev, jump, slots
	nodeLeafItem                 // an item of a leaf list: empty
	nodeTrie                     // an item trie node
)

// node is one parsed node. Its entries are as written: all of them for a full
// node, or the bitmap's for a delta.
type node struct {
	kind    nodeKind
	typ     *Type // object nodes
	k       uint64
	prev    ref
	jump    ref
	leaves  int64 // items: end of the append holding the leaves
	full    bool
	base    ref
	bitmap  uint64
	entries []entry
}

// written returns entry j if this node writes it.
func (n *node) written(j int) (entry, bool) {
	if n.full {
		if j < len(n.entries) {
			return n.entries[j], true
		}
		return entry{}, false
	}
	if j >= 64 || n.bitmap&(1<<uint(j)) == 0 {
		return entry{}, false
	}
	return n.entries[bits.OnesCount64(n.bitmap&(1<<uint(j)-1))], true
}

// footer is one parsed append footer.
type footer struct {
	start, end int64 // the footer line, end after its LF
	body       int64 // where the body starts
	path       string
	segs       []string
	version    uint64
	ms         int64
	stamp      string
	vjump      int64 // end of the append of version J(version), 0 for version 0
	ext        []uint64
	itemField  *Field // the new item; for version 1, a field for the root
	lens       []uint64
	leaves     []leafPos
	nodes      []*node // node i is nodes[i-1]
}

func (f *footer) node(i uint32) *node { return f.nodes[i-1] }

// leafPos is where one leaf of an item sits in its append's body.
type leafPos struct {
	field   *Field
	rel     string // path relative to the item
	present bool
	off     int64 // value offset from the body start
	n       int64 // value length
}

// depth is the trie depth of a list of c items (SPEC §5).
func depth(c uint64) int {
	h := 1
	for cap := uint64(16); cap < c; cap *= 16 {
		h++
	}
	return h
}

// vlqReader walks a footer's decoded VLQs.
type vlqReader struct {
	vals []uint64
	pos  int
	off  int64 // for errors: where the VLQs start in the file
}

func (r *vlqReader) next() (uint64, error) {
	if r.pos >= len(r.vals) {
		return 0, formatErr(r.off, "footer ends early")
	}
	v := r.vals[r.pos]
	r.pos++
	return v, nil
}

// parseFooterLine parses the footer line data, which ends at end (after its
// LF). schema is used to lay out the nodes; schemaEnd bounds references.
func parseFooterLine(data []byte, end int64, s *Schema, schemaEnd int64) (*footer, error) {
	start := end - int64(len(data))
	if len(data) < 2 || data[len(data)-1] != '\n' || !bytes.HasPrefix(data, []byte("==[ ")) {
		return nil, formatErr(start, "not a footer")
	}
	close := bytes.Index(data[4:], []byte(" ]=="))
	if close >= 0 {
		close += 4
	}
	if close < 0 {
		return nil, formatErr(start, "footer has no ]==")
	}
	head := strings.Split(string(data[4:close]), " ")
	if len(head) != 3 {
		return nil, formatErr(start, "footer must hold a path, a version and a timestamp")
	}
	f := &footer{start: start, end: end, path: head[0], stamp: head[2]}
	v, err := strconv.ParseUint(head[1], 10, 64)
	if err != nil || v == 0 || head[1] != strconv.FormatUint(v, 10) {
		return nil, formatErr(start, "bad version %q", head[1])
	}
	f.version = v
	if !validStampSyntax(f.stamp) {
		return nil, formatErr(start, "bad timestamp %q", f.stamp)
	}
	t, err := ParseTimestamp(f.stamp)
	if err != nil {
		return nil, formatErr(start, "%v", err)
	}
	f.ms = t.UnixMilli()
	vstart := start + int64(close) + 4
	vals, err := appendVLQs(make([]uint64, 0, 64), data[close+4:len(data)-1], vstart)
	if err != nil {
		return nil, err
	}
	r := &vlqReader{vals: vals, off: vstart}
	bodyLen, err := r.next()
	if err != nil {
		return nil, err
	}
	if bodyLen > uint64(start-schemaEnd) {
		return nil, formatErr(vstart, "body length %d reaches before the first append", bodyLen)
	}
	f.body = start - int64(bodyLen)
	if f.vjump, err = r.reference(f, schemaEnd); err != nil {
		return nil, err
	}
	ne, err := r.next()
	if err != nil {
		return nil, err
	}
	if ne > uint64(len(vals)) {
		return nil, formatErr(vstart, "extension count %d is too large", ne)
	}
	for ; ne > 0; ne-- {
		x, err := r.next()
		if err != nil {
			return nil, err
		}
		f.ext = append(f.ext, x)
	}
	if err := f.parseNodes(r, s, schemaEnd); err != nil {
		return nil, err
	}
	if r.pos != len(r.vals) {
		return nil, formatErr(vstart, "footer has %d VLQs after its nodes", len(r.vals)-r.pos)
	}
	return f, nil
}

// reference reads one append reference (SPEC §4).
func (r *vlqReader) reference(f *footer, schemaEnd int64) (int64, error) {
	d, err := r.next()
	if err != nil || d == 0 {
		return 0, err
	}
	if d-1 > uint64(f.body-schemaEnd) {
		return 0, formatErr(r.off, "reference reaches before the first append")
	}
	return f.body - int64(d-1), nil
}

// pointer reads one node pointer (SPEC §4).
func (r *vlqReader) pointer(f *footer, schemaEnd int64) (ref, error) {
	d, err := r.next()
	if err != nil {
		return ref{}, err
	}
	i, err := r.next()
	if err != nil {
		return ref{}, err
	}
	switch {
	case d == 0 && i == 0:
		return ref{}, nil
	case i == 0:
		return ref{}, formatErr(r.off, "pointer to node 0")
	case i > 1<<20:
		return ref{}, formatErr(r.off, "pointer to node %d", i)
	case d == 0:
		return ref{end: f.end, i: uint32(i)}, nil
	}
	if d-1 > uint64(f.body-schemaEnd) {
		return ref{}, formatErr(r.off, "pointer reaches before the first append")
	}
	return ref{end: f.body - int64(d-1), i: uint32(i)}, nil
}

// parseNodes reads the leaf lengths and the nodes, laid out by the path.
func (f *footer) parseNodes(r *vlqReader, s *Schema, schemaEnd int64) error {
	segs := segments(f.path)
	f.segs = segs
	if f.version == 1 {
		if len(segs) != 1 || segs[0] != s.Root.Name {
			return formatErr(f.start, "version 1 must write the root %s", s.Root.Name)
		}
		f.itemField = &Field{Name: s.Root.Name, Kind: KindObject, Type: s.Root}
		if err := f.readLeaves(r); err != nil {
			return err
		}
		n, err := r.objectNode(f, nodeRoot, s.Root, schemaEnd)
		if err != nil {
			return err
		}
		f.nodes = append(f.nodes, n)
		return nil
	}
	item, err := itemField(s, segs)
	if err != nil {
		return formatErr(f.start, "%v", err)
	}
	f.itemField = item
	if err := f.readLeaves(r); err != nil {
		return err
	}
	root, err := r.objectNode(f, nodeRoot, s.Root, schemaEnd)
	if err != nil {
		return err
	}
	f.nodes = append(f.nodes, root)
	typ, parent := s.Root, root
	for i := 0; i < len(segs); {
		fld := typ.Field(segs[i])
		j := typ.slot(segs[i])
		e, ok := parent.written(j)
		if !ok {
			return formatErr(f.start, "the slot for %s isn't written", segs[i])
		}
		i++
		if fld.Kind == KindObject {
			n, err := r.objectNode(f, nodeNested, fld.Type, schemaEnd)
			if err != nil {
				return err
			}
			f.nodes = append(f.nodes, n)
			typ, parent = fld.Type, n
			continue
		}
		m, _ := strconv.ParseUint(segs[i], 10, 64) // itemField checked the syntax
		if m >= e.count {
			return formatErr(f.start, "item %d of %s is past its count %d", m, strings.Join(segs[:i], "."), e.count)
		}
		for h := depth(e.count); h > 0; h-- {
			n, err := r.trieNode(f, schemaEnd)
			if err != nil {
				return err
			}
			f.nodes = append(f.nodes, n)
		}
		i++
		last := i == len(segs)
		if last && m != e.count-1 {
			return formatErr(f.start, "a new item must be the last of %s", strings.Join(segs[:i-1], "."))
		}
		switch {
		case last && fld.Item.Kind != KindObject:
			f.nodes = append(f.nodes, &node{kind: nodeLeafItem})
		default:
			n, err := r.objectNode(f, nodeItem, fld.Item.Type, schemaEnd)
			if err != nil {
				return err
			}
			f.nodes = append(f.nodes, n)
			typ, parent = fld.Item.Type, n
		}
	}
	return nil
}

// itemField finds the field of the item a footer path appends.
func itemField(s *Schema, segs []string) (*Field, error) {
	typ := s.Root
	var item *Field
	for i := 0; i < len(segs); i++ {
		if item != nil {
			if item.Kind != KindObject {
				return nil, valueErr("path %s goes past a leaf", strings.Join(segs, "."))
			}
			typ, item = item.Type, nil
		}
		fld := typ.Field(segs[i])
		if fld == nil {
			return nil, valueErr("%s has no field %s", pathOr(segs[:i], s.Root.Name), segs[i])
		}
		switch fld.Kind {
		case KindObject:
			if !fld.Type.events {
				return nil, valueErr("%s holds no event lists", strings.Join(segs[:i+1], "."))
			}
			typ = fld.Type
		case KindEvent:
			i++
			if i == len(segs) || !isIndex(segs[i]) {
				return nil, valueErr("path %s needs an item number after %s", strings.Join(segs, "."), fld.Name)
			}
			item = fld.Item
		default:
			return nil, valueErr("%s is a leaf, not an event list", strings.Join(segs[:i+1], "."))
		}
	}
	if item == nil {
		return nil, valueErr("path %s doesn't end at an item", strings.Join(segs, "."))
	}
	return item, nil
}

func pathOr(segs []string, root string) string {
	if len(segs) == 0 {
		return root
	}
	return strings.Join(segs, ".")
}

// isIndex reports whether s is a stored item number: decimal, no leading zero.
func isIndex(s string) bool {
	if s == "" || len(s) > 19 || s[0] == '0' && len(s) > 1 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// readLeaves reads the item's leaf lengths and lays out its body.
func (f *footer) readLeaves(r *vlqReader) error {
	start := r.pos
	var off int64
	var walk func(t *Type, prefix string) error
	add := func(fld *Field, rel string, present bool, n uint64) {
		p := leafPos{field: fld, rel: rel, present: present, n: int64(n)}
		if present {
			if fld.Block {
				p.off = off + int64(len(rel)) + 9
				off += 2*int64(len(rel)) + 20 + int64(n)
			} else {
				p.off = off + int64(len(rel)) + 2
				off += int64(len(rel)) + 3 + int64(n)
			}
		}
		f.leaves = append(f.leaves, p)
	}
	leaf := func(fld *Field, rel string) error {
		present := true
		if fld.Optional {
			flag, err := r.next()
			if err != nil {
				return err
			}
			if flag > 1 {
				return formatErr(r.off, "optional flag %d for %s", flag, rel)
			}
			present = flag == 1
		}
		var n uint64
		if present {
			var err error
			if n, err = r.next(); err != nil {
				return err
			}
			if n > 1<<48 {
				return formatErr(r.off, "leaf %s is too long", rel)
			}
		}
		add(fld, rel, present, n)
		return nil
	}
	walk = func(t *Type, prefix string) error {
		for _, fld := range t.Fields {
			rel := prefix + fld.Name
			switch {
			case fld.IsLeaf():
				if err := leaf(fld, rel); err != nil {
					return err
				}
			case fld.Kind == KindObject:
				if fld.Optional {
					flag, err := r.next()
					if err != nil {
						return err
					}
					if flag > 1 {
						return formatErr(r.off, "optional flag %d for %s", flag, rel)
					}
					if flag == 0 {
						continue
					}
				}
				if err := walk(fld.Type, rel+"."); err != nil {
					return err
				}
			}
		}
		return nil
	}
	var err error
	if f.itemField.Kind == KindObject {
		err = walk(f.itemField.Type, "")
	} else {
		err = leaf(f.itemField, f.itemField.Name)
	}
	if err != nil {
		return err
	}
	f.lens = r.vals[start:r.pos]
	if off != f.start-f.body {
		return formatErr(f.start, "leaf lengths give a body of %d bytes, not %d", off, f.start-f.body)
	}
	return nil
}

// objectNode reads a root, item or nested object node.
func (r *vlqReader) objectNode(f *footer, kind nodeKind, t *Type, schemaEnd int64) (*node, error) {
	n := &node{kind: kind, typ: t}
	var err error
	if kind != nodeRoot {
		if n.k, err = r.next(); err != nil {
			return nil, err
		}
		if n.prev, err = r.pointer(f, schemaEnd); err != nil {
			return nil, err
		}
		if n.jump, err = r.pointer(f, schemaEnd); err != nil {
			return nil, err
		}
	}
	if kind == nodeItem {
		d, err := r.next()
		if err != nil {
			return nil, err
		}
		if d == 0 {
			n.leaves = f.end
		} else {
			if d-1 > uint64(f.body-schemaEnd) {
				return nil, formatErr(r.off, "leaves reference reaches before the first append")
			}
			n.leaves = f.body - int64(d-1)
		}
	}
	if err := r.entries(f, n, len(t.slots), func(j int) (entry, error) {
		var e entry
		var err error
		if t.slots[j].Kind == KindEvent {
			if e.count, err = r.next(); err != nil {
				return e, err
			}
		}
		e.ptr, err = r.pointer(f, schemaEnd)
		return e, err
	}, schemaEnd); err != nil {
		return nil, err
	}
	if n.full && len(n.entries) != len(t.slots) {
		return nil, formatErr(r.off, "a full slots form has %d slots, not %d", len(n.entries), len(t.slots))
	}
	return n, nil
}

// trieNode reads one item trie node.
func (r *vlqReader) trieNode(f *footer, schemaEnd int64) (*node, error) {
	n := &node{kind: nodeTrie}
	err := r.entries(f, n, 16, func(int) (entry, error) {
		p, err := r.pointer(f, schemaEnd)
		return entry{ptr: p}, err
	}, schemaEnd)
	if err == nil && n.full && len(n.entries) == 0 {
		err = formatErr(r.off, "an empty trie node")
	}
	return n, err
}

// entries reads a full or delta form of at most max entries.
func (r *vlqReader) entries(f *footer, n *node, max int, one func(j int) (entry, error), schemaEnd int64) error {
	form, err := r.next()
	if err != nil {
		return err
	}
	switch form {
	case 0:
		n.full = true
		c, err := r.next()
		if err != nil {
			return err
		}
		if c > uint64(max) {
			return formatErr(r.off, "%d entries, at most %d", c, max)
		}
		n.entries = make([]entry, c)
		for j := range n.entries {
			if n.entries[j], err = one(j); err != nil {
				return err
			}
		}
	case 1:
		if n.base, err = r.pointer(f, schemaEnd); err != nil {
			return err
		}
		if n.base.none() || n.base.end == f.end {
			return formatErr(r.off, "a delta needs an earlier base")
		}
		if n.bitmap, err = r.next(); err != nil {
			return err
		}
		if max < 64 && n.bitmap>>uint(max) != 0 {
			return formatErr(r.off, "bitmap names entries past %d", max)
		}
		n.entries = make([]entry, 0, bits.OnesCount64(n.bitmap))
		for b := n.bitmap; b != 0; b &= b - 1 {
			e, err := one(bits.TrailingZeros64(b))
			if err != nil {
				return err
			}
			n.entries = append(n.entries, e)
		}
	default:
		return formatErr(r.off, "unknown node form %d", form)
	}
	return nil
}

// appendRef appends a reference to the append ending at target, from an
// append whose body starts at body.
func appendRef(dst []byte, body, target int64) []byte {
	if target == 0 {
		return AppendVLQ(dst, 0)
	}
	return AppendVLQ(dst, uint64(body-target+1))
}

// appendPtr appends a pointer; self is the end the new footer will have,
// marked by selfEnd.
func appendPtr(dst []byte, body int64, p ref) []byte {
	switch {
	case p.none():
		return append(dst, 'A', 'A')
	case p.end == selfEnd:
		return AppendVLQ(append(dst, 'A'), uint64(p.i))
	}
	return AppendVLQ(AppendVLQ(dst, uint64(body-p.end+1)), uint64(p.i))
}

// selfEnd marks a ref to a node of the footer being written.
const selfEnd = -1
