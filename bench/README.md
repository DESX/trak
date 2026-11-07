# Benchmarks

Each library runs the same workload: 1,000,000 small events written to a new file,
then read back. The writers are byte-identical, so all three produce the same
89 MB file (SHA-256 `acea79bf…`).

## Workload

Four types, with event `i` (0-based) of type `i % 4` and timestamp
`2026-11-06T21:20:00.000Z + i ms`:

| Type | Schema | Event `i` |
|---|---|---|
| `build` | `{ user: str, status: enum(ok, failed), duration: num, tags: [] str }` | `user{i%100}`, `failed` if `i%3==0` else `ok`, `i%5000`, `[ci]` or `[ci, nightly]` |
| `deploy` | `{ target: str, version: str }` | `prod`/`staging`, `1.{i%50}.0` |
| `log` | `{ msg: str }` | `request {i} served` |
| `metric` | `{ name: str, value: num }` | `latency`, `i%1000` |

| Measurement | What is timed |
|---|---|
| Write | Create the file and write all events with explicit timestamps, one `write` call per object, no fsync. This includes the time to build each value. |
| Open + `count()` | Open the file, count `build` events, close (warm page cache). |
| `get(random n)` | `build.get(n)` for random `n`, then read the `user` leaf. |
| Sequential read of 40 rows | `build.range(a, a+39)`, decoding every leaf of every row. |

## Results

Intel Core i3-10110U (4 threads, 2.1 GHz), 16 GB, Linux 6.8, ext4. Go 1.22.2,
GCC 13.3 (`-O3` via CMake Release), CPython 3.12.3. Median of three runs.

| Operation | Go | C++ | Python | Goal (Go / C++) | Goal (Python) |
|---|---|---|---|---|---|
| Open + `count()` | 52 µs | 45 µs | 0.34 ms | < 1 ms | < 5 ms |
| `get(random n)` | 7.8 µs | 9.2 µs | 137 µs | < 20 µs | < 300 µs |
| Sequential read of 40 rows | 60 µs | 40 µs | 0.85 ms | < 50 µs | < 1 ms |
| Write throughput | 562k events/s | 551k events/s | 44k events/s | > 500k/s | > 50k/s |

Two results miss their goals. The rest meet them with room to spare.

- **Go, sequential 40 rows (60 µs against 50 µs).** Most of the time is
  parsing footers and allocating the per-row event and decoded tree. Navigating
  to the start of each range also costs about 2·log₂(n) footer reads, which is
  significant next to 40 rows. A cursor API that reuses buffers across rows
  would close the gap.
- **Python, write (44k against 50k events/s).** The time includes building each
  value dict and formatting each timestamp in the benchmark loop.

## Running

```sh
# Python
python bench/bench_python.py --file /tmp/bench.trak

# Go
cd go && go run ./internal/bench -file /tmp/bench.trak

# C++
cmake -S cpp -B cpp/build-bench -DCMAKE_BUILD_TYPE=Release -DTRAK_BUILD_TESTS=OFF -DTRAK_BUILD_BENCH=ON
cmake --build cpp/build-bench
cpp/build-bench/trak_bench /tmp/bench.trak
```

Each program writes the file, prints its results, and leaves the file behind,
so `sha256sum` can confirm the three are identical.

## Navigation bounds

The read-count bounds are tests, not benchmarks. They run on a 1M-event file
with 8 types, one of them rare, in every language: `go/bounds_test.go`,
`cpp/tests/bounds_test.cpp` and `python/tests/test_bounds.py` (with
`TRAK_SLOW=1`).

| Operation | Bound (footer reads) |
|---|---|
| Open (recovery and loading schemas) | 2K + 4 |
| `count()` and `latest()` | 2K + 4 |
| `get(n)` | 3·log₂(n) + 4, where n is the type's event count |
