package trak

import (
	"fmt"
	"os"
	"sort"
)

// Report summarizes a checked file.
type Report struct {
	Versions    uint64
	Size        int64 // bytes of valid appends, header included
	Torn        int64 // bytes after the last valid append
	SchemaBytes int64
	BodyBytes   int64
	FooterBytes int64
	FullNodes   uint64
	DeltaNodes  uint64
	footers     []int32
}

// FooterStats returns the mean, 99th percentile and largest footer line.
func (r *Report) FooterStats() (mean float64, p99, max int) {
	if len(r.footers) == 0 {
		return 0, 0, 0
	}
	s := append([]int32(nil), r.footers...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return float64(r.FooterBytes) / float64(len(s)), int(s[len(s)*99/100]), int(s[len(s)-1])
}

// CheckOptions configure Check.
type CheckOptions struct {
	Progress func(done, total uint64) // called every 65536 versions
}

// Check validates every append of the named file (SPEC §7): each footer
// parses, each body walks, each value is valid, and each version is an exact
// path copy of the one before. A torn tail is reported as an error after the
// rest has been checked.
func Check(name string, opt *CheckOptions) error {
	_, err := CheckReport(name, opt)
	return err
}

// CheckReport is Check, also returning a summary.
func CheckReport(name string, opt *CheckOptions) (*Report, error) {
	f, err := Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	rep, err := f.check(opt)
	if err != nil {
		return rep, err
	}
	rep.Torn = st.Size() - f.size
	if rep.Torn > 0 {
		return rep, formatErr(f.size, "%d bytes after the last valid append", rep.Torn)
	}
	return rep, nil
}

func (f *File) check(opt *CheckOptions) (*Report, error) {
	rep := &Report{Size: f.size, SchemaBytes: f.schemaEnd}
	if f.latest == nil {
		return rep, formatErr(f.schemaEnd, "the file has no root append")
	}
	n := f.latest.version
	rep.Versions = n
	ends := make([]int64, n+1)
	for cur := f.latest; ; {
		if ends[cur.version] != 0 {
			return rep, formatErr(cur.start, "version %d appears twice", cur.version)
		}
		ends[cur.version] = cur.end
		if cur.version == 1 {
			if cur.body != f.schemaEnd {
				return rep, formatErr(cur.body, "version 1 must follow the schema")
			}
			break
		}
		prev, err := f.footer(cur.body)
		if err != nil {
			return rep, err
		}
		if prev.version != cur.version-1 {
			return rep, formatErr(cur.start, "version %d follows version %d", cur.version, prev.version)
		}
		cur = prev
	}
	rep.footers = make([]int32, 0, n)
	var prev *footer
	for v := uint64(1); v <= n; v++ {
		if opt != nil && opt.Progress != nil && v%65536 == 0 {
			opt.Progress(v, n)
		}
		ft, err := f.footer(ends[v])
		if err != nil {
			return rep, err
		}
		if err := f.checkAppend(ft, prev, ends); err != nil {
			return rep, fmt.Errorf("version %d (%s): %w", v, ft.path, err)
		}
		rep.BodyBytes += ft.start - ft.body
		rep.FooterBytes += ft.end - ft.start
		rep.footers = append(rep.footers, int32(ft.end-ft.start))
		for _, nd := range ft.nodes {
			if nd.kind == nodeLeafItem {
				continue
			}
			if nd.full {
				rep.FullNodes++
			} else {
				rep.DeltaNodes++
			}
		}
		prev = ft
	}
	return rep, nil
}

func (f *File) checkAppend(ft, prev *footer, ends []int64) error {
	v := ft.version
	if prev != nil && ft.ms < prev.ms {
		return formatErr(ft.start, "timestamp %s is before %s", ft.stamp, prev.stamp)
	}
	var want int64
	if j := Jump(v); j > 0 {
		want = ends[j]
	}
	if ft.vjump != want {
		return formatErr(ft.start, "version jump names the wrong append")
	}
	var body []byte
	if err := f.walkBody(ft, &body); err != nil {
		return err
	}
	var p *plan
	if v == 1 {
		p = planRoot(f.schema)
	} else {
		var err error
		if p, err = f.planAppend(prev, ft.segs[:len(ft.segs)-1]); err != nil {
			return err
		}
	}
	if joinSegs(p.segs) != ft.path {
		return formatErr(ft.start, "the next item is %s, not %s", joinSegs(p.segs), ft.path)
	}
	for _, lp := range ft.leaves {
		if !lp.present {
			continue
		}
		val := body[lp.off : lp.off+lp.n]
		switch lp.field.Kind {
		case KindNum:
			if !ValidNum(val) {
				return formatErr(ft.body+lp.off, "%s: %q is not a num", lp.rel, val)
			}
		case KindEnum:
			ok := false
			for _, w := range lp.field.Words {
				ok = ok || w == string(val)
			}
			if !ok {
				return formatErr(ft.body+lp.off, "%s: %q is not one of %v", lp.rel, val, lp.field.Words)
			}
		case KindPath:
			if err := f.checkStoredPath(string(val), prev); err != nil {
				return formatErr(ft.body+lp.off, "%s: %v", lp.rel, err)
			}
		}
	}
	if len(p.nodes) != len(ft.nodes) {
		return formatErr(ft.start, "%d nodes, the path needs %d", len(ft.nodes), len(p.nodes))
	}
	self := func(r ref) ref {
		if r.end == selfEnd {
			r.end = ft.end
		}
		return r
	}
	for i := range p.nodes {
		pn, n := &p.nodes[i], ft.nodes[i]
		bad := func(format string, args ...any) error {
			return formatErr(ft.start, "node %d: %s", i+1, fmt.Sprintf(format, args...))
		}
		if pn.kind != n.kind || pn.typ != n.typ {
			return bad("wrong kind of node")
		}
		if n.kind == nodeLeafItem {
			continue
		}
		if n.kind != nodeRoot && (n.k != pn.k || n.prev != self(pn.prev) || n.jump != self(pn.jump)) {
			return bad("version %d, prev or jump is wrong (want version %d)", n.k, pn.k)
		}
		if n.kind == nodeItem {
			want := pn.leaves
			if want == selfEnd {
				want = ft.end
			}
			if n.leaves != want {
				return bad("leaves reference is wrong")
			}
		}
		if !n.full {
			b, bft, err := f.resolve(n.base)
			if err != nil {
				return bad("delta base: %v", err)
			}
			if bft.end >= ft.end || !b.full || b.kind != n.kind || b.typ != n.typ {
				return bad("a delta's base must be an earlier full node of the same kind")
			}
		} else if len(n.entries) != len(pn.entries) {
			return bad("%d entries, want %d", len(n.entries), len(pn.entries))
		}
		es, err := f.entriesOf(n, len(pn.entries))
		if err != nil {
			return bad("%v", err)
		}
		if len(es) != len(pn.entries) {
			return bad("%d entries, want %d", len(es), len(pn.entries))
		}
		for j, e := range es {
			want := pn.entries[j]
			want.ptr = self(want.ptr)
			if e != want {
				return bad("entry %d is %v, want %v", j, e, want)
			}
		}
	}
	return nil
}

// checkStoredPath requires a stored path to name a value of the version
// before.
func (f *File) checkStoredPath(p string, prev *footer) error {
	segs := segments(p)
	if len(segs) == 0 {
		return fmt.Errorf("empty path")
	}
	for _, s := range segs {
		if !IsName(s) && !isIndex(s) {
			return fmt.Errorf("bad path %q", p)
		}
	}
	if prev == nil {
		return fmt.Errorf("path %s: nothing exists yet", p)
	}
	v, err := f.rootOf(prev).Get(p)
	if err != nil {
		return err
	}
	if l, ok := v.(Leaf); ok && !l.Present {
		return fmt.Errorf("path %s names an absent value", p)
	}
	return nil
}
