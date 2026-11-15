# Benchmarks

The writer policies compared on Sophie logs, and the viewer timed against its targets on them.

Both files come from `sophie-gen --size 5GB --seed 1`, one per policy. They hold the same content:
- 3,280,851 versions
- 334 sessions and 1,008,786 turns
- the largest session at 413,336 turns
- 4,110 changes to the defaults

They differ only in how footers lay out their nodes ([docs/DECISIONS.md](../docs/DECISIONS.md) P1).

Machine: Intel Core i3-10110U (2 cores, 4 threads, 2.1 GHz), 15 GB RAM, a SATA SSD, Linux 6.8, Go 1.22, Node 20, headless Chromium 1228.

## Policies

| Policy | Size | Footers | Footer bytes per append (mean / p99 / max) | Nodes: full / delta | Reads, latest version (mean / max) | Reads, old version (mean / max) | Cold lookup (mean) |
|---|---:|---:|---|---|---|---|---:|
| reference | 5.34 GB | 31.6% | 514 / 789 / 918 | 32,728,637 / 0 | 8.4 / 10 | 45.9 / 53 | 0.28 ms |
| delta | 4.51 GB | 19.1% | 263 / 362 / 473 | 11,443,584 / 21,285,053 | 11.9 / 14 | 49.3 / 58 | 0.18 ms |

Measured by `go run ./internal/bench`: footers from a full check of each file, lookups over 400 random turns at the latest version and 400 at random old versions.

- **Footer bytes per append** is the footer line: the path, version and timestamp, then the VLQs of the nodes on the changed path.
- **Reads** counts the footers a cold lookup of a random turn's `reply` reads, with the footer cache emptied first. At the latest version it starts from the last footer. At an old version, most of them are the version walk: jumps from the latest version back to the one asked for, about 2 log₂ n footers. Delta adds about two reads at each level, one per base.

**Recommended default: delta.** For the same content it writes 49% fewer footer bytes, which makes the file 15% smaller. It writes as fast as the reference policy. It costs three or four more footer reads per cold lookup, but those reads are smaller, so a lookup is faster, and it is no slower in the viewer. The reference policy stays as the byte-exact policy, which a second writer implementation can be checked against.

## Viewer

`viewer/test/e2e.test.js` runs the viewer in headless Chromium against `trak serve` on localhost.

- **Open:** from the click in the file picker to the root drawn: schema, root leaves and every list's count.
- **Jump:** typing a row number and pressing Enter, until the page is drawn. There are 49 jumps: 25 across the largest session's 413,336 turns, the rest across six other lists.
- **Scroll:** 30 page and row steps from a spot no earlier jump visited.
- **Step:** the defaults' version scrubber, moved one version at a time, 50 times, at both ends of its history.

Times are from the action to the drawn view, measured in the page. A cold run evicts the file from the page cache first.

| Policy | Cache | Open | Jump to a row (p50 / p95 / max) | Scroll a page (p50 / p95 / max) | Scrubber step (p50 / p95 / max) | Heap |
|---|---|---:|---|---|---|---:|
| reference | cold | 54 ms | 32 / 56 / 65 ms | 14 / 25 / 25 ms | 1 / 3 / 39 ms | 73 MB |
| reference | warm | 33 ms | 24 / 46 / 49 ms | 14 / 29 / 29 ms | 1 / 4 / 16 ms | 77 MB |
| delta | cold | 61 ms | 36 / 48 / 59 ms | 17 / 36 / 36 ms | 1 / 5 / 12 ms | 54 MB |
| delta | warm | 38 ms | 29 / 42 / 48 ms | 15 / 26 / 27 ms | 1 / 5 / 24 ms | 55 MB |
| delta | warm, UI 1.1.0 | 45 ms | 29 / 42 / 60 ms | 15 / 24 / 25 ms | 1 / 8 / 25 ms | 55 MB |

The first four rows are UI 1.0.0. The last is UI 1.1.0, which draws the table as CSS grid rows with fitted columns, on the regenerated files that carry a description.

Targets: open under 200 ms, jump under 100 ms, a page of rows under 50 ms with no blank frames, a scrubber step under 50 ms, and memory independent of file size. All four runs meet every target. No page was ever drawn partly empty: rows are drawn only once every row of the page is read.

Memory is bounded by the reader's caches, 64 MB of blocks and at most 100,000 parsed footers, not by the file. Across a whole run, about 120 jumps and steps, the viewer read about 100 MB, 2% of the file. Most of that was 16 KB blocks around the rows each page showed. Dragging the scrollbar from the top to the middle of the largest list read about 10 MB, for the pages it stopped at on the way, and landed on row 206,655 of 413,336.

## Write throughput

| Policy | Appends | Seconds | Appends/s | MB/s |
|---|---:|---:|---:|---:|
| reference | 664,076 | 14.1 | 46,952 | 74 |
| delta | 664,076 | 14.7 | 45,285 | 61 |

Writing 1 GB of Sophie log with `sophie-gen`, which includes generating the content. Both policies write about 46,000 appends a second.

## Reproduce

```sh
cd go
go run ./cmd/sophie-gen --size 5GB --seed 1 --policy reference /data/sophie-reference.trak
go run ./cmd/sophie-gen --size 5GB --seed 1 --policy delta /data/sophie-delta.trak
go run ./internal/bench --write 1000MB /data/sophie-reference.trak /data/sophie-delta.trak

cd ../viewer/test && npm ci && npx playwright install chromium
TRAK_DATA=/dir/with/one/file TRAK_PERF_OUT=perf.json npm test
```
