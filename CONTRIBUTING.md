# Contributing

The spec ([SPEC.md](SPEC.md)) is normative. The libraries implement it exactly,
and the three writers (Python, Go, C++) must produce identical bytes. Every change must keep
the conformance suite and the cross-implementation tests passing.

## Python

```sh
cd python
python -m venv .venv && . .venv/bin/activate
pip install -e '.[dev]'
pytest                      # TRAK_SLOW=1 for full-size runs (1M-event bounds test)
mypy
ruff check . && ruff format --check .
```

## Go

```sh
cd go
go test ./...               # -short skips the largest cases
go vet ./...
staticcheck ./...
go test -run '^$' -fuzz FuzzReader -fuzztime 60s .
go test -run '^$' -fuzz FuzzSchemaParser -fuzztime 60s .
```

## C++

```sh
cmake -S cpp -B cpp/build -DCMAKE_BUILD_TYPE=Debug -DTRAK_WERROR=ON -DTRAK_SANITIZE=ON
cmake --build cpp/build --parallel
ctest --test-dir cpp/build --output-on-failure
clang-tidy -p cpp/build cpp/src/*.cpp     # needs -DCMAKE_EXPORT_COMPILE_COMMANDS=ON
```

The tests fetch GoogleTest and nlohmann/json with FetchContent. The library has
no dependencies.

## JavaScript and the viewer

```sh
cd js && npm test                         # unit tests and the conformance vectors
cd viewer/test && npm ci && npx playwright install chromium && npm test
cd go/cmd/trak && go generate             # after changing viewer/ or js/src
```

The viewer has no build step: `viewer/` is plain ES modules that import
`js/src`. `go generate` copies both into the CLI, which embeds them for
`trak serve`; CI fails if the copy is stale. The end-to-end tests build the CLI,
write test data with Python, and drive the viewer in headless Chromium.

## Conformance vectors and cross-checks

```sh
python conformance/generate.py            # regenerate the vectors; review any diff by hand
cd go && go build -o interop ./internal/interop && cd ..
cmake --build cpp/build --target trak_interop
python conformance/crosscheck.py --seeds 200 \
  --impl python="python python/tests/interop.py" \
  --impl go=go/interop --impl cpp=cpp/build/trak_interop \
  --reader js="node js/tools/interop.js"
python docs/check_readme.py               # run the README quickstarts
```

CI regenerates the vectors and fails if they change.

## Commits

One logical change per commit, using
[Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`,
`test:`, `docs:`, `ci:`, `perf:`).

## Spec changes

Agree a clarification first and record it in
[docs/DECISIONS.md](docs/DECISIONS.md). A PATCH change rewords the spec and
never changes bytes. A MINOR change adds bytes that older readers can skip.

## Releasing

1. Move the `Unreleased` notes in CHANGELOG.md under the new version, with the
   date.
2. Set the version in `python/pyproject.toml`, `python/src/trak/__init__.py`,
   `cpp/CMakeLists.txt`, `cpp/src/core.cpp` and `js/package.json`.
3. Tag `vX.Y.Z` and push the tag. The release workflow:
   - creates the matching `go/vX.Y.Z` tag that Go needs for the `go/` module
   - publishes `trak-log` to PyPI
   - attaches CLI binaries to the GitHub release
