# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
The spec and the libraries share one version number.

## [Unreleased]

## [1.2.0] - Unreleased

A new format: trak 1.2 holds one versioned tree, not a table per type. It
doesn't read or write 1.0 or 1.1 files, and the libraries that did are gone.

### Added

- **SPEC.md**, trak 1.2: one root object whose event lists are append-only, every
  append a full version written by path copying, any field of any version in
  O(log n) reads. [docs/DECISIONS.md](docs/DECISIONS.md) records where it differs
  from the draft it started from.
- **Go**: writer with two policies (reference and delta), reader, and check, which
  verifies every version as an exact path copy of the one before.
- **JavaScript reader** over memory, `File`, HTTP Range and `trak serve`, batching
  every read in one turn of the event loop into one request.
- **sophie-gen**: synthetic logs of [Sophie](docs/sophie.md) at any size, from a
  seed and a policy.
- **trak serve** and a new viewer: a pane of open files, and tabs of tree, table
  and log views. Every view has a URL, and a debug panel lists every byte range
  read.
- Conformance vectors, written by the Go tests and read by the Go and JS tests.
- A browser test that times the viewer's responsiveness targets on each file, and
  bench/README.md with the policy comparison.

### Removed

- The Python and C++ libraries, the 1.1 JavaScript reader and viewer, families,
  lists and maps as values, and `@` annotations.

## [1.1.0] - 2026-11-10

The first release. Version 1.0.0 was never released; its changes are folded in
here.

### Added

- **SPEC.md**, trak 1.1. It includes the clarifications D1 to D6 and the event
  families of D7 in [docs/DECISIONS.md](docs/DECISIONS.md):
  - D1: how type prefixes and suffixes bind
  - D2: raw map keys in paths
  - D3: map entries with no leaves
  - D4: repeated map keys
  - D5: no leading zeros in VLQs
  - D6: `{}` as an empty object or the map prefix
  - D7: event families: `build.step` is a child of `build`, every event
    records its row in each enclosing family, and a family's row `m` is found
    in O(k log n). Seeks by any non-decreasing key are O(log n).
- **Python library** `trak-log` (import `trak`), Python 3.10 or later, with no
  dependencies. It includes a reader, writer and `check`.
- **Go library** `github.com/desx/trak/go`, Go 1.22 or later, with no
  dependencies outside the standard library. It includes a reader, writer and
  `check`, plus native fuzz targets.
- **C++17 library**, the CMake target `trak::trak`, with no dependencies. It
  includes a reader, writer and `check`, plus a libFuzzer target.
- **JavaScript reader** for browsers and Node.js 18 or later, with no
  dependencies, reading through byte sources (File, HTTP Range, memory).
- In every library: family tables, `prev()`, file-order paging with cursors
  (`before` and `after`), write positions (`n` and offset), and directory
  fsync on create with `sync`.
- **`trak` command line tool**, with `types`, `schema`, `cat` (and
  `cat --family`), `export`, `tail -f`, `check` and `serve`.
- **Web viewer**, served by `trak serve`: a log view and virtual-scrolling
  tables for types and families, reading only the byte ranges it shows.
- **Conformance suite** with 41 valid, 23 invalid and 30 write vectors, shared by
  all four libraries.
- **Cross-implementation property tests:** every writer produces identical bytes,
  and every reader decodes every writer's files, families included.
- **Navigation bound tests** on 1M-event files, browser end-to-end tests of the
  viewer, and benchmarks in [bench/](bench).

[Unreleased]: https://github.com/desx/trak/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/desx/trak/releases/tag/v1.1.0
