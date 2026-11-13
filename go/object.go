package trak

import (
	"fmt"
	"strconv"
	"strings"
)

// Object is one object of the tree at some version: the root, an item, or a
// nested object.
type Object struct {
	f       *File
	typ     *Type
	path    string // "" for the root
	node    ref    // none for a nested object whose lists never changed
	leaves  int64  // end of the append holding its leaves; 0: find the root's
	prefix  string // the leaves' paths within that append, for nested objects
	version uint64 // the tree version it was reached from
}

// Type returns the object's type.
func (o *Object) Type() *Type { return o.typ }

// Path returns the object's path, "" for the root.
func (o *Object) Path() string { return o.path }

func (o *Object) child(name string) string {
	if o.path == "" {
		return name
	}
	return o.path + "." + name
}

// K returns the object's version count: how many times it has changed. A
// nested object whose lists never changed is at 0.
func (o *Object) K() (uint64, error) {
	if o.node.none() {
		return 0, nil
	}
	n, ft, err := o.f.resolve(o.node)
	if err != nil {
		return 0, err
	}
	if n.kind == nodeRoot {
		return ft.version - 1, nil
	}
	return n.k, nil
}

// Written describes the append that wrote this version of the object, or the
// append that created it for one without a node.
func (o *Object) Written() (Append, error) {
	if o.node.none() {
		return o.Created()
	}
	return o.f.AppendEndingAt(o.node.end)
}

// Created describes the append holding the object's leaves.
func (o *Object) Created() (Append, error) {
	end, err := o.leavesEnd()
	if err != nil {
		return Append{}, err
	}
	return o.f.AppendEndingAt(end)
}

func (o *Object) leavesEnd() (int64, error) {
	if o.leaves == 0 {
		ft, err := o.f.footerAt(1)
		if err != nil {
			return 0, err
		}
		o.leaves = ft.end
	}
	return o.leaves, nil
}

// Version returns version k of this object, 0 ≤ k ≤ K().
func (o *Object) Version(k uint64) (*Object, error) {
	cur, err := o.K()
	if err != nil {
		return nil, err
	}
	if k > cur {
		return nil, fmt.Errorf("%w: version %d of %s, which has %d", ErrNotFound, k, o.describe(), cur+1)
	}
	if o.node.none() {
		return o, nil
	}
	n, ft, err := o.f.resolve(o.node)
	if err != nil {
		return nil, err
	}
	if n.kind == nodeRoot {
		return o.f.Root(k + 1)
	}
	r := o.node
	for n.k > k {
		next := n.prev
		if n.k > 0 && Jump(n.k) >= k && !n.jump.none() {
			next = n.jump
		}
		if next.none() {
			return nil, formatErr(ft.start, "object %s has no version %d", o.describe(), k)
		}
		r = next
		if n, ft, err = o.f.resolve(r); err != nil {
			return nil, err
		}
	}
	out := *o
	out.node = r
	out.version = ft.version
	return &out, nil
}

func (o *Object) describe() string {
	if o.path == "" {
		return o.typ.Name
	}
	return o.path
}

// Get resolves a path relative to this object.
func (o *Object) Get(path string) (any, error) {
	var cur any = o
	for _, seg := range segments(path) {
		switch v := cur.(type) {
		case *Object:
			next, err := v.Field(seg)
			if err != nil {
				return nil, err
			}
			cur = next
		case *List:
			m, err := strconv.ParseInt(seg, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: %s needs an item number, not %q", ErrNotFound, v.path, seg)
			}
			if cur, err = v.Item(m); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: %s goes past a leaf", ErrNotFound, path)
		}
	}
	return cur, nil
}

// Field returns the named field: a Leaf, an *Object or a *List.
func (o *Object) Field(name string) (any, error) {
	fld := o.typ.Field(name)
	if fld == nil {
		return nil, fmt.Errorf("%w: %s has no field %s", ErrNotFound, o.describe(), name)
	}
	switch {
	case fld.IsLeaf():
		return o.Leaf(name)
	case fld.Kind == KindObject:
		return o.nested(fld)
	}
	return o.List(name)
}

func (o *Object) nested(fld *Field) (*Object, error) {
	c := &Object{f: o.f, typ: fld.Type, path: o.child(fld.Name), leaves: o.leaves, prefix: o.prefix + fld.Name + ".", version: o.version}
	if fld.Type.events && !o.node.none() {
		e, err := o.slotEntry(fld.Name)
		if err != nil {
			return nil, err
		}
		c.node = e.ptr
	}
	return c, nil
}

func (o *Object) slotEntry(name string) (entry, error) {
	if o.node.none() {
		return entry{}, nil
	}
	n, _, err := o.f.resolve(o.node)
	if err != nil {
		return entry{}, err
	}
	return o.f.entryAt(n, o.typ.slot(name))
}

// List returns the named event list.
func (o *Object) List(name string) (*List, error) {
	fld := o.typ.Field(name)
	if fld == nil || fld.Kind != KindEvent {
		return nil, fmt.Errorf("%w: %s has no event list %s", ErrNotFound, o.describe(), name)
	}
	e, err := o.slotEntry(name)
	if err != nil {
		return nil, err
	}
	return &List{f: o.f, field: fld, path: o.child(name), count: e.count, root: e.ptr, version: o.version}, nil
}

