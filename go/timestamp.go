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
