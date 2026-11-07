"""Run the README quickstarts as written (CI job `readme`).

Every fenced block preceded by `<!-- readme-test: LANG -->` is extracted and run
in one scratch directory, in README order:

- python: run with this interpreter (the package must be installed or on PYTHONPATH)
- go: built as a module that replaces github.com/buk/trak/go with ../go
- cpp: compiled against cpp/ (built with CMake into the scratch directory)
- cli: each command run with the trak CLI built from go/cmd/trak; `tail -f`
  runs as plain `tail` so it ends

The example file at the top of the README must also pass `trak check`.

    python docs/check_readme.py
"""

from __future__ import annotations

import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
EXE = ".exe" if os.name == "nt" else ""


def run(cmd: list[str], cwd: pathlib.Path, env: dict[str, str] | None = None) -> str:
    print("$", " ".join(cmd), flush=True)
    p = subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True, check=False)
    sys.stdout.write(p.stdout)
    if p.returncode != 0:
        sys.stderr.write(p.stderr)
        raise SystemExit(f"failed: {' '.join(cmd)}")
    return p.stdout


def main() -> None:
    readme = (ROOT / "README.md").read_text(encoding="utf-8")
    blocks = re.findall(r"<!-- readme-test: (\w+) -->\n```\w*\n(.*?)```", readme, re.DOTALL)
    assert {lang for lang, _ in blocks} == {"python", "go", "cpp", "cli"}, blocks
    with tempfile.TemporaryDirectory() as tmp:
        d = pathlib.Path(tmp)
        trak_cli = d / f"trak{EXE}"
        run(["go", "build", "-o", str(trak_cli), "./cmd/trak"], ROOT / "go")

        example = readme[readme.index("```\ntrak 1.0") + 4 :]
        (d / "example.trak").write_text(
            example[: example.index("```")], encoding="utf-8", newline=""
        )
        run([str(trak_cli), "check", "example.trak"], d)

        for lang, code in blocks:
            if lang == "python":
                env = dict(os.environ, PYTHONPATH=str(ROOT / "python" / "src"))
                (d / "quickstart.py").write_text(code, encoding="utf-8")
                out = run([sys.executable, "quickstart.py"], d, env)
                assert out.startswith("2 builds\n"), out
            elif lang == "go":
                g = d / "goquick"
                g.mkdir()
                (g / "main.go").write_text(code, encoding="utf-8")
                (g / "go.mod").write_text(
                    "module quickstart\n\ngo 1.22\n\nrequire github.com/buk/trak/go v0.0.0\n\n"
                    f"replace github.com/buk/trak/go => {(ROOT / 'go').as_posix()}\n",
                    encoding="utf-8",
                )
                out = run(["go", "run", "."], g)
                shutil.copy(g / "builds.trak", d / "builds.trak")
                assert out == "latest build by joel\n", out
            elif lang == "cpp":
                build = d / "cppbuild"
                run(
                    [
                        "cmake",
                        "-S",
                        str(ROOT / "cpp"),
                        "-B",
                        str(build),
                        "-DTRAK_BUILD_TESTS=OFF",
                        "-DCMAKE_BUILD_TYPE=Release",
                    ],
                    d,
                )
                run(["cmake", "--build", str(build), "--config", "Release", "--parallel"], d)
                (d / "quickstart.cpp").write_text(code, encoding="utf-8")
                lib = next(p for p in build.rglob("*") if p.name in ("libtrak.a", "trak.lib"))
                if os.name == "nt":
                    run(
                        [
                            "cl",
                            "/nologo",
                            "/std:c++17",
                            "/EHsc",
                            f"/I{ROOT / 'cpp' / 'include'}",
                            "quickstart.cpp",
                            str(lib),
                        ],
                        d,
                    )
                else:
                    cxx = os.environ.get("CXX", "c++")
                    run(
                        [
                            cxx,
                            "-std=c++17",
                            f"-I{ROOT / 'cpp' / 'include'}",
                            "quickstart.cpp",
                            str(lib),
                            "-o",
                            "quickstart",
                        ],
                        d,
                    )
                out = run([str(d / f"quickstart{EXE}")], d)
                assert out == "1 build by ana\n", out
            else:
                for line in code.splitlines():
                    cmd = line.split("#")[0].split()
                    if not cmd:
                        continue
                    assert cmd[0] == "trak", line
                    if cmd[1:3] == ["tail", "-f"]:
                        cmd = ["trak", "tail", *cmd[3:]]
                    run([str(trak_cli), *cmd[1:]], d)
    print("README quickstarts ok")


if __name__ == "__main__":
    main()
