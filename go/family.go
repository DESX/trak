package trak

import (
	"fmt"
	"time"
)

// below is a seek key smaller than any real key: the key of node 0.
const below = -(1 << 62)

// seekKey is a key that never decreases along a type's chain.
type seekKey func(*footer) (int64, error)

func startKey(f *footer) (int64, error) {
	if f.schema {
		return below, nil
	}
	return f.start, nil
}

func stampKey(f *footer) (int64, error) {
	if f.schema {
		return below, nil
	}
	ts, err := ParseTimestamp(f.stamp)
	if err != nil {
		return 0, formatErr(f.fstart, "%v", err)
	}
	return toMillis(ts), nil
}

// seek returns the first event of a type whose key is >= m, for a key that
// never decreases along the chain, or nil when there is none (SPEC §7, Seek).
func (r *Reader) seek(s *Schema, key seekKey, m int64) (*footer, error) {
	f, err := r.latestFooter(s)
	if err != nil || f.schema {
		return nil, err
	}
	if k, err := key(f); err != nil || k < m {
		return nil, err
	}
	for f.node() > 1 {
		j := Jump(f.node())
		if j > 0 {
			g, err := r.hop(f, true)
			if err != nil {
				return nil, err
			}
			k, err := key(g)
			if err != nil {
				return nil, err
			}
			if k >= m {
				f = g
				continue
			}
			if j == f.node()-1 {
				break // the step target is the jump target
			}
		}
		g, err := r.hop(f, false)
		if err != nil {
			return nil, err
		}
		k, err := key(g)
		if err != nil {
			return nil, err
		}
		if k < m {
			break
		}
		f = g
	}
	return f, nil
}

// familyNumber returns an event's number in one of its families (SPEC §6,
// Family numbers). family must be in the event type's lineage.
func (r *Reader) familyNumber(f *footer, family string) (int64, error) {
	s := r.byName[f.name]
	if s == nil {
		return 0, formatErr(f.fstart, "event of type %q has no schema", f.name)
	}
	level := -1
	for i, a := range s.Lineage {
		if a == family {
			level = i
			break
		}
	}
	if level < 0 {
		return 0, fmt.Errorf("%w: %s is not in family %q", ErrNotFound, f.name, family)
	}
	if level == len(s.Lineage)-1 {
		if from, ok := r.childFrom[f.name]; !ok || from > f.start {
			return int64(f.num), nil
		}
	}
	if uint64(level) >= f.vlqs[5] {
		return 0, formatErr(f.fstart, "%s event has no number in family %s", f.name, family)
	}
	return int64(f.vlqs[6+level]), nil
}

// latestAll finds the latest node of every type in one walk back: each event
// names its own type and points at its dir type's latest node, so about 2K
// events cover every type (SPEC §7).
func (r *Reader) latestAll() error {
	want := len(r.schemas)
	f, err := r.last()
	for err == nil && f != nil && len(r.latest) < want {
		if s := r.byName[f.name]; s != nil {
			if _, ok := r.latest[s.Index]; !ok {
				r.latest[s.Index] = f
			}
		}
		if f.schema {
			f, err = r.prev(f)
			continue
		}
		if dt := f.vlqs[4]; dt != 0 && dt <= uint64(want) {
			if _, ok := r.latest[int(dt)]; !ok {
				g, err := r.follow(f, f.vlqs[3])
				if err != nil {
					return err
				}
				if g.name != r.schemas[dt-1].Name {
					return formatErr(g.end, "directory pointer leads to the wrong type")
				}
				r.latest[int(dt)] = g
			}
		}
		f, err = r.prev(f)
	}
	return err
}

// Family is the events of a type and its descendants, numbered from 1 in
// file order (SPEC §1).
type Family struct {
	r      *Reader
	schema *Schema
	types  []*Schema
}

// Name is the family's type name.
func (fam *Family) Name() string { return fam.schema.Name }

// Types returns the family's types: the type itself first, then its
// descendants in index order.
func (fam *Family) Types() []*Schema { return append([]*Schema(nil), fam.types...) }

func (fam *Family) key(f *footer) (int64, error) {
	if f.schema {
		return below, nil
	}
	return fam.r.familyNumber(f, fam.schema.Name)
}

// tops returns the latest event of each type that has any.
func (fam *Family) tops() ([]*footer, error) {
	if len(fam.types) > 1 {
		if err := fam.r.latestAll(); err != nil {
			return nil, err
		}
	}
	var out []*footer
	for _, s := range fam.types {
		f, err := fam.r.latestFooter(s)
		if err != nil {
			return nil, err
		}
		if !f.schema {
			out = append(out, f)
		}
	}
	return out, nil
}

