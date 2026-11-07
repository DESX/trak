from __future__ import annotations

from trak._jump import jump, jump_recurrence


def test_matches_recurrence() -> None:
    expected = jump_recurrence(1_000_000)
    for n, j in enumerate(expected):
        assert jump(n) == j, n


def test_small_values() -> None:
    assert [jump(n) for n in range(8)] == [0, 0, 1, 0, 3, 4, 3, 0]


def test_large() -> None:
    n = (1 << 40) - 1
    assert jump(n) == 0
    assert jump(n + 1) == n