// Leaf returns a plain leaf; name may be a dotted path through nested objects.
func (o *Object) Leaf(name string) (Leaf, error) {
	end, err := o.leavesEnd()
	if err != nil {
		return Leaf{}, err
	}
	ft, err := o.f.footer(end)
	if err != nil {
		return Leaf{}, err
	}
	rel := o.prefix + name
	for _, p := range ft.leaves {
		if p.rel == rel {
			return Leaf{f: o.f, Field: p.field, Path: o.child(name), Present: p.present, Off: ft.body + p.off, Len: p.n}, nil
		}
	}
	// An absent optional nested object leaves no lengths for its leaves.
	if o.typ.Field(strings.SplitN(name, ".", 2)[0]) != nil {
		return Leaf{f: o.f, Path: o.child(name)}, nil
	}
	return Leaf{}, fmt.Errorf("%w: %s has no leaf %s", ErrNotFound, o.describe(), name)
}

// Leaves returns every plain leaf of the object, nested objects' included, in
// schema order.
func (o *Object) Leaves() ([]Leaf, error) {
	end, err := o.leavesEnd()
	if err != nil {
		return nil, err
	}
	ft, err := o.f.footer(end)
	if err != nil {
		return nil, err
	}
	var out []Leaf
	for _, p := range ft.leaves {
		if strings.HasPrefix(p.rel, o.prefix) {
			out = append(out, Leaf{f: o.f, Field: p.field, Path: o.child(p.rel[len(o.prefix):]), Present: p.present, Off: ft.body + p.off, Len: p.n})
		}
	}
	return out, nil
}

// List is one event list at some version.
type List struct {
	f       *File
	field   *Field
	path    string
	count   uint64
	root    ref
	version uint64
}

// Field returns the list's field.
func (l *List) Field() *Field { return l.field }

// Path returns the list's path.
func (l *List) Path() string { return l.path }

// Len returns the number of items.
func (l *List) Len() uint64 { return l.count }

// Item returns item m: an *Object for object lists, a Leaf for leaf lists.
// Negative m counts back from the latest item.
func (l *List) Item(m int64) (any, error) {
	if m < 0 {
		m += int64(l.count)
	}
	if m < 0 || uint64(m) >= l.count {
		return nil, fmt.Errorf("%w: item %d of %s, which has %d", ErrNotFound, m, l.path, l.count)
	}
	r, err := l.itemRef(uint64(m))
	if err != nil {
		return nil, err
	}
	return l.itemAt(uint64(m), r)
}

func (l *List) itemAt(m uint64, r ref) (any, error) {
	path := l.path + "." + strconv.FormatUint(m, 10)
	n, ft, err := l.f.resolve(r)
	if err != nil {
		return nil, err
	}
	if l.field.Item.Kind != KindObject {
		if n.kind != nodeLeafItem {
			return nil, formatErr(ft.start, "%s should be a leaf item", path)
		}
		p := ft.leaves[0]
		return Leaf{f: l.f, Field: p.field, Path: path, Present: true, Off: ft.body + p.off, Len: p.n}, nil
	}
	if n.kind != nodeItem {
		return nil, formatErr(ft.start, "%s should be an item", path)
	}
	return &Object{f: l.f, typ: l.field.Item.Type, path: path, node: r, leaves: n.leaves, version: l.version}, nil
}

// itemRef walks the trie to item m.
func (l *List) itemRef(m uint64) (ref, error) {
	r := l.root
	for h := depth(l.count) - 1; h >= 0; h-- {
		n, ft, err := l.f.resolve(r)
		if err != nil {
			return ref{}, err
		}
		if n.kind != nodeTrie {
			return ref{}, formatErr(ft.start, "expected a trie node for %s", l.path)
		}
		e, err := l.f.entryAt(n, int(m>>(4*uint(h))&15))
		if err != nil {
			return ref{}, err
		}
		if e.ptr.none() {
			return ref{}, formatErr(ft.start, "item %d of %s is missing", m, l.path)
		}
		r = e.ptr
	}
	return r, nil
}

// Range calls fn for items lo ≤ m < hi in order, sharing trie reads.
func (l *List) Range(lo, hi uint64, fn func(m uint64, item any) error) error {
	if hi > l.count {
		hi = l.count
	}
	for m := lo; m < hi; {
		// Read one bottom trie node, then its items.
		r := l.root
		for h := depth(l.count) - 1; h >= 1; h-- {
			n, _, err := l.f.resolve(r)
			if err != nil {
				return err
			}
			e, err := l.f.entryAt(n, int(m>>(4*uint(h))&15))
			if err != nil {
				return err
			}
			r = e.ptr
		}
		n, _, err := l.f.resolve(r)
		if err != nil {
			return err
		}
		es, err := l.f.entriesOf(n, 16)
		if err != nil {
			return err
		}
		for ; m < hi; m++ {
			item, err := l.itemAt(m, es[m&15].ptr)
			if err != nil {
				return err
			}
			if err := fn(m, item); err != nil {
				return err
			}
			if m&15 == 15 {
				m++
				break
			}
		}
	}
	return nil
}

// Leaf is one leaf value in the file.
type Leaf struct {
	f       *File
	Field   *Field
	Path    string
	Present bool
	Off     int64 // where the value starts in the file
	Len     int64
}

// Bytes reads the value.
func (l Leaf) Bytes() ([]byte, error) {
	if !l.Present {
		return nil, nil
	}
	return l.f.readNew(l.Off, l.Len)
}

// String reads the value as a string, "" if absent or unreadable.
func (l Leaf) String() string {
	b, _ := l.Bytes()
	return string(b)
}
