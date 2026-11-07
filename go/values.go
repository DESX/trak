package trak

import (
	"bytes"
	"math"
	"reflect"
	"sort"
	"strconv"
)

// Pair is one entry of an ordered map value.
type Pair struct {
	Key   string
	Value any
}

// Pairs is a map value whose entries are written in the given order. Go maps
// are unordered, so a map[string]T is written with keys sorted bytewise.
type Pairs []Pair

// --- encoding ---------------------------------------------------------------

type encoder struct {
	body []byte
	vals []uint64
}

func encodeValue(root *Type, v any) ([]byte, []uint64, error) {
	e := &encoder{}
	if err := e.object(root, reflect.ValueOf(v), nil); err != nil {
		return nil, nil, err
	}
	return e.body, e.vals, nil
}

func showPath(path []byte) string {
	if len(path) == 0 {
		return "<root>"
	}
	return strconv.Quote(string(path))[1 : len(strconv.Quote(string(path)))-1]
}

// deref unwraps interfaces and pointers. ok is false for nil.
func deref(v reflect.Value) (reflect.Value, bool) {
	for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
		if v.IsNil() {
			return v, false
		}
		v = v.Elem()
	}
	return v, v.IsValid()
}

func join(path []byte, key string) []byte {
	out := make([]byte, 0, len(path)+1+len(key))
	if len(path) > 0 {
		out = append(append(out, path...), '.')
	}
	return append(out, key...)
}

// members returns an object value's fields by key.
func members(v reflect.Value, path []byte) (map[string]reflect.Value, error) {
	v, ok := deref(v)
	if !ok {
		return nil, valueErr("%s: expected an object", showPath(path))
	}
	out := map[string]reflect.Value{}
	switch v.Kind() {
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return nil, valueErr("%s: object keys must be strings", showPath(path))
		}
		it := v.MapRange()
		for it.Next() {
			out[it.Key().String()] = it.Value()
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := f.Name
			if tag, ok := f.Tag.Lookup("trak"); ok {
				if tag == "-" {
					continue
				}
				name = tag
			}
			out[name] = v.Field(i)
		}
	default:
		return nil, valueErr("%s: expected an object, got %s", showPath(path), v.Type())
	}
	return out, nil
}

func (e *encoder) object(t *Type, v reflect.Value, path []byte) error {
	fields, err := members(v, path)
	if err != nil {
		return err
	}
	for k := range fields {
		found := false
		for _, m := range t.Members {
			if m.Key == k {
				found = true
				break
			}
		}
		if !found {
			return valueErr("%s: unknown field '%s'", showPath(path), k)
		}
	}
	for _, m := range t.Members {
		if err := e.value(m.Type, fields[m.Key], join(path, m.Key)); err != nil {
			return err
		}
	}
	return nil
}

func (e *encoder) value(t *Type, v reflect.Value, path []byte) error {
	v, present := deref(v)
	if !present {
		if !t.Optional {
			return valueErr("%s: value is required", showPath(path))
		}
		e.vals = append(e.vals, 0)
		return nil
	}
	if t.Optional {
		e.vals = append(e.vals, 1)
	}
	switch t.Kind {
	case KindStr, KindNum, KindEnum:
		data, err := leafBytes(t, v, path)
		if err != nil {
			return err
		}
		e.vals = append(e.vals, uint64(len(data)))
		if t.Block {
			e.body = append(e.body, data...)
			e.body = append(e.body, "\n--[ "...)
			e.body = append(e.body, path...)
			e.body = append(e.body, " ]--\n"...)
		} else {
			e.body = append(e.body, path...)
			e.body = append(e.body, ": "...)
			e.body = append(e.body, data...)
			e.body = append(e.body, '\n')
		}
		return nil
	case KindObject:
		return e.object(t, v, path)
	case KindList:
		if (v.Kind() != reflect.Slice && v.Kind() != reflect.Array) || isBytes(v) {
			return valueErr("%s: expected a list, got %s", showPath(path), v.Type())
		}
		e.vals = append(e.vals, uint64(v.Len()))
		for i := 0; i < v.Len(); i++ {
			if err := e.value(t.Elem, v.Index(i), join(path, strconv.Itoa(i))); err != nil {
				return err
			}
		}
		return nil
	default:
		entries, err := mapEntries(v, path)
		if err != nil {
			return err
		}
		e.vals = append(e.vals, uint64(len(entries)))
		seen := make(map[string]bool, len(entries))
		for _, p := range entries {
			if seen[p.Key] {
				return valueErr("%s: repeated map key '%s'", showPath(path), p.Key)
			}
			seen[p.Key] = true
			e.vals = append(e.vals, uint64(len(p.Key)))
			if err := e.value(t.Elem, reflect.ValueOf(p.Value), join(path, p.Key)); err != nil {
				return err
			}
		}
		return nil
	}
}

