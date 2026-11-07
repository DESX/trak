package trak

import (
	"bytes"
	"strconv"
)

var (
	schemaOpen  = []byte("##[ ")
	schemaClose = []byte(" ]##")
	eventOpen   = []byte("==[ ")
	eventClose  = []byte(" ]==")
)

// footer is a parsed footer line. Start is the object's first byte, FStart
// the footer's first byte and End the byte after the footer's LF.
type footer struct {
	schema bool
	start  int64
	fstart int64
	end    int64
	name   string
	num    uint64 // schema: type index; event: n
	stamp  string // schema: version; event: timestamp
	vlqs   []uint64
	inline [16]uint64 // backing for vlqs when they fit, saving an allocation
}

func (f *footer) node() uint64 {
	if f.schema {
		return 0
	}
	return f.num
}

func (f *footer) values() []uint64 { return f.vlqs[6+f.vlqs[5]:] }

// target resolves a pointer stored in this footer to the target's end offset.
func (f *footer) target(pointer uint64) (int64, error) {
	if pointer == 0 {
		return 0, formatErr(f.fstart, "required pointer is 0")
	}
	if pointer-1 > uint64(f.start) {
		return 0, formatErr(f.fstart, "pointer reaches before the start of the file")
	}
	return f.start - int64(pointer-1), nil
}

func isDecimal(b []byte) bool {
	if len(b) == 0 || b[0] < '1' || b[0] > '9' {
		return false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// canonicalVersion reports whether b is MAJOR.MINOR with no leading zeros.
func canonicalVersion(b []byte) bool {
	major, minor, ok := bytes.Cut(b, []byte("."))
	return ok && isVersion(b) && (len(major) == 1 || major[0] != '0') && (len(minor) == 1 || minor[0] != '0')
}

func isVersion(b []byte) bool {
	i := bytes.IndexByte(b, '.')
	if i <= 0 || i == len(b)-1 {
		return false
	}
	for j, c := range b {
		if j != i && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// parseFooter parses buf[fstart:end] (including its LF). intern, when not
// nil, turns the type name into a string without allocating for names it
// has seen.
func parseFooter(buf []byte, fstart, end, minStart int64, intern func([]byte) string) (*footer, error) {
	line := buf[fstart : end-1]
	f := &footer{fstart: fstart, end: end}
	var close []byte
	switch {
	case bytes.HasPrefix(line, schemaOpen):
		f.schema, close = true, schemaClose
	case bytes.HasPrefix(line, eventOpen):
		close = eventClose
	default:
		return nil, formatErr(fstart, "line is not a footer")
	}
	k := bytes.Index(line[4:], close)
	if k < 0 {
		return nil, formatErr(fstart, "footer has no closing marker")
	}
	k += 4
	var parts [3][]byte
	fields := line[4:k]
	for i := 0; i < 3; i++ {
		sp := bytes.IndexByte(fields, ' ')
		if i < 2 && sp < 0 || i == 2 && sp >= 0 {
			return nil, formatErr(fstart+4, "footer needs exactly three fields")
		}
		if sp < 0 {
			sp = len(fields)
		}
		parts[i] = fields[:sp]
		if len(parts[i]) == 0 {
			return nil, formatErr(fstart+4, "footer needs exactly three fields")
		}
		if i < 2 {
			fields = fields[sp+1:]
		}
	}
	letters := line[k+4:]
	if len(letters) == 0 {
		return nil, formatErr(fstart+int64(k)+4, "footer has no VLQs")
	}
	vlqs, err := appendVLQs(f.inline[:0], letters, fstart+int64(k)+4)
	if err != nil {
		return nil, err
	}
	f.vlqs = vlqs
	for _, c := range parts[0] {
		if c >= 0x80 {
			return nil, formatErr(fstart+4, "type name is not ASCII")
		}
	}
	if intern != nil {
		f.name = intern(parts[0])
	} else {
		f.name = string(parts[0])
	}
	if !isDecimal(parts[1]) {
		return nil, formatErr(fstart+5+int64(len(parts[0])), "bad decimal number in footer")
	}
	f.num, err = strconv.ParseUint(string(parts[1]), 10, 64)
	if err != nil {
		return nil, formatErr(fstart+5+int64(len(parts[0])), "footer number overflows")
	}
	stampAt := fstart + 6 + int64(len(parts[0])+len(parts[1]))
	f.stamp = string(parts[2])
	if f.schema {
		if !isVersion(parts[2]) {
			return nil, formatErr(stampAt, "bad version in schema footer")
		}
		if len(vlqs) < 3 || uint64(len(vlqs)-3) != vlqs[2] {
			return nil, formatErr(fstart+int64(k)+4, "schema footer has the wrong number of VLQs")
		}
	} else {
		if !validStampSyntax(f.stamp) {
			return nil, formatErr(stampAt, "bad timestamp in event footer")
		}
		if len(vlqs) < 6 || uint64(len(vlqs)-6) < vlqs[5] {
			return nil, formatErr(fstart+int64(k)+4, "event footer has too few VLQs")
		}
	}
	if vlqs[0] > uint64(fstart-minStart) {
		return nil, formatErr(fstart+int64(k)+4, "body length reaches past the first object")
	}
	f.start = fstart - int64(vlqs[0])
	return f, nil
}

func appendSchemaFooter(dst []byte, name string, index int, version string, vlqs []uint64) []byte {
	dst = append(dst, schemaOpen...)
	dst = append(dst, name...)
	dst = append(dst, ' ')
	dst = strconv.AppendInt(dst, int64(index), 10)
	dst = append(dst, ' ')
	dst = append(dst, version...)
	dst = append(dst, schemaClose...)
	for _, v := range vlqs {
		dst = AppendVLQ(dst, v)
	}
	return append(dst, '\n')
}

func appendEventFooter(dst []byte, name string, n uint64, stamp []byte, vlqs []uint64) []byte {
	dst = append(dst, eventOpen...)
	dst = append(dst, name...)
	dst = append(dst, ' ')
	dst = strconv.AppendUint(dst, n, 10)
	dst = append(dst, ' ')
	dst = append(dst, stamp...)
	dst = append(dst, eventClose...)
	for _, v := range vlqs {
		dst = AppendVLQ(dst, v)
	}
	return append(dst, '\n')
}
