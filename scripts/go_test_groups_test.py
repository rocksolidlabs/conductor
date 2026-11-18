# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 the Conductor Authors and RockSolid Labs, Inc.
"""The Go race tests' groups: every test in exactly one, and the check that says so."""

import contextlib
import io
from pathlib import Path
import re
import unittest
import unittest.mock

import go_test_groups as g

CI = Path(__file__).resolve().parent.parent / ".github" / "workflows" / "ci.yml"
MOD = "example.com/m"
API = [f"Test{w}" for w in ("A", "AB", "Accept", "Bounds", "Close", "Drain", "Echo", "Folder", "Gate", "Hold", "Idle", "Join")]
API += ["FuzzFrame", "ExampleServer"]


class FakeGo:
    """go list and go test -list over a made-up module; -list matches as Go's
    matchString does, unanchored (re.search)."""

    def __init__(self, extra_packages=(), api=API):
        self.pkgs = {
            "./internal/api": [f"{MOD}/internal/api"],
            "./internal/crew/...": [f"{MOD}/internal/crew", f"{MOD}/internal/crew/ledger"],
            "./internal/dispatch/...": [f"{MOD}/internal/dispatch"],
        }
        self.all = [f"{MOD}/cmd/x", *(p for ps in self.pkgs.values() for p in ps), f"{MOD}/internal/session", *extra_packages]
        self.pkgs["./..."] = self.all
        self.names = {"./internal/api": list(api)}
        self.lists = []

    def packages(self, patterns):
        out = []
        for pat in patterns:
            out += self.pkgs.get(pat, [])
        return out

    def tests(self, pkg, expression):
        self.lists.append(expression)
        listing = [n for n in self.names[pkg] if re.search(expression, n)]
        listing += [b for b in ("BenchmarkWrite",) if re.search(expression, b)]
        return g.runnable("\n".join(listing) + f"\nok  \t{MOD}/internal/api\t0.1s\n")


class Dealing(unittest.TestCase):
    def test_every_name_is_in_exactly_one_group(self):
        names = [f"Test{i}" for i in range(500)]
        groups = g.deal(names, 5)
        flat = [n for grp in groups for n in grp]
        self.assertEqual(sorted(flat), sorted(names))
        self.assertTrue(all(groups), "a group is empty")

    def test_a_test_added_or_removed_moves_no_other_test(self):
        names = [f"Test{i}" for i in range(300)]
        before = {n: k for k, grp in enumerate(g.deal(names, 5)) for n in grp}
        for changed in (names + ["TestAddedInTheMiddle", "TestAAAFirst"], names[1:]):
            after = {n: k for k, grp in enumerate(g.deal(changed, 5)) for n in grp}
            moved = [n for n in after if n in before and after[n] != before[n]]
            self.assertEqual(moved, [])

    def test_a_group_is_the_same_on_every_machine(self):
        # SHA-256, never Python's hash(), which a process salts.
        self.assertEqual(g.bucket("TestA", 5), int.from_bytes(__import__("hashlib").sha256(b"TestA").digest()[:8], "big") % 5)
        self.assertEqual([g.bucket(n, 5) for n in ("TestA", "TestAB")], [g.bucket(n, 5) for n in ("TestA", "TestAB")])

    def test_the_expression_selects_exactly_its_names(self):
        e = g.run_expression(["TestA", "TestClose"])
        self.assertEqual([n for n in ("TestA", "TestAB", "TestClose", "TestCloseAll", "XTestA") if re.search(e, n)], ["TestA", "TestClose"])
        self.assertNotIn("/", e)  # a slash would make -run match subtests by level
        with self.assertRaises(g.Error):
            g.run_expression([])
        with self.assertRaises(g.Error):
            g.run_expression(["Test(A"])

    def test_the_listing_keeps_what_run_selects(self):
        out = "TestA\nFuzzB\nExampleC\nBenchmarkD\nok  \tx/y\t0.2s\n"
        self.assertEqual(g.runnable(out), ["TestA", "FuzzB", "ExampleC"])
        for junk in ("panic: boom\n", "Helper\n"):
            with self.assertRaises(g.Error):
                g.runnable(junk)