var pairsType = reflect.TypeOf(Pairs(nil))

// mapEntries accepts Pairs, a map with string keys (sorted bytewise), or a
// JSON-decoded list of [key, value] pairs.
func mapEntries(v reflect.Value, path []byte) ([]Pair, error) {
	if v.Type() == pairsType {
		return v.Interface().(Pairs), nil
	}
	switch v.Kind() {
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return nil, valueErr("%s: map keys must be strings", showPath(path))
		}
		out := make([]Pair, 0, v.Len())
		it := v.MapRange()
		for it.Next() {
			out = append(out, Pair{it.Key().String(), it.Value().Interface()})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
		return out, nil
	case reflect.Slice, reflect.Array:
		if isBytes(v) {
			break
		}
		out := make([]Pair, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			item, _ := deref(v.Index(i))
			if !item.IsValid() || (item.Kind() != reflect.Slice && item.Kind() != reflect.Array) || item.Len() != 2 {
				return nil, valueErr("%s: map entries must be [key, value] pairs", showPath(path))
			}
			k, _ := deref(item.Index(0))
			if !k.IsValid() || k.Kind() != reflect.String {
				return nil, valueErr("%s: map keys must be strings", showPath(path))
			}
			out = append(out, Pair{k.String(), item.Index(1).Interface()})
		}
		return out, nil
	}
	return nil, valueErr("%s: expected a map, got %s", showPath(path), v.Type())
}

func isBytes(v reflect.Value) bool {
	return v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8
}

func leafBytes(t *Type, v reflect.Value, path []byte) ([]byte, error) {
	var data []byte
	switch {
	case v.Kind() == reflect.String:
		data = []byte(v.String())
	case isBytes(v):
		data = v.Bytes()
	case t.Kind == KindNum:
		return formatNum(v, path)
	default:
		return nil, valueErr("%s: %s needs a string or []byte, got %s", showPath(path), t.Kind, v.Type())
	}
	switch t.Kind {
	case KindNum:
		if !ValidNum(data) {
			return nil, valueErr("%s: %q is not a valid num", showPath(path), data)
		}
	case KindEnum:
		ok := false
		for _, w := range t.Words {
			if string(data) == w {
				ok = true
				break
			}
		}
		if !ok {
			return nil, valueErr("%s: %q is not one of %s", showPath(path), data, joinWords(t.Words))
		}
	}
	return data, nil
}

func joinWords(words []string) string {
	var b bytes.Buffer
	for i, w := range words {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(w)
	}
	return b.String()
}

func formatNum(v reflect.Value, path []byte) ([]byte, error) {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.AppendInt(nil, v.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.AppendUint(nil, v.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, valueErr("%s: num must be finite", showPath(path))
		}
		bits := 64
		if v.Kind() == reflect.Float32 {
			bits = 32
		}
		return strconv.AppendFloat(nil, f, 'g', -1, bits), nil
	case reflect.Bool:
		return nil, valueErr("%s: num can't be a bool", showPath(path))
	}
	return nil, valueErr("%s: num needs a number or a decimal string, got %s", showPath(path), v.Type())
}

// ValidNum reports whether b matches the num grammar in SPEC §5:
// -?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?
func ValidNum(b []byte) bool {
	i, n := 0, len(b)
	if i < n && b[i] == '-' {
		i++
	}
	if i >= n {
		return false
	}
	if b[i] == '0' {
		i++
	} else if b[i] >= '1' && b[i] <= '9' {
		for i < n && isDigit(b[i]) {
			i++
		}
	} else {
		return false
	}
	if i < n && b[i] == '.' {
		i++
		start := i
		for i < n && isDigit(b[i]) {
			i++
		}
		if i == start {
			return false
		}
	}
	if i < n && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < n && (b[i] == '+' || b[i] == '-') {
			i++
		}
		start := i
		for i < n && isDigit(b[i]) {
			i++
		}
		if i == start {
			return false
		}
	}
	return i == n
}

// --- decoding ----------------------------------------------------------------

// maxEmptyElements bounds lists of empty objects, which cost no bytes in the
// file and so could otherwise make a tiny file decode into a huge tree.
const maxEmptyElements = 1 << 16

