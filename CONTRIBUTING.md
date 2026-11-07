# Contributing

## Python

```sh
cd python
python -m venv .venv && . .venv/bin/activate
pip install -e '.[dev]'
pytest
mypy
ruff check . && ruff format --check .
```

## Go

```sh
cd go
go test ./...
go vet ./...
staticcheck ./...
```

## C++

```sh
cmake -S cpp -B cpp/build -DCMAKE_BUILD_TYPE=Debug
cmake --build cpp/build
ctest --test-dir cpp/build --output-on-failure
```

## Commits

One logical change per commit, using
[Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`,
`test:`, `docs:`, `ci:`).

## Spec changes

`SPEC.md` is normative. Clarifications are agreed first and recorded in
[docs/DECISIONS.md](docs/DECISIONS.md). A PATCH change rewords the spec; a
MINOR change adds bytes that older readers can skip.