class Commands(unittest.TestCase):
    def test_each_group_runs_with_the_whole_modules_flags(self):
        go = FakeGo()
        api = [g.command(f"api-{k}", go) for k in range(1, 6)]
        for argv in api:
            self.assertEqual(argv[:6], ["go", "test", "-race", "-count=1", "-timeout", "60m"])
            self.assertEqual((argv[6], argv[-1]), ("-run", "./internal/api"))
        self.assertEqual(g.command("crew", go)[6:], ["./internal/crew/..."])
        self.assertEqual(g.command("dispatch", go)[6:], ["./internal/dispatch/..."])
        self.assertEqual(g.command("rest", go)[6:], [f"{MOD}/cmd/x", f"{MOD}/internal/session"])
        for bad in ("api-0", "api-6", "ap-1", "web"):
            with self.assertRaises(g.Error):
                g.command(bad, go)

    def test_the_groups_are_the_matrix_of_ci(self):
        self.assertEqual(g.workflow_groups(CI.read_text()), g.groups())
        self.assertEqual(g.groups(), ["api-1", "api-2", "api-3", "api-4", "api-5", "crew", "dispatch", "rest"])

    def test_ci_runs_every_group_through_this_script_and_checks_them(self):
        text = CI.read_text()
        job = re.search(r"^  go-race:\n(.*?)(?=^  [A-Za-z0-9_-]+:\s*$)", text, re.M | re.S).group(1)
        self.assertIn("fail-fast: false", job)
        self.assertIn("GROUP: ${{ matrix.group }}", job)
        self.assertIn('run: python3 scripts/go_test_groups.py run "$GROUP"', job)
        self.assertEqual([r for r in re.findall(r"^\s+(?:- )?run: (.*)$", job, re.M) if "go test" in r], [])
        go_job = re.search(r"^  go:\n(.*?)(?=^  [A-Za-z0-9_-]+:\s*$)", text, re.M | re.S).group(1)
        self.assertIn("run: python3 scripts/go_test_groups.py check", go_job)

    def test_run_shows_the_groups_tests_then_hands_over_to_go_test(self):
        ran = []
        out = io.StringIO()
        with (
            unittest.mock.patch.object(g, "Go", lambda root: FakeGo()),
            unittest.mock.patch.object(g.os, "execvp", lambda f, argv: ran.append(argv)),
            unittest.mock.patch.object(g.os, "chdir", lambda d: None),
            unittest.mock.patch.dict(g.os.environ, {"GITHUB_ACTIONS": "true"}),
            contextlib.redirect_stdout(out),
        ):
            g.main(["run", "api-2"])
        dealt = g.deal(API, 5)[1]
        self.assertEqual(ran, [[*g.RUN, "-run", g.run_expression(dealt), "./internal/api"]])
        lines = out.getvalue().splitlines()
        self.assertEqual(lines[1:], [f"::group::the {len(dealt)} tests of api-2", *dealt, "::endgroup::"])


class Check(unittest.TestCase):
    def test_whole_groups_pass_and_go_answers_each_groups_expression(self):
        go = FakeGo()
        self.assertEqual(g.problems(g.groups(), go), [])
        self.assertEqual(go.lists, [".*", *(g.run_expression(d) for d in g.deal(API, 5))])  # the listing, then each group's -run

    def test_a_test_dropped_from_every_group_fails(self):
        real = g.deal

        def drop(names, n):
            return [[x for x in grp if x != "TestEcho"] for grp in real(names, n)]

        with unittest.mock.patch.object(g, "deal", drop):
            bad = g.problems(g.groups(), FakeGo())
        self.assertTrue(any("TestEcho runs in 0 groups: none" in b for b in bad), bad)

    def test_a_test_in_two_groups_fails(self):
        real = g.deal

        def twice(names, n):
            out = real(names, n)
            k = next(i for i, grp in enumerate(out) if "TestEcho" in grp)
            out[(k + 1) % n] = sorted(out[(k + 1) % n] + ["TestEcho"])
            return out

        with unittest.mock.patch.object(g, "deal", twice):
            bad = g.problems(g.groups(), FakeGo())
        self.assertTrue(any("TestEcho runs in 2 groups" in b for b in bad), bad)

    def test_an_expression_go_reads_wider_than_its_names_fails(self):
        # Unanchored, TestA's group would also run TestAccept, dealt to another.
        self.assertNotEqual(g.bucket("TestA", 5), g.bucket("TestAccept", 5))
        with unittest.mock.patch.object(g, "run_expression", lambda names: "(" + "|".join(names) + ")"):
            bad = g.problems(g.groups(), FakeGo())
        self.assertTrue(any("TestAccept runs in 2 groups" in b for b in bad), bad)
        self.assertTrue(any("selects" in b and "extra ['TestAccept']" in b for b in bad), bad)

    def test_a_matrix_that_misses_doubles_or_invents_a_group_fails(self):
        go = FakeGo()
        self.assertEqual(g.problems([x for x in g.groups() if x != "api-3"], go), ["the matrix does not list api-3: its tests run in no job"])
        self.assertEqual(g.problems(g.groups() + ["crew"], go), ["the matrix lists crew 2 times"])
        self.assertEqual(g.problems(g.groups() + ["api-6"], go), ["the matrix lists api-6, which is no group"])

    def test_a_package_in_two_groups_or_in_one_go_does_not_list_fails(self):
        go = FakeGo()
        go.pkgs["./internal/dispatch/..."] = [f"{MOD}/internal/dispatch", f"{MOD}/internal/crew/ledger"]
        bad = g.problems(g.groups(), go)
        self.assertIn(f"{MOD}/internal/crew/ledger runs in 2 groups: crew, dispatch", bad)
        go = FakeGo()
        go.pkgs["./internal/dispatch/..."] = []
        self.assertIn("dispatch's packages (./internal/dispatch/...) are none", g.problems(g.groups(), go))

    def test_a_new_package_lands_in_rest(self):
        go = FakeGo(extra_packages=[f"{MOD}/internal/new"])
        self.assertEqual(g.problems(g.groups(), go), [])
        self.assertIn(f"{MOD}/internal/new", g.command("rest", go))

    def test_the_workflow_parser_reads_only_the_go_race_matrix(self):
        text = "jobs:\n  e2e:\n    strategy:\n      matrix:\n        group: [x]\n  go-race:\n    strategy:\n      fail-fast: false\n      matrix:\n        group: [api-1, rest]\n  next:\n    x: 1\n"
        self.assertEqual(g.workflow_groups(text), ["api-1", "rest"])
        with self.assertRaises(g.Error):
            g.workflow_groups("jobs:\n  go:\n    x: 1\n")


if __name__ == "__main__":
    unittest.main()