// A decoded tree mirrors the schema: leafRef for leaves, nil for absent,
// objNode for objects, listNode for lists and *mapNode for maps.
type (
	leafRef  int
	objNode  []any
	listNode []any
	mapNode  struct {
		plen    int64
		entries []mapEntryNode
	}
	mapEntryNode struct {
		klen  uint64
		first int // index of the entry's first leaf, or -1
		child any
	}
)

type leafRec struct {
	t      *Type
	start  int64
	voff   int64
	length int64
	plen   int64
}

func (r *leafRec) pathOff() int64 {
	if r.t.Block {
		return r.voff + r.length + 5
	}
	return r.start
}

// walk computes every leaf's offsets from an event's VLQs (SPEC §8).
type walk struct {
	vals   []uint64
	vi     int
	off    int64
	end    int64
	leaves []leafRec
	tree   any
}

func newWalk(root *Type, vals []uint64, start, end int64) (w *walk, err error) {
	w = &walk{vals: vals, off: start, end: end}
	defer func() {
		if r := recover(); r != nil {
			fe, ok := r.(*FormatError)
			if !ok {
				panic(r)
			}
			w, err = nil, fe
		}
	}()
	w.tree = w.object(root, -1)
	if w.vi != len(vals) {
		return nil, formatErr(end, "event has more value VLQs than its schema uses")
	}
	if w.off != end {
		return nil, formatErr(w.off, "value walk doesn't end at the footer")
	}
	return w, nil
}

func (w *walk) next() uint64 {
	if w.vi >= len(w.vals) {
		panic(formatErr(w.end, "event has fewer value VLQs than its schema needs"))
	}
	v := w.vals[w.vi]
	w.vi++
	return v
}

func (w *walk) object(t *Type, plen int64) objNode {
	out := make(objNode, len(t.Members))
	for i, m := range t.Members {
		out[i] = w.node(m.Type, plen+1+int64(len(m.Key)))
	}
	return out
}

func zeroSize(t *Type) bool {
	if t.Kind != KindObject || t.Optional {
		return false
	}
	for _, m := range t.Members {
		if !zeroSize(m.Type) {
			return false
		}
	}
	return true
}

func digits(i int) int64 { return int64(len(strconv.Itoa(i))) }

func (w *walk) node(t *Type, plen int64) any {
	if t.Optional {
		switch w.next() {
		case 0:
			return nil
		case 1:
		default:
			panic(formatErr(w.end, "optional flag must be 0 or 1"))
		}
	}
	switch t.Kind {
	case KindStr, KindNum, KindEnum:
		length := w.next()
		start := w.off
		room := uint64(w.end - start)
		var voff int64
		if t.Block {
			if length > room || uint64(plen)+10 > room-length {
				panic(formatErr(start, "value walk runs past the footer"))
			}
			voff = start
			w.off = start + int64(length) + plen + 10
		} else {
			if uint64(plen)+3 > room || length > room-uint64(plen)-3 {
				panic(formatErr(start, "value walk runs past the footer"))
			}
			voff = start + plen + 2
			w.off = voff + int64(length) + 1
		}
		w.leaves = append(w.leaves, leafRec{t, start, voff, int64(length), plen})
		return leafRef(len(w.leaves) - 1)
	case KindObject:
		return w.object(t, plen)
	case KindList:
		count := w.next()
		if zeroSize(t.Elem) {
			if count > maxEmptyElements {
				panic(formatErr(w.end, "list of empty objects is too long to decode"))
			}
			out := make(listNode, count)
			for i := range out {
				out[i] = w.object(t.Elem, 0)
			}
			return out
		}
		if count > uint64(len(w.vals)) {
			panic(formatErr(w.end, "list count exceeds the footer's VLQs"))
		}
		out := make(listNode, count)
		for i := range out {
			out[i] = w.node(t.Elem, plen+1+digits(i))
		}
		return out
	default:
		count := w.next()
		if count > uint64(len(w.vals)) {
			panic(formatErr(w.end, "map count exceeds the footer's VLQs"))
		}
		m := &mapNode{plen: plen, entries: make([]mapEntryNode, count)}
		for i := range m.entries {
			klen := w.next()
			if klen > uint64(w.end) {
				panic(formatErr(w.end, "map key length runs past the footer"))
			}
			first := len(w.leaves)
			child := w.node(t.Elem, plen+1+int64(klen))
			if len(w.leaves) == first {
				first = -1
			}
			m.entries[i] = mapEntryNode{klen, first, child}
		}
		return m
	}
}
