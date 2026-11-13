package trak

import (
	"fmt"
	"strconv"
)

// planNode is one node of an append's footer, with every entry resolved. Refs
// to nodes of the new footer have end selfEnd.
type planNode struct {
	kind    nodeKind
	typ     *Type
	k       uint64
	prev    ref
	jump    ref
	leaves  int64 // items: selfEnd for this append
	entries []entry
	old     *node // the version this node replaces, if any
	oldRef  ref
}

// plan is the path copy for one append: the nodes from the root down to the
// new item.
type plan struct {
	segs  []string // the new item's path
	item  *Field
	index uint64 // the new item's number
	nodes []planNode
}

// planRoot plans version 1: the root with every list empty.
func planRoot(s *Schema) *plan {
	return &plan{
		segs:  []string{s.Root.Name},
		item:  &Field{Name: s.Root.Name, Kind: KindObject, Type: s.Root},
		nodes: []planNode{{kind: nodeRoot, typ: s.Root, entries: make([]entry, len(s.Root.slots))}},
	}
}

// planAppend plans appending one item to the list at listSegs, on top of the
// version whose footer is prev.
func (f *File) planAppend(prev *footer, listSegs []string) (*plan, error) {
	p := &plan{}
	rootRef := ref{end: prev.end, i: 1}
	root, _, err := f.resolve(rootRef)
	if err != nil {
		return nil, err
	}
	es, err := f.entriesOf(root, len(f.schema.Root.slots))
	if err != nil {
		return nil, err
	}
	p.nodes = append(p.nodes, planNode{kind: nodeRoot, typ: f.schema.Root, entries: es, old: root, oldRef: rootRef})
	typ := f.schema.Root
	for i := 0; i < len(listSegs); i++ {
		seg := listSegs[i]
		fld := typ.Field(seg)
		path := joinSegs(listSegs[:i+1])
		if fld == nil {
			return nil, valueErr("%s has no field %s", pathOr(listSegs[:i], typ.Name), seg)
		}
		j := typ.slot(seg)
		if j < 0 {
			return nil, valueErr("%s holds no event lists", path)
		}
		parent := len(p.nodes) - 1
		old := p.nodes[parent].entries[j]
		next := ref{end: selfEnd, i: uint32(len(p.nodes) + 1)}
		if fld.Kind == KindObject {
			n, err := f.nextVersion(nodeNested, fld.Type, old.ptr)
			if err != nil {
				return nil, err
			}
			p.nodes[parent].entries[j] = entry{ptr: next}
			p.nodes = append(p.nodes, n)
			typ = fld.Type
			continue
		}
		last := i == len(listSegs)-1
		var m uint64
		count := old.count
		if last {
			m = count
			count++
		} else {
			i++
			if !isIndex(listSegs[i]) {
				return nil, valueErr("%s needs an item number, not %q", path, listSegs[i])
			}
			m, _ = strconv.ParseUint(listSegs[i], 10, 64)
			if m >= count {
				return nil, valueErr("%s has no item %d", path, m)
			}
			if fld.Item.Kind != KindObject {
				return nil, valueErr("%s.%d is a leaf, not an object", path, m)
			}
		}
		p.nodes[parent].entries[j] = entry{count: count, ptr: next}
		// The trie, top down. When it grows a level, the old root becomes
		// entry 0 of the new one.
		cur := old.ptr
		hOld, hNew := 0, depth(count)
		if old.count > 0 {
			hOld = depth(old.count)
		}
		for h := hNew - 1; h >= 0; h-- {
			n := planNode{kind: nodeTrie}
			if h >= hOld {
				if h == hOld && !cur.none() {
					n.entries = []entry{{ptr: cur}}
				}
			} else if !cur.none() {
				on, _, err := f.resolve(cur)
				if err != nil {
					return nil, err
				}
				if on.kind != nodeTrie {
					return nil, formatErr(cur.end, "expected a trie node")
				}
				if n.entries, err = f.entriesOf(on, 0); err != nil {
					return nil, err
				}
				n.old, n.oldRef = on, cur
			}
			d := int(m >> (4 * uint(h)) & 15)
			for len(n.entries) <= d {
				n.entries = append(n.entries, entry{})
			}
			cur = n.entries[d].ptr
			n.entries[d] = entry{ptr: ref{end: selfEnd, i: uint32(len(p.nodes) + 2)}}
			p.nodes = append(p.nodes, n)
		}
		if last {
			if !cur.none() {
				return nil, formatErr(prev.end, "%s already has an item %d", path, m)
			}
			p.segs = append(append([]string(nil), listSegs...), strconv.FormatUint(m, 10))
			p.item, p.index = fld.Item, m
			if fld.Item.Kind == KindObject {
				p.nodes = append(p.nodes, planNode{kind: nodeItem, typ: fld.Item.Type, leaves: selfEnd,
					entries: make([]entry, len(fld.Item.Type.slots))})
			} else {
				p.nodes = append(p.nodes, planNode{kind: nodeLeafItem})
			}
			return p, nil
		}
		if cur.none() {
			return nil, formatErr(prev.end, "item %d of %s is missing", m, path)
		}
		n, err := f.nextVersion(nodeItem, fld.Item.Type, cur)
		if err != nil {
			return nil, err
		}
		p.nodes = append(p.nodes, n)
		typ = fld.Item.Type
	}
	return nil, valueErr("%s is not an event list", joinSegs(listSegs))
}

