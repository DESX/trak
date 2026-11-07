package trak

import (
	"math"
	"math/bits"
)

// Jump returns J(n) from SPEC §7 in O(log n). n − J(n) is the smallest term of
// the greedy skew-binary decomposition of n into numbers of the form 2^k − 1.
func Jump(n uint64) uint64 {
	if n == 0 {
		return 0
	}
	r := n
	for {
		if r == math.MaxUint64 {
			return n - r
		}
		t := uint64(1)<<(bits.Len64(r+1)-1) - 1
		if t == r {
			return n - t
		}
		r -= t
	}
}
