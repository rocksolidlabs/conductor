# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 the Conductor Authors and RockSolid Labs, Inc.
"""The preflight runner: its receipts, steps, display and Windows handling."""

import contextlib
import io
import json
from pathlib import Path
import re
import tempfile
import unittest
import unittest.mock

import preflight as p

HEAD = "a" * 40


def receipt(machine, head=HEAD, ok=True, partial=False, steps=None, version=p.RECEIPT_VERSION):
    if steps is None:
        steps = [{"name": "x", "command": "x", "cwd": None, "exit": 0, "seconds": 1.0, "log": ""}]
    return {"version": version, "head": head, "machine": machine, "ok": ok, "partial": partial, "steps": steps}


class Receipts(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.d = Path(self.dir.name)
        self.addCleanup(self.dir.cleanup)

    def write(self, r):
        p.receipt_path(self.d, r["head"], r["machine"]).write_text(json.dumps(r))

    def test_a_passing_receipt_is_a_complete_run_of_the_machine_at_the_head(self):
        self.write(receipt("linux"))
        self.assertTrue(p.receipt_passes(p.load_receipt(self.d, HEAD, "linux"), HEAD, "linux"))

    def test_a_partial_run_an_old_head_another_version_or_a_failed_step_does_not_pass(self):
        self.write(receipt("linux", partial=True))
        self.write(receipt("mac", head="b" * 40))
        self.write(receipt("windows", version=0))
        for m in ("linux", "mac", "windows"):
            self.assertFalse(p.receipt_passes(p.load_receipt(self.d, HEAD, m), HEAD, m), m)
        self.write(receipt("linux", steps=[]))
        self.assertFalse(p.receipt_passes(p.load_receipt(self.d, HEAD, "linux"), HEAD, "linux"))
        self.write(receipt("mac", steps=[{"name": "lint", "exit": 0}, {"name": "desktop-test", "exit": 2}]))
        self.assertFalse(p.receipt_passes(p.load_receipt(self.d, HEAD, "mac"), HEAD, "mac"))

    def test_an_unreadable_receipt_does_not_count(self):
        p.receipt_path(self.d, HEAD, "linux").write_text("{not json")
        self.assertIsNone(p.load_receipt(self.d, HEAD, "linux"))
        self.assertFalse(p.receipt_passes(None, HEAD, "linux"))


class Display(unittest.TestCase):
    def test_wslg_x_server_is_used_whatever_the_shell_says(self):
        with tempfile.NamedTemporaryFile() as sock:
            self.assertEqual(p.display_env({"DISPLAY": "172.26.16.42:0.0"}, socket=sock.name), {"DISPLAY": ":0"})
            self.assertEqual(p.display_env({}, socket=sock.name), {"DISPLAY": ":0"})

    def test_an_ordinary_display_is_kept_and_none_is_reported(self):
        missing = "/nonexistent/X0"
        self.assertEqual(p.display_env({"DISPLAY": ":1"}, socket=missing), {})
        self.assertEqual(p.display_env({"WAYLAND_DISPLAY": "wayland-1"}, socket=missing), {})
        self.assertIsNone(p.display_env({}, socket=missing))


class Windows(unittest.TestCase):
    def test_a_step_fails_when_the_command_set_no_exit_code_or_was_not_found(self):
        script = p._ps_script("C:\\r", ["npm", "ci"], {"CI": "1"})
        self.assertIn("exit $LASTEXITCODE", script)
        self.assertIn("$null -eq $LASTEXITCODE", script)
        self.assertIn("$env:CI = '1'", script)
        self.assertIn("Set-Location -LiteralPath 'C:\\r'", script)
        with_path = p._ps_script("C:\\r", ["npm", "ci"], None, ["C:\\Program Files\\nodejs", "c:\\program files\\git\\cmd"])
        self.assertIn("$env:Path = 'C:\\Program Files\\nodejs;c:\\program files\\git\\cmd' + ';' + $env:Path", with_path)
        with tempfile.TemporaryDirectory() as d:
            log = Path(d) / "x.log"
            calls = []

            def fake_powershell(s):
                calls.append(s)
                return 0, "At line:1\n+ & 'npm' 'ci'\n    + CategoryInfo : ObjectNotFound: (npm:String) [], CommandNotFoundException\n"

            with unittest.mock.patch.object(p, "powershell", fake_powershell):
                run = p.windows_runner("C:\\r", {"npm": "C:\\Program Files\\nodejs\\npm.cmd"})
                self.assertEqual(run(["npm", "ci"], "desktop", None, log), 1)
                self.assertIn("'C:\\Program Files\\nodejs\\npm.cmd' 'ci'", calls[-1])
                self.assertIn("$env:Path = 'C:\\Program Files\\nodejs' + ';' + $env:Path", calls[-1])
                run = p.windows_runner("C:\\r", {"npm": None})
                self.assertEqual(run(["npm", "test"], "desktop", None, log), 127)
                self.assertIn("not installed", log.read_text())

    def test_the_tool_is_found_on_the_path_or_where_its_installer_puts_it(self):
        self.assertEqual(p.windows_tool("npm", run=lambda s: (0, "C:\\Program Files\\nodejs\\npm.cmd\n")), "C:\\Program Files\\nodejs\\npm.cmd")
        self.assertIsNone(p.windows_tool("npm", run=lambda s: (0, "\n")))
        self.assertIsNone(p.windows_tool("npm", run=lambda s: (1, "error")))


class Steps(unittest.TestCase):
    def test_every_ci_job_has_a_linux_step(self):
        ci = Path(__file__).resolve().parent.parent / ".github" / "workflows" / "ci.yml"
        text = ci.read_text()
        jobs_block = text[text.index("\njobs:\n") :]
        jobs = set(re.findall(r"^  ([A-Za-z0-9_-]+):\s*$", jobs_block, re.M))
        covered = {s["covers"] for s in p.LINUX_STEPS} | set(p.GATE_JOBS)
        self.assertTrue(jobs, "no jobs parsed from ci.yml")
        self.assertEqual(jobs - covered, set(), f"ci.yml jobs with no preflight step: {jobs - covered}")

    def test_the_dco_check_runs_first_against_origin_main(self):
        # The fetch that makes origin/main GitHub's comes before the steps (Main).
        first = p.LINUX_STEPS[0]
        self.assertEqual((first["name"], first["covers"]), ("dco", "dco"))
        self.assertEqual(first["argv"], ["python3", "scripts/dco_check.py", "--base", "origin/main", "--head", "HEAD"])
        # Not on the Mac or Windows rows: what a commit carries does not depend on the machine.
        for steps in (p.MAC_STEPS, p.WINDOWS_STEPS):
            self.assertFalse(any(s["name"] == "dco" for s in steps))

    def test_the_go_race_tests_run_on_the_whole_module_here_and_on_the_mac(self):
        for steps in (p.LINUX_STEPS, p.MAC_STEPS):
            race = [s for s in steps if s["name"] == "go-test-race"]
            self.assertEqual(race[0]["argv"], ["go", "test", "-race", "-count=1", "-timeout", "60m", "./..."])

    def test_every_entry_point_runs_the_race_tests_with_the_same_bound(self):
        # CI, make test and the documented command pass the bound preflight
        # passes: go test's default is less than internal/api takes on a
        # loaded machine (docs/checks.md), so one left at it fails there.
        root = Path(__file__).resolve().parent.parent
        want = " ".join(p.GO_TEST_RACE)
        for path in (".github/workflows/ci.yml", "Makefile", "docs/checks.md"):
            lines = [line for line in (root / path).read_text().splitlines() if "go test -race" in line and "./..." in line]
            self.assertTrue(lines, f"{path} runs no race tests of the whole module")
            for line in lines:
                self.assertIn(want, line, f"{path}: {line.strip()}")

    def test_the_desktop_tests_run_on_every_machine(self):
        for steps in (p.LINUX_STEPS, p.MAC_STEPS, p.WINDOWS_STEPS):
            self.assertTrue(any(s["name"] == "desktop-test" for s in steps))

    def test_a_run_writes_a_receipt_that_the_gate_reads(self):
        with tempfile.TemporaryDirectory() as d:
            r = p.Run(Path("."), HEAD, "b" * 40, "linux", d, only=None, log=lambda *_: None)
            calls = []

            def runner(argv, cwd, env, logfile):
                calls.append(argv)
                Path(logfile).write_text("ran\n")
                return 0

            self.assertTrue(r.step("one", ["true"], runner))
            self.assertTrue(r.step("two", ["true", "x"], runner, cwd="desktop"))
            written = r.write({"tools": {"go": "go1"}})
            self.assertEqual(calls, [["true"], ["true", "x"]])
            self.assertTrue(p.receipt_passes(p.load_receipt(d, HEAD, "linux"), HEAD, "linux"))
            self.assertEqual(written["steps"][1]["cwd"], "desktop")
            # A partial run, or a failed step, writes a receipt that does not pass.
            r2 = p.Run(Path("."), HEAD, "", "mac", d, only=["one"], log=lambda *_: None)
            r2.step("one", ["true"], runner)
            r2.write()
            self.assertFalse(p.receipt_passes(p.load_receipt(d, HEAD, "mac"), HEAD, "mac"))
            r3 = p.Run(Path("."), HEAD, "", "windows", d, log=lambda *_: None)
            r3.step("one", ["false"], lambda *a: 1)
            r3.write()
            self.assertFalse(p.receipt_passes(p.load_receipt(d, HEAD, "windows"), HEAD, "windows"))


    def test_a_partial_run_checks_the_head_out_on_the_remote_machines(self):
        # --only names checks; getting the revision onto a remote machine is
        # not one: the Mac's and Windows's rows check the head out before the
        # steps named, so a partial run tests the head, never the machine's
        # last checkout (#295). The receipt stays a partial one.
        step = [{"name": "desktop-test", "argv": ["npm", "test"], "cwd": "desktop"}]
        for machine, run, steps in (("mac", p.run_mac, "MAC_STEPS"), ("windows", p.run_windows, "WINDOWS_STEPS")):
            with self.subTest(machine), tempfile.TemporaryDirectory() as d, tempfile.TemporaryDirectory() as repo:
                Path(repo, ".git").mkdir()
                calls = []

                def runner(argv, cwd, env, logfile):
                    calls.append(" ".join(argv))
                    Path(logfile).write_text("ran\n")
                    return 0

                with unittest.mock.patch.multiple(
                    p,
                    push_revision=lambda dest, head: None,
                    ssh_runner=lambda host, repo: runner,
                    windows_repo_paths=lambda: ("C:\\Users\\x\\conductor-preflight", repo),
                    windows_tool=lambda name: "x",
                    windows_runner=lambda winpath, tools=None: runner,
                    **{steps: step},
                ):
                    receipt = run(Path("."), HEAD, "b" * 40, d, ["desktop-test"], lambda *_: None)
                self.assertEqual([s["name"] for s in receipt["steps"]], ["checkout", "desktop-test"], calls)
                self.assertIn("checkout", calls[0])
                self.assertEqual(calls[1], "npm test")
                self.assertTrue(receipt["partial"])
                self.assertFalse(p.receipt_passes(p.load_receipt(d, HEAD, machine), HEAD, machine))


class Main(unittest.TestCase):
    def test_origin_main_is_fetched_before_the_base_is_read_and_a_step_runs(self):
        # The dco step judges origin/main..HEAD: a ref behind GitHub's main
        # would put main's own commits in the range, so the fetch comes first,
        # a partial run included, and a fetch that fails stops the run before
        # any step.
        fetch = ("fetch", "--quiet", "origin", "main")
        with tempfile.TemporaryDirectory() as d:
            answers = {
                ("rev-parse", "--show-toplevel"): d,
                ("status", "--porcelain", "--untracked-files=no"): "",
                fetch: "",
                ("rev-parse", "HEAD"): HEAD,
                ("rev-parse", "origin/main"): "b" * 40,
                ("rev-parse", "--path-format=absolute", "--git-common-dir"): d,
            }
            calls, runs = [], []

            def fake_git(*args, cwd=None):
                calls.append(args)
                return answers[args]

            def failing_git(*args, cwd=None):
                if args == fetch:
                    raise p.Error("fatal: unable to access 'https://github.com/x/y/': Could not resolve host")
                return fake_git(*args, cwd=cwd)

            def fake_linux(root, head, base, directory, only, say):
                runs.append((head, base, only))
                return receipt("linux", ok=False, partial=True)

            out, err = io.StringIO(), io.StringIO()
            with unittest.mock.patch.object(p, "git", fake_git), unittest.mock.patch.dict(p.RUNNERS, {"linux": fake_linux}):
                with contextlib.redirect_stdout(out):
                    code = p.main(["--only", "dco"])
            self.assertEqual(code, 1, out.getvalue())  # a partial run never passes
            self.assertIn(fetch, calls)
            self.assertLess(calls.index(fetch), calls.index(("rev-parse", "origin/main")))
            self.assertEqual(runs, [(HEAD, "b" * 40, ["dco"])])
            runs.clear()
            with unittest.mock.patch.object(p, "git", failing_git), unittest.mock.patch.dict(p.RUNNERS, {"linux": fake_linux}):
                with contextlib.redirect_stderr(err):
                    code = p.main([])
            self.assertEqual(code, 2)
            self.assertEqual(runs, [])
            self.assertIn("git fetch origin main failed: fatal: unable to access", err.getvalue())


if __name__ == "__main__":
    unittest.main()
