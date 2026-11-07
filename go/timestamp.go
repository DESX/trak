package trak

import (
	"fmt"
	"time"
)

// TimestampLayout is the trak timestamp format, always in UTC (SPEC §6).
const TimestampLayout = "20060102T150405.000Z"

// FormatTimestamp formats t in UTC, truncated to the millisecond.
func FormatTimestamp(t time.Time) string {
	return t.UTC().Format(TimestampLayout)
}

// ParseTimestamp parses a trak timestamp.
func ParseTimestamp(s string) (time.Time, error) {
	if !validStampSyntax(s) {
		return time.Time{}, fmt.Errorf("bad timestamp %q, expected YYYYMMDDTHHMMSS.sssZ", s)
	}
	t, err := time.Parse(TimestampLayout, s)
	if err == nil && t.Year() == 0 {
		err = fmt.Errorf("year out of range")
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("bad timestamp %q: %v", s, err)
	}
	return t, nil
}

func validStampSyntax(s string) bool {
	if len(s) != 20 || s[8] != 'T' || s[15] != '.' || s[19] != 'Z' {
		return false
	}
	for i := 0; i < 19; i++ {
		if i == 8 || i == 15 {
			continue
		}
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// toMillis converts t to milliseconds since the epoch, rounding down.
func toMillis(t time.Time) int64 { return t.UnixMilli() }

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// appendStamp appends the trak timestamp for ms without going through
// time.Format, which is slow enough to matter on the write path.
func appendStamp(dst []byte, ms int64) []byte {
	days := ms / 86_400_000
	if ms%86_400_000 < 0 {
		days--
	}
	rem := ms - days*86_400_000
	// Howard Hinnant's civil_from_days.
	z := days + 719468
	era := z / 146097
	if z < 0 && z%146097 != 0 {
		era--
	}
	doe := z - era*146097
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	y := yoe + era*400
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	d := doy - (153*mp+2)/5 + 1
	m := mp + 3
	if mp >= 10 {
		m = mp - 9
	}
	if m <= 2 {
		y++
	}
	put := func(v int64, width int) {
		for i := width - 1; i >= 0; i-- {
			dst = append(dst, 0)
		}
		for i := len(dst) - 1; width > 0; i, width = i-1, width-1 {
			dst[i] = byte('0' + v%10)
			v /= 10
		}
	}
	put(y, 4)
	put(m, 2)
	put(d, 2)
	dst = append(dst, 'T')
	put(rem/3_600_000, 2)
	put(rem/60_000%60, 2)
	put(rem/1000%60, 2)
	dst = append(dst, '.')
	put(rem%1000, 3)
	return append(dst, 'Z')
}
