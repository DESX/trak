package trak

import (
	"errors"
	"math"
	"math/rand"
	"strings"
	"testing"
)

func TestVLQSpecTable(t *testing.T) {
	for v, s := range map[uint64]string{0: "A", 25: "Z", 26: "bA", 148: "fS", 1520: "cgM", 52310: "czjY"} {
		if got := string(AppendVLQ(nil, v)); got != s {
			t.Errorf("AppendVLQ(%d) = %s, want %s", v, got, s)
		}
		if got, err := DecodeVLQs([]byte(s), 0); err != nil || len(got) != 1 || got[0] != v {
			t.Errorf("DecodeVLQs(%s) = %v, %v", s, got, err)
		}
	}
}

func TestVLQRoundTripRange(t *testing.T) {
	limit := uint64(10_000_000)
	if testing.Short() {
		limit = 100_000
	}
	buf := make([]byte, 0, 16)
	for v := uint64(0); v < limit; v++ {
		buf = AppendVLQ(buf[:0], v)
		got, err := DecodeVLQs(buf, 0)
		if err != nil || len(got) != 1 || got[0] != v {
			t.Fatalf("round trip %d: %v %v", v, got, err)
		}
	}
}

func TestVLQRandom64(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var values []uint64
	var buf []byte
	for i := 0; i < 20000; i++ {
		v := rng.Uint64() >> rng.Intn(64)
		values = append(values, v)
		buf = AppendVLQ(buf, v)
	}
	values = append(values, math.MaxUint64)
	buf = AppendVLQ(buf, math.MaxUint64)
	got, err := DecodeVLQs(buf, 0)
	if err != nil || len(got) != len(values) {
		t.Fatal(err)
	}
	for i := range values {
		if got[i] != values[i] {
			t.Fatalf("value %d: %d != %d", i, got[i], values[i])
		}
	}
}

func TestVLQErrors(t *testing.T) {
	for s, msg := range map[string]string{
		"aB":                          "leading zero",
		"bAaB":                        "leading zero",
		"b":                           "final capital",
		"A1":                          "bad VLQ letter",
		strings.Repeat("p", 14) + "A": "overflows",
	} {
		_, err := DecodeVLQs([]byte(s), 100)
		var fe *FormatError
		if !errors.As(err, &fe) || !strings.Contains(fe.Msg, msg) || fe.Offset < 100 {
			t.Errorf("DecodeVLQs(%q) = %v, want %q", s, err, msg)
		}
	}
}

func jumpRecurrence(limit int) []uint64 {
	j := make([]uint64, limit+1)
	for n := 1; n <= limit; n++ {
		p := uint64(n - 1)
		if jp := j[p]; p > jp && p-jp == jp-j[jp] {
			j[n] = j[jp]
		} else {
			j[n] = p
		}
	}
	return j
}

func TestJumpMatchesRecurrence(t *testing.T) {
	for n, want := range jumpRecurrence(1_000_000) {
		if got := Jump(uint64(n)); got != want {
			t.Fatalf("Jump(%d) = %d, want %d", n, got, want)
		}
	}
	if Jump(math.MaxUint64) != 0 || Jump(1<<40) != 1<<40-1 {
		t.Fatal("large jumps")
	}
}

func TestAppendStampMatchesTimeFormat(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	ms := []int64{0, -1, -86_400_000, 1_794_000_000_000, 253402300799999, -62135596800000}
	for i := 0; i < 100000; i++ {
		ms = append(ms, rng.Int63n(253402300799999+62135596800000)-62135596800000)
	}
	for _, m := range ms {
		if got, want := string(appendStamp(nil, m)), FormatTimestamp(fromMillis(m)); got != want {
			t.Fatalf("%d: %s != %s", m, got, want)
		}
	}
}
