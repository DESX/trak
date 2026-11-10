package trak

import "fmt"

// Before returns up to limit events that start before cursor, newest first.
// A cursor is an Event.Offset, or a negative number for the end of the file.
// Pass the last event returned to get the next page; an empty slice means
// there are no more. A cursor that isn't where an object starts gives an
// error wrapping ErrBadCursor.
func (r *Reader) Before(cursor int64, limit int) ([]*Event, error) {
	c := r.end
	if cursor >= 0 {
		if err := r.checkCursor(cursor); err != nil {
			return nil, err
		}
		c = cursor
	}
	var out []*Event
	var f *footer
	var err error
	if c != r.magicEnd {
		f, err = r.footer(c)
	}
	for err == nil && f != nil && len(out) < limit {
		if !f.schema {
			e, err := r.event(f)
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		}
		f, err = r.prev(f)
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// After returns up to limit events that start after cursor, oldest first. A
// cursor is an Event.Offset, or a negative number for the start of the file.
// Pass the last event returned to get the next page; an empty slice means
// there are no more. A cursor that isn't where an object starts gives an
// error wrapping ErrBadCursor.
func (r *Reader) After(cursor int64, limit int) ([]*Event, error) {
	c := int64(-1)
	if cursor >= 0 {
		if err := r.checkCursor(cursor); err != nil {
			return nil, err
		}
		c = cursor
	}
	if limit <= 0 {
		return nil, nil
	}
	// The objects after c can't be found by scanning forward, since logged
	// content may look like footers. Instead find, per type, its first event
	// after c by seeking on offsets, then walk back from a point that has at
	// least limit events before it.
	bound := r.end
	if err := r.latestAll(); err != nil {
		return nil, err
	}
	for _, s := range r.schemas {
		top, err := r.latestFooter(s)
		if err != nil {
			return nil, err
		}
		if top.schema {
			continue
		}
		first, err := r.seek(s, startKey, c+1)
		if err != nil {
			return nil, err
		}
		if first != nil && top.num-first.num+1 >= uint64(limit) {
			g, err := r.navigate(top, first.num+uint64(limit)-1)
			if err != nil {
				return nil, err
			}
			bound = min(bound, g.end)
		}
	}
	var found []*footer
	var f *footer
	var err error
	if bound != r.magicEnd {
		f, err = r.footer(bound)
	}
	for err == nil && f != nil && f.start > c {
		if !f.schema {
			found = append(found, f)
		}
		f, err = r.prev(f)
	}
	if err != nil {
		return nil, err
	}
	if len(found) > limit {
		found = found[len(found)-limit:]
	}
	out := make([]*Event, len(found))
	for i, f := range found {
		e, err := r.event(f)
		if err != nil {
			return nil, err
		}
		out[len(found)-1-i] = e
	}
	return out, nil
}

// checkCursor checks that c is where an object starts: the magic line's end,
// End, or the end of a valid footer (SPEC §11's footer test).
func (r *Reader) checkCursor(c int64) error {
	if c == r.magicEnd || c == r.end {
		return nil
	}
	if c < r.magicEnd || c > r.end {
		return fmt.Errorf("%w: cursor %d is outside the file's valid objects", ErrBadCursor, c)
	}
	f, err := r.footer(c)
	if err == nil {
		if s := r.byName[f.name]; s == nil {
			err = formatErr(f.fstart, "unknown type %q", f.name)
		} else if !f.schema && s.Root != nil {
			_, err = newWalk(s.Root, f.values(), f.start, f.fstart)
		}
	}
	if err != nil {
		return fmt.Errorf("%w: cursor %d is not the start of an object", ErrBadCursor, c)
	}
	return nil
}
