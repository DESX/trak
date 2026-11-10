# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
The spec and the three libraries share one version number.

## [Unreleased]

## [1.0.0] - unreleased

The first release.

### Added

- **SPEC.md**, trak 1.0. It includes the clarifications D1 to D6 in
  [docs/DECISIONS.md](docs/DECISIONS.md):
  - D1: how type prefixes and suffixes bind
  - D2: raw map keys in paths
  - D3: map entries with no leaves
  - D4: repeated map keys
  - D5: no leading zeros in VLQs
  - D6: `{}` as an empty object or the map prefix
- **Python library** `trak-log` (import `trak`), Python 3.10 or later, with no
  dependencies. It includes a reader, writer and `check`.
- **Go library** `github.com/desx/trak/go`, Go 1.22 or later, with no
  dependencies outside the standard library. It includes a reader, writer and
  `check`, plus native fuzz targets.
- **C++17 library**, the CMake target `trak::trak`, with no dependencies. It
  includes a reader, writer and `check`, plus a libFuzzer target.
- **`trak` command line tool**, with `types`, `schema`, `cat`, `export`,
  `tail -f` and `check`.
- **Conformance suite** with 33 valid, 18 invalid and 25 write vectors, shared by
  all three libraries.
- **Cross-implementation property tests:** every writer produces identical bytes,
  and every reader decodes every writer's files.
- **Navigation bound tests** on 1M-event files, and benchmarks in
  [bench/](bench).

[Unreleased]: https://github.com/desx/trak/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/desx/trak/releases/tag/v1.0.0
