# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 the Conductor Authors and RockSolid Labs, Inc.
"""The Go race tests in groups, one CI job each: what CI's `go-race` job runs.

    python3 scripts/go_test_groups.py groups            # the group names, as the job's matrix lists them
    python3 scripts/go_test_groups.py command <group>   # the go test command of one group
    python3 scripts/go_test_groups.py run <group>       # run it, as CI's job does
    python3 scripts/go_test_groups.py check [--workflow .github/workflows/ci.yml]

`go test -race -count=1 -timeout 60m ./...` is one command for a developer
(`make test`, docs/checks.md). On CI's four-core runner it took about 57
minutes, `internal/api` alone some 52, so CI runs it as several jobs side by
side, each with the same flags and bound:

- `api-1` to `api-5`: the tests of `internal/api`, dealt into five groups by
  name. A test's group is the SHA-256 of its name modulo five, so a test
  added or removed moves no other test: a group's members change only by
  their own names. (A round-robin over the sorted names would move every
  test after the new one.) Each group runs `-run '^(A|B|...)$'`: top-level
  tests, each subtest with its parent; the package's TestMain runs in each
  group, as it does for any `-run`.
- `crew`: `internal/crew` and everything under it (its ledger).
- `dispatch`: `internal/dispatch` and everything under it.
- `rest`: every other package of `go list ./...`, in one command as before.

A group's tests are what `-run` selects: tests, fuzz targets (their seed
corpus) and examples. Benchmarks are listed by `go test -list` but run only
under `-bench`, which no check passes, here as before.

`check` fails (exit 1, a line per problem) unless the workflow's matrix lists
every group once and nothing else, every package of `go list ./...` is in
exactly one group, and every test of a split package is selected by exactly
one group's `-run`, as `go test -list` with that expression answers: Go's own
matching, not this script's. Exit 2 when go fails.
"""

import argparse
import hashlib
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys

# The flags of every group's run, the bound included: the same as the whole
# module's command (preflight.py's GO_TEST_RACE, which preflight_test.py
# holds this to). The listing builds with -race too, so that it lists what
# the run builds and shares its compilation.
RUN = ["go", "test", "-race", "-count=1", "-timeout", "60m"]
LIST = ["go", "test", "-race", "-list"]

# A package split by test, and into how many groups. internal/api's tests
# ran 3,129 s in main's run of 2026-11-18 (3,100 to 3,400 in the runs
# before): five groups of about 630 s each, eleven minutes, with the build.
SPLIT = {"api": ("./internal/api", 5)}
# Packages in a job of their own: internal/crew 496 s with its ledger 50,
# internal/dispatch 467 s (the same run).
WHOLE = {"crew": ("./internal/crew/...",), "dispatch": ("./internal/dispatch/...",)}
REST = "rest"

# What -run selects. A name is listed by go test -list only when go took it
# for one of these kinds.
RUNNABLE = ("Test", "Fuzz", "Example")
BENCHMARK = "Benchmark"


class Error(Exception):
    pass


def groups():
    """Every group's name, in the matrix's order."""
    names = []
    for prefix, (_, n) in SPLIT.items():
        names += [f"{prefix}-{k}" for k in range(1, n + 1)]
    return names + list(WHOLE) + [REST]


def bucket(name, n):
    """The 0-based group of a test name among n: stable across runs and machines."""
    return int.from_bytes(hashlib.sha256(name.encode()).digest()[:8], "big") % n


def deal(names, n):
    """The names dealt into n groups by bucket, each sorted."""
    out = [[] for _ in range(n)]
    for name in sorted(set(names)):
        out[bucket(name, n)].append(name)
    return out


def run_expression(names):
    """A -run expression that selects exactly these top-level names."""
    if not names:
        raise Error("a group with no tests: its -run would select nothing")
    for name in names:
        if not name.isidentifier():
            raise Error(f"{name!r} is not a Go identifier")
    return "^(" + "|".join(names) + ")$"


def runnable(listing):
    """The names -run selects, from go test -list's output; benchmarks left out."""
    names = []
    for line in listing.splitlines():
        line = line.strip()
        if not line or re.match(r"^(ok|\?)\s", line):
            continue
        if not line.isidentifier():
            raise Error(f"go test -list printed a line that is not a name: {line!r}")
        if line.startswith(BENCHMARK):
            continue
        if not line.startswith(RUNNABLE):
            raise Error(f"go test -list printed {line!r}, which is no test, fuzz target, example or benchmark")
        names.append(line)
    return names


def split_of(group):
    """(package, index, n) for a group of a split package, else None."""
    m = re.fullmatch(r"([a-z]+)-([0-9]+)", group)
    if not m or m.group(1) not in SPLIT:
        return None
    pkg, n = SPLIT[m.group(1)]
    k = int(m.group(2))
    if not 1 <= k <= n:
        return None
    return pkg, k - 1, n


class Go:
    """The go commands the groups are made from; a test passes a fake."""

    def __init__(self, root):
        self.root = root

    def _out(self, argv):
        p = subprocess.run(argv, cwd=self.root, capture_output=True, text=True)
        if p.returncode:
            raise Error(f"{' '.join(argv[:4])} ... failed ({p.returncode}): {(p.stderr or p.stdout).strip()[-2000:]}")
        return p.stdout

    def packages(self, patterns):
        return self._out(["go", "list", *patterns]).split()

    def tests(self, pkg, expression):
        return runnable(self._out([*LIST, expression, pkg]))


def rest_packages(go):
    """Every package of the module that no other group runs."""
    taken = set(go.packages([pkg for pkg, _ in SPLIT.values()]))
    for patterns in WHOLE.values():
        taken |= set(go.packages(list(patterns)))
    return [p for p in go.packages(["./..."]) if p not in taken]