// latest returns the family's latest event footer and its number.
func (fam *Family) latest() (*footer, int64, error) {
	tops, err := fam.tops()
	if err != nil {
		return nil, 0, err
	}
	var best *footer
	var bestKey int64
	for _, f := range tops {
		k, err := fam.key(f)
		if err != nil {
			return nil, 0, err
		}
		if best == nil || k > bestKey {
			best, bestKey = f, k
		}
	}
	return best, bestKey, nil
}

// Count returns the number of events in the family: the largest family
// number among its types' latest events.
func (fam *Family) Count() (int64, error) {
	_, n, err := fam.latest()
	return n, err
}

// Latest returns the family's last event, or nil when it has none.
func (fam *Family) Latest() (*Event, error) {
	f, _, err := fam.latest()
	if f == nil || err != nil {
		return nil, err
	}
	return fam.r.event(f)
}

// Get returns row m: a seek in each of the family's types, O(k log n).
func (fam *Family) Get(m int64) (*Event, error) {
	if m >= 1 {
		for _, s := range fam.types {
			f, err := fam.r.seek(s, fam.key, m)
			if err != nil {
				return nil, err
			}
			if f == nil {
				continue
			}
			if k, err := fam.key(f); err != nil {
				return nil, err
			} else if k == m {
				return &Event{r: fam.r, Schema: s, f: f}, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: family %s has no row %d", ErrNotFound, fam.schema.Name, m)
}

// run is one type's part of a family range, read in chunks.
type run struct {
	s      *Schema
	lo, hi int64 // nodes not read yet
	desc   bool
	buf    []*footer // read, in the order they are yielded
}

func (u *run) fill(r *Reader) error {
	if len(u.buf) > 0 || u.lo > u.hi {
		return nil
	}
	x, y := u.lo, min(u.hi, u.lo+255)
	if u.desc {
		x, y = max(u.lo, u.hi-255), u.hi
		u.hi = x - 1
	} else {
		u.lo = y + 1
	}
	chunk, err := r.footersBetween(u.s, x, y)
	if err != nil {
		return err
	}
	if u.desc {
		for i, j := 0, len(chunk)-1; i < j; i, j = i+1, j-1 {
			chunk[i], chunk[j] = chunk[j], chunk[i]
		}
	}
	u.buf = chunk
	return nil
}

// Range calls yield for rows a to b inclusive, descending when a > b, until
// yield returns false: one range per type, merged by family number.
func (fam *Family) Range(a, b int64, yield func(*Event) bool) error {
	count, err := fam.Count()
	if err != nil {
		return err
	}
	for _, x := range []int64{a, b} {
		if x < 1 || x > count {
			return fmt.Errorf("%w: family %s has no row %d", ErrNotFound, fam.schema.Name, x)
		}
	}
	lo, hi := min(a, b), max(a, b)
	var runs []*run
	for _, s := range fam.types {
		first, err := fam.r.seek(s, fam.key, lo)
		if err != nil {
			return err
		}
		if first == nil {
			continue
		}
		if k, err := fam.key(first); err != nil {
			return err
		} else if k > hi {
			continue
		}
		after, err := fam.r.seek(s, fam.key, hi+1)
		if err != nil {
			return err
		}
		var last int64
		if after != nil {
			last = int64(after.num) - 1
		} else {
			top, err := fam.r.latestFooter(s)
			if err != nil {
				return err
			}
			last = int64(top.node())
		}
		runs = append(runs, &run{s: s, lo: int64(first.num), hi: last, desc: a > b})
	}
	for {
		var best *run
		var bestKey int64
		for _, u := range runs {
			if err := u.fill(fam.r); err != nil {
				return err
			}
			if len(u.buf) == 0 {
				continue
			}
			k, err := fam.key(u.buf[0])
			if err != nil {
				return err
			}
			if best == nil || (a <= b && k < bestKey) || (a > b && k > bestKey) {
				best, bestKey = u, k
			}
		}
		if best == nil {
			return nil
		}
		f := best.buf[0]
		best.buf = best.buf[1:]
		if !yield(&Event{r: fam.r, Schema: best.s, f: f}) {
			return nil
		}
	}
}

// AtTime returns the first row with timestamp >= ts, or nil. Timestamps never
// decrease within a family, so it's the earliest of each type's first such
// event.
func (fam *Family) AtTime(ts time.Time) (*Event, error) {
	want := toMillis(ts)
	var best *footer
	var bestKey int64
	for _, s := range fam.types {
		f, err := fam.r.seek(s, stampKey, want)
		if err != nil {
			return nil, err
		}
		if f == nil {
			continue
		}
		k, err := fam.key(f)
		if err != nil {
			return nil, err
		}
		if best == nil || k < bestKey {
			best, bestKey = f, k
		}
	}
	if best == nil {
		return nil, nil
	}
	return fam.r.event(best)
}
