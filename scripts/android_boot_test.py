"""Tests for android_boot.py: a step that failed for a fetch is tried again, within its count, each try under its bound; one that failed
for any other reason is not."""
from __future__ import annotations

from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import android_boot  # noqa: E402


class Tries(unittest.TestCase):
    def run_with(self, outcomes: list[tuple[int, str]]):
        calls, pauses, said = [], [], []

        def run(argv, cwd, bound):
            calls.append(bound)
            return outcomes.pop(0)

        ok = android_boot.tries(["./gradlew", "--no-daemon", ":app:assembleDebug"], Path("."), 30, count=3, pause=7, run=run, sleep=pauses.append, say=said.append)
        return ok, calls, pauses, said

    def test_a_fetch_that_fails_then_passes_is_tried_again(self) -> None:
        ok, calls, pauses, said = self.run_with([
            (1, "> Could not GET 'https://dl.google.com/dl/android/maven2/x.pom'. Received status code 504 from server: Gateway Timeout"),
            (124, ""),
            (0, "BUILD SUCCESSFUL"),
        ])
        self.assertTrue(ok)
        self.assertEqual(calls, [30, 30, 30])
        self.assertEqual(pauses, [7, 7])
        self.assertIn("ran past its bound", said[1])

    def test_a_fetch_that_never_passes_fails_after_its_count(self) -> None:
        ok, calls, pauses, _ = self.run_with([(1, "java.net.UnknownHostException: services.gradle.org")] * 3)
        self.assertFalse(ok)
        self.assertEqual(len(calls), 3)
        self.assertEqual(pauses, [7, 7])

    def test_a_build_that_fails_for_its_code_is_not_tried_again(self) -> None:
        ok, calls, pauses, said = self.run_with([(1, "e: file:///x/MainActivity.kt:3:5 Unresolved reference 'x'.\nBUILD FAILED")])
        self.assertFalse(ok)
        self.assertEqual(calls, [30])
        self.assertEqual(pauses, [])
        self.assertIn("not a fetch's", said[-1])

    def test_each_kind_of_fetch_failure_is_known(self) -> None:
        for line in (
            "Could not resolve all files for configuration ':app:debugRuntimeClasspath'.",
            "Could not download kotlin-stdlib-2.2.20.jar",
            "Could not install Gradle distribution from 'https://services.gradle.org/distributions/gradle-8.14.3-all.zip'.",
            "java.net.SocketException: Connection reset",
            "Read timed out",
            "Error: Failed to install the following SDK packages",
        ):
            self.assertTrue(android_boot.FETCH.search(line), line)
        self.assertIsNone(android_boot.FETCH.search("> Task :app:testDebugUnitTest FAILED\n3 tests completed, 1 failed"))

    def test_the_device_logs_telling_lines_are_known(self) -> None:
        for line in (
            "11-22 09:17:10.249  4708  4739 I ConductorBootCheck: components: x",
            "11-22 09:17:10.249  4708  4708 W ConductorShell: the shell loads nothing: x",
            "11-22 09:17:10.249  4708  4708 E AndroidRuntime: FATAL EXCEPTION: main",
            "11-22 09:17:10.249  4708  4708 E AndroidRuntime: Process: dev.conductor.mobile, PID: 4708",
            "11-22 09:17:10.249   512   530 E ActivityManager: ANR in dev.conductor.mobile",
        ):
            self.assertTrue(android_boot.TELLING.search(line), line)
        self.assertIsNone(android_boot.TELLING.search("11-22 09:17:10.249   512   530 I chatty: uid=1000 expire 3 lines"))

    def test_a_run_past_its_bound_is_stopped(self) -> None:
        code, _ = android_boot.run_streamed([sys.executable, "-c", "import time; print('started', flush=True); time.sleep(60)"], Path(tempfile.gettempdir()), 2)
        self.assertEqual(code, 124)
        code, out = android_boot.run_streamed([sys.executable, "-c", "print('done')"], Path(tempfile.gettempdir()), 30)
        self.assertEqual((code, out.strip()), (0, "done"))


if __name__ == "__main__":
    unittest.main()