def command(group, go):
    """The go test argv of one group."""
    split = split_of(group)
    if split:
        pkg, k, n = split
        return [*RUN, "-run", run_expression(deal(go.tests(pkg, ".*"), n)[k]), pkg]
    if group in WHOLE:
        return [*RUN, *WHOLE[group]]
    if group == REST:
        return [*RUN, *rest_packages(go)]
    raise Error(f"no group {group!r}: the groups are {', '.join(groups())}")


def workflow_groups(text):
    """The group names of ci.yml's go-race matrix, as written."""
    job = re.search(r"^  go-race:\n(.*?)(?=^  [A-Za-z0-9_-]+:\s*$|\Z)", text, re.M | re.S)
    if not job:
        raise Error("the workflow has no go-race job")
    m = re.search(r"^      matrix:\n        group: \[([^\]]*)\]\s*$", job.group(1), re.M)
    if not m:
        raise Error("the go-race job has no `group: [...]` matrix")
    return [g.strip() for g in m.group(1).split(",") if g.strip()]


def problems(matrix, go):
    """Every way the groups miss a test or run one twice, as lines; none when whole."""
    out = []
    want = groups()
    for g in sorted({g for g in matrix if matrix.count(g) > 1}):
        out.append(f"the matrix lists {g} {matrix.count(g)} times")
    for g in want:
        if g not in matrix:
            out.append(f"the matrix does not list {g}: its tests run in no job")
    for g in matrix:
        if g not in want:
            out.append(f"the matrix lists {g}, which is no group")

    # Packages: each in exactly one group (a split package counts once, its
    # tests are judged below).
    owners = {}
    for name, (pkg, _) in SPLIT.items():
        for p in go.packages([pkg]):
            owners.setdefault(p, []).append(name)
    for name, patterns in WHOLE.items():
        found = go.packages(list(patterns))
        if not found:
            out.append(f"{name}'s packages ({' '.join(patterns)}) are none")
        for p in found:
            owners.setdefault(p, []).append(name)
    for p in rest_packages(go):
        owners.setdefault(p, []).append(REST)
    every = go.packages(["./..."])
    for p in every:
        if len(owners.get(p, [])) != 1:
            out.append(f"{p} runs in {len(owners.get(p, []))} groups: {', '.join(owners.get(p, [])) or 'none'}")
    for p in sorted(set(owners) - set(every)):
        out.append(f"{p} is in a group but not in go list ./...")

    # Tests of a split package: each selected by exactly one group's -run,
    # as go itself matches the expression.
    for name, (pkg, n) in SPLIT.items():
        every_test = go.tests(pkg, ".*")
        if len(set(every_test)) != len(every_test):
            out.append(f"{pkg}: go test -list names a test twice")
        seen = {}
        for k, dealt in enumerate(deal(every_test, n)):
            g = f"{name}-{k + 1}"
            try:
                selected = go.tests(pkg, run_expression(dealt))
            except Error as e:
                out.append(f"{g}: {e}")
                continue
            if sorted(selected) != dealt:
                extra = sorted(set(selected) - set(dealt))
                missing = sorted(set(dealt) - set(selected))
                out.append(f"{g}: its -run selects {len(selected)} tests, {len(dealt)} dealt to it (extra {extra[:5]}, missing {missing[:5]})")
            for t in selected:
                seen.setdefault(t, []).append(g)
        for t in every_test:
            if len(seen.get(t, [])) != 1:
                out.append(f"{pkg}: {t} runs in {len(seen.get(t, []))} groups: {', '.join(seen.get(t, [])) or 'none'}")
        for t in sorted(set(seen) - set(every_test)):
            out.append(f"{pkg}: {t} is selected but not listed")
    return out


def main(argv=None):
    root = Path(__file__).resolve().parent.parent
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = ap.add_subparsers(dest="what", required=True)
    sub.add_parser("groups")
    for what in ("command", "run"):
        sub.add_parser(what).add_argument("group")
    c = sub.add_parser("check")
    c.add_argument("--workflow", default=str(root / ".github" / "workflows" / "ci.yml"))
    a = ap.parse_args(argv)
    go = Go(root)
    try:
        if a.what == "groups":
            print("\n".join(groups()))
            return 0
        if a.what == "check":
            bad = problems(workflow_groups(Path(a.workflow).read_text()), go)
            for line in bad:
                print(line)
            if bad:
                print(f"{len(bad)} problem(s): a test would run in no job or in two (scripts/go_test_groups.py)")
                return 1
            print(f"every package and every test runs in exactly one of {len(groups())} groups")
            return 0
        cmd = command(a.group, go)
    except Error as e:
        print(f"go_test_groups: {e}", file=sys.stderr)
        return 2
    if a.what == "command":
        print(shlex.join(cmd))
        return 0
    if split_of(a.group):
        # The expression is long: the log shows its names, one a line, folded on CI.
        names = cmd[-2][2:-2].split("|")
        print(shlex.join([*cmd[:-2], f"-run '^(<the {len(names)} tests below>)$'", cmd[-1]]))
        fold = os.environ.get("GITHUB_ACTIONS") == "true"
        print(f"::group::the {len(names)} tests of {a.group}" if fold else f"the {len(names)} tests of {a.group}:")
        print("\n".join(names))
        if fold:
            print("::endgroup::")
    else:
        print(shlex.join(cmd))
    sys.stdout.flush()
    os.chdir(root)
    os.execvp(cmd[0], cmd)


if __name__ == "__main__":
    sys.exit(main())
