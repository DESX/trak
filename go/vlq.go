package trak

import "math"

// AppendVLQ appends the letter VLQ for v (SPEC §4).
func AppendVLQ(dst []byte, v uint64) []byte {
	if v < 26 {
		return append(dst, byte('A'+v))
	}
	var buf [14]byte
	i := len(buf) - 1
	buf[i] = byte('A' + v%26)
	v /= 26
	for v > 0 {
		i--
		buf[i] = byte('a' + v%26)
		v /= 26
	}
	return append(dst, buf[i:]...)
}

// DecodeVLQs decodes concatenated letter VLQs. offset locates data in the
// file for error messages.
func DecodeVLQs(data []byte, offset int64) ([]uint64, error) {
	return appendVLQs(make([]uint64, 0, len(data)), data, offset)
}

// appendVLQs decodes data like DecodeVLQs, appending to out.
func appendVLQs(out []uint64, data []byte, offset int64) ([]uint64, error) {
	var v uint64
	started := false
	for i, c := range data {
		var d uint64
		switch {
		case c >= 'A' && c <= 'Z':
			d = uint64(c - 'A')
		case c >= 'a' && c <= 'z':
			d = uint64(c - 'a')
			if !started && d == 0 {
				return nil, formatErr(offset+int64(i), "VLQ has a leading zero digit")
			}
		default:
			return nil, formatErr(offset+int64(i), "bad VLQ letter %q", c)
		}
		if v > (math.MaxUint64-d)/26 {
			return nil, formatErr(offset+int64(i), "VLQ overflows 64 bits")
		}
		v = v*26 + d
		if c <= 'Z' {
			out = append(out, v)
			v, started = 0, false
		} else {
			started = true
		}
	}
	if started {
		return nil, formatErr(offset+int64(len(data)), "VLQ ends without a final capital letter")
	}
	return out, nil
}