// nextVersion plans the next version of the object whose current node is
// cur, or its first node if cur is none.
func (f *File) nextVersion(kind nodeKind, t *Type, cur ref) (planNode, error) {
	n := planNode{kind: kind, typ: t}
	if cur.none() {
		if kind == nodeItem {
			return n, formatErr(0, "an item without a node")
		}
		n.entries = make([]entry, len(t.slots))
		return n, nil
	}
	on, _, err := f.resolve(cur)
	if err != nil {
		return n, err
	}
	if on.kind != kind || on.typ != t {
		return n, formatErr(cur.end, "expected a node of %s", typeName(t))
	}
	if n.entries, err = f.entriesOf(on, len(t.slots)); err != nil {
		return n, err
	}
	n.old, n.oldRef = on, cur
	n.k, n.prev, n.leaves = on.k+1, cur, on.leaves
	if n.jump, err = f.objectVersion(cur, on, Jump(n.k)); err != nil {
		return n, err
	}
	return n, nil
}

// objectVersion walks from node r (version n.k) back to version k.
func (f *File) objectVersion(r ref, n *node, k uint64) (ref, error) {
	for n.k > k {
		next := n.prev
		if Jump(n.k) >= k && !n.jump.none() {
			next = n.jump
		}
		if next.none() {
			return ref{}, formatErr(r.end, "object version %d is unreachable", k)
		}
		var err error
		r = next
		if n, _, err = f.resolve(r); err != nil {
			return ref{}, err
		}
	}
	if n.k != k {
		return ref{}, formatErr(r.end, "object version %d is missing", k)
	}
	return r, nil
}

func typeName(t *Type) string {
	if t.Name == "" {
		return "an inline object"
	}
	return t.Name
}

func joinSegs(segs []string) string {
	out := ""
	for i, s := range segs {
		if i > 0 {
			out += "."
		}
		out += s
	}
	return out
}

// Policy chooses how nodes are laid out (SPEC §7).
type Policy uint8

const (
	// Reference writes every node in full form.
	Reference Policy = iota
	// Delta writes a node as a delta against a full base when that is less
	// than half its full size.
	Delta
)

func (p Policy) String() string {
	switch p {
	case Reference:
		return "reference"
	case Delta:
		return "delta"
	}
	return fmt.Sprintf("policy(%d)", uint8(p))
}

// ParsePolicy parses a policy name.
func ParsePolicy(s string) (Policy, error) {
	switch s {
	case "reference", "full", "":
		return Reference, nil
	case "delta":
		return Delta, nil
	}
	return 0, fmt.Errorf("unknown policy %q (reference or delta)", s)
}
