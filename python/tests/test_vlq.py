from __future__ import annotations

import os
import random

import pytest

from trak import FormatError
from trak._vlq import MAX, decode, encode, encode_all

SPEC_TABLE = {0: "A", 25: "Z", 26: "bA", 148: "fS", 1520: "cgM", 52310: "czjY"}


@pytest.mark.parametrize(("value", "text"), SPEC_TABLE.items())
def test_spec_table(value: int, text: str) -> None:
    assert encode(value) == text.encode()
    assert decode(text.encode()) == [value]


def test_round_trip_range() -> None:
    limit = 10_000_000 if os.environ.get("TRAK_SLOW") else 1_000_000
    for v in range(limit):
        assert decode(encode(v)) == [v]


def test_round_trip_random_64_bit() -> None:
    rng = random.Random(1)
    values = [rng.getrandbits(rng.randint(1, 64)) for _ in range(20_000)] + [MAX]
    assert decode(encode_all(values)) == values


def test_concatenated() -> None:
    assert decode(b"kXAA") == [283, 0, 0]
    assert encode_all([146, 1, 1, 139]) == b"fQBBfJ"


@pytest.mark.parametrize(
    ("text", "message"),
    [
        (b"aB", "leading zero"),
        (b"bAaB", "leading zero"),
        (b"b", "final capital"),
        (b"A1", "bad VLQ letter"),
        (b"A-", "bad VLQ letter"),
        (b"p" * 14 + b"A", "overflows"),
    ],
)
def test_decode_errors(text: bytes, message: str) -> None:
    with pytest.raises(FormatError, match=message):
        decode(text)


def test_decode_error_offset() -> None:
    with pytest.raises(FormatError) as e:
        decode(b"AB!", offset=100)
    assert e.value.offset == 102


def test_encode_range() -> None:
    with pytest.raises(OverflowError):
        encode(-1)
    with pytest.raises(OverflowError):
        encode(MAX + 1)
    assert decode(encode(MAX)) == [MAX]
