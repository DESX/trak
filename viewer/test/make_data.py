"""Write the viewer's end-to-end test data: a build log with a family of four types.

    python make_data.py OUT_DIR [EVENTS]

Writes OUT_DIR/demo.trak, OUT_DIR/sub/027-family-deep.trak and OUT_DIR/expect.json with
the counts and sample rows the browser test checks against.
"""

from __future__ import annotations

import json
import pathlib
import random
import shutil
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "python" / "src"))

import trak  # noqa: E402

BUILD = """// One CI build run.
{
  user: str,                  // who triggered the build
  status: enum(ok, failed, running),
  duration: num @ms,          // wall-clock time
  tags: [] str,
  host: { name: str, region: enum(us, eu, ap), cpu: { cores: num, model: str } },
  env: {} str,
  stdout: |str @log,          // compiler output
  error: str?,                // set when the build failed
}"""


def write(path: pathlib.Path, n: int) -> None:
    rng = random.Random(1)
    with trak.create(path, overwrite=True) as w:
        w.define("build", BUILD)
        w.define("build.step", "// One step of a build.\n{ step: str, exit: num }")
        w.define("build.step.cmd", "{ argv: [] str, out: |str? }")
        w.define("build.test", "{ suite: str, passed: num, failed: num }")
        w.define("deploy", "// A release pushed to an environment.\n{ target: enum(staging, prod), version: str @semver }")
        ms = 1_794_000_000_000
        for i in range(n):
            ms += rng.choice([1, 5, 40, 250, 1000])
            ts = trak._time.from_ms(ms)
            base = {
                "user": rng.choice(["ana", "joel", "bo", "kim"]),
                "status": rng.choice(["ok", "ok", "failed", "running"]),
                "duration": rng.randint(10, 90000),
                "tags": rng.sample(["ci", "nightly", "release", "pr"], rng.randint(0, 3)),
                "host": {"name": f"runner-{rng.randint(1, 40)}", "region": rng.choice(["us", "eu", "ap"]),
                         "cpu": {"cores": rng.choice([4, 8, 16]), "model": "x86-64"}},
                "env": [["CC", "gcc"], ["OPT", "-O2"]][: rng.randint(0, 2)],
                "stdout": "\n".join(f"[{k}] compiling module_{k}.c" for k in range(rng.randint(1, 30))),
            }
            r = rng.random()
            if r < 0.15:
                w.write("build", {**base, "error": "link failed" if base["status"] == "failed" else None}, ts)
            elif r < 0.55:
                w.write("build.step", {**base, "step": rng.choice(["fetch", "compile", "link"]), "exit": rng.choice([0, 1])}, ts)
            elif r < 0.8:
                w.write("build.step.cmd", {**base, "step": "compile", "exit": 0, "argv": ["cc", "-c", f"m{i}.c"],
                                           "out": None if rng.random() < 0.5 else "warning: unused\n" * 3}, ts)
            elif r < 0.95:
                w.write("build.test", {**base, "suite": "unit", "passed": rng.randint(0, 900), "failed": rng.randint(0, 3)}, ts)
            else:
                w.write("deploy", {"target": rng.choice(["staging", "prod"]), "version": f"1.{i}.0"}, ts)


def main() -> None:
    out = pathlib.Path(sys.argv[1])
    n = int(sys.argv[2]) if len(sys.argv) > 2 else 20000
    (out / "sub").mkdir(parents=True, exist_ok=True)
    write(out / "demo.trak", n)
    shutil.copy(ROOT / "conformance" / "valid" / "027-family-deep.trak", out / "sub")
    with trak.open(out / "demo.trak") as r:
        family = r.family("build")
        rows = family.count()
        samples = {}
        for m in (1, 2, rows // 3, rows // 2, rows):
            e = family.get(m)
            samples[str(m)] = {"type": e.type, "n": e.n, "user": e.get("user")}
        expect = {
            "counts": {s.name: r.table(s.name).count() for s in r.types()},
            "family_rows": rows,
            "samples": samples,
            "last": {"type": family.latest().type, "n": family.latest().n},
        }
    (out / "expect.json").write_text(json.dumps(expect, indent=2))


if __name__ == "__main__":
    main()
