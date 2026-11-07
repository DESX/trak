from __future__ import annotations

import pathlib

import pytest

ROOT = pathlib.Path(__file__).resolve().parents[2]


def spec_example() -> bytes:
    """The §13 example, taken from SPEC.md so the spec stays the single source."""
    text = (ROOT / "SPEC.md").read_text(encoding="utf-8")
    section = text[text.index("## 13. Example") :]
    start = section.index("```\n") + 4
    return section[start : section.index("```", start)].encode("utf-8")


BUILD = """// One CI build run.
{
  user: str,                  // who triggered the build
  status: enum(ok, failed),
  duration: num @ms,          // wall-clock time
  tags: [] str,
  stdout: |str @log,          // compiler output
  error: str?,                // set when the build failed
}
"""

DEPLOY = """// A release pushed to an environment.
{
  target: str,
  version: str @semver,
}
"""

EVENTS = [
    (
        {
            "user": "ana",
            "status": "ok",
            "duration": 910,
            "tags": ["ci"],
            "stdout": "Compiling core...\nDone.",
        },
        "20261107T140210.120Z",
    ),
    (
        {
            "user": "joel",
            "status": "failed",
            "duration": 1520,
            "tags": ["ci", "nightly"],
            "stdout": "Compiling core...\nld: undefined symbol main",
            "error": "link failed",
        },
        "20261107T140322.415Z",
    ),
]


@pytest.fixture
def example(tmp_path: pathlib.Path) -> pathlib.Path:
    p = tmp_path / "example.trak"
    p.write_bytes(spec_example())
    return p
