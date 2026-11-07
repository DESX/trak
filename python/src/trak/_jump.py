"""Skew-binary jump targets (SPEC §7)."""

from __future__ import annotations


def jump(n: int) -> int:
    """Return J(n) in O(log n).

    ``n - J(n)`` is the smallest term of the greedy skew-binary decomposition
    of ``n`` into numbers of the form 2^k - 1.
    """
    if n <= 0:
        return 0
    r = n
    while True:
        t = (1 << ((r + 1).bit_length() - 1)) - 1
        if t == r:
            return n - t
        r -= t


def jump_recurrence(limit: int) -> list[int]:
    """J(0..limit) computed directly from the recurrence in SPEC §7. Used by tests."""
    j = [0] * (limit + 1)
    for n in range(1, limit + 1):
        p = n - 1
        jp = j[p]
        if p > jp and p - jp == jp - j[jp]:
            j[n] = j[jp]
        else:
            j[n] = p
    return j
