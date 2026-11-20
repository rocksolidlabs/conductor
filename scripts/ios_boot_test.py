"""Tests for ios_boot.py: a step tried again until it passes, within its count, each try under its bound."""
from __future__ import annotations

from pathlib import Path
import subprocess
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import ios_boot  # noqa: E402


class Done:
    def __init__(self, code: int) -> None:
        self.returncode = code


class Tries(unittest.TestCase):
    def run_with(self, outcomes: list):
        calls, pauses, said = [], [], []

        def run(argv, timeout):
            calls.append(timeout)
            o = outcomes.pop(0)
            if o == "hang":
                raise subprocess.TimeoutExpired(argv, timeout)
            return Done(o)

        ok = ios_boot.tries(["xcodebuild", "-resolvePackageDependencies"], 30, count=3, pause=7, run=run, sleep=pauses.append, say=said.append)
        return ok, calls, pauses, said

    def test_a_step_that_fails_then_passes_is_tried_again(self) -> None:
        ok, calls, pauses, said = self.run_with([1, "hang", 0])
        self.assertTrue(ok)
        self.assertEqual(calls, [30, 30, 30])
        self.assertEqual(pauses, [7, 7])
        self.assertIn("ran past 30 s", said[1])

    def test_a_step_that_never_passes_fails_after_its_count(self) -> None:
        ok, calls, pauses, _ = self.run_with([1, 1, 1])
        self.assertFalse(ok)
        self.assertEqual(len(calls), 3)
        self.assertEqual(pauses, [7, 7])

    def test_a_step_that_passes_at_once_is_run_once(self) -> None:
        ok, calls, pauses, _ = self.run_with([0])
        self.assertTrue(ok)
        self.assertEqual((len(calls), pauses), (1, []))

    def test_the_resolve_names_the_apps_project_and_its_build_folder(self) -> None:
        argv = ios_boot.resolve_argv()
        self.assertEqual(argv[:2], ["xcodebuild", "-resolvePackageDependencies"])
        self.assertIn(str(ios_boot.ROOT / "mobile/ios/App/App.xcodeproj"), argv)
        self.assertIn(str(ios_boot.ROOT / "mobile/ios/App/build"), argv)


if __name__ == "__main__":
    unittest.main()
