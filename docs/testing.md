# Testing and coverage

Echo supports built-in Go tests and named, framework-free C test targets. Both use CodeLens actions in the editor and one reconnectable **Test Output** panel with Stop and Rerun. C targets treat the configured entry function only as the editor anchor: the executable and its internal test ledger remain authoritative.

Configure targets in **Settings → Testing** or in the portable `.echo/workspace.json` file:

```json
{
  "testing": {
    "c": {
      "codeLens": true,
      "coverage": true,
      "targets": [
        {
          "id": "unit",
          "name": "Unit tests",
          "entry": {
            "file": "${workspaceFolder}/tests/test_main.c",
            "function": "main"
          },
          "build": {
            "command": "cmake",
            "args": ["--build", "${workspaceFolder}/build", "--target", "unit_tests"],
            "cwd": "${workspaceFolder}",
            "environment": {},
            "timeout": "5m"
          },
          "executable": "${workspaceFolder}/build/unit_tests.exe",
          "args": [],
          "cwd": "${workspaceFolder}",
          "environment": {},
          "timeout": "30s",
          "sourceRoots": ["${workspaceFolder}/src", "${workspaceFolder}/include"],
          "coverage": {
            "provider": "gcov",
            "objectRoots": ["${workspaceFolder}/build"]
          }
        }
      ]
    }
  }
}
```

The build command is optional and runs directly, without a shell, before the executable in the same logical output session. A failed or timed-out build prevents the executable from starting. Rerun reloads the latest saved target and repeats both stages.

For GCC coverage, compile and link with `--coverage` (or equivalent `-fprofile-arcs -ftest-coverage` flags). Echo removes only `.gcda` counters beneath the configured object roots before a run, then reads `gcov` JSON reports after a passing suite.

For LLVM source-based coverage, compile and link with `-fprofile-instr-generate -fcoverage-mapping`. Set `provider` to `llvm`; the executable is always the primary coverage object, and `coverage.objects` may list additional binaries or shared libraries. Echo supplies a run-unique `LLVM_PROFILE_FILE`, merges all raw profiles with `llvm-profdata`, and exports coverage with `llvm-cov`.

Coverage tools are resolved from `PATH` in the same host or Linux sandbox as the build. Echo displays covered lines in green, partially covered lines in amber, and uncovered lines in red. Reports are restricted to `.c` and `.h` files that are inside both a registered workspace root and one of the target's source roots. Coverage is cleared when a new run starts, target configuration changes, or a source/header edit makes the result stale.

Paths and argument values support `${workspaceFolder}`, `${workspaceFolder:name}`, `${pathSeparator}`, and `${env:NAME}`. Entry files, source roots, object roots, executables, and working directories must remain inside a registered workspace root. Dynamic selected-file, input, and command variables are rejected.

C debugging uses the first enabled `lldb` adapter profile (normally CodeLLDB), with the target's build as its pre-launch step. Echo opens Debug Settings with an actionable error when no such profile is enabled.
