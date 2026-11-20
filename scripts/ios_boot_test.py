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


OTOOL = """Contents of (__DATA,__objc_classlist) section
0000000100010000 0x100020000 _OBJC_CLASS_$__TtC3App18ShellViewController
    isa 0x100020100 _OBJC_METACLASS_$__TtC3App18ShellViewController
    data 0x100018000
        name 0x10000a000 _TtC3App18ShellViewController
0000000100010008 0x100020200 _OBJC_CLASS_$_ConductorKeySigningPolicy
    data 0x100018100
        name 0x10000a040 ConductorKeySigningPolicy
"""


class Policy(unittest.TestCase):
    def test_the_class_names_are_read_from_the_class_list(self) -> None:
        self.assertEqual(ios_boot.classes(OTOOL), {"_TtC3App18ShellViewController", "ConductorKeySigningPolicy"})
        self.assertNotIn("ConductorKeySigningPolicy", ios_boot.classes(OTOOL.split("0000000100010008")[0]))
        self.assertEqual(ios_boot.classes("0000000100020200 S _OBJC_CLASS_$_ConductorKeySigningPolicy\n0000000100020300 S _OBJC_METACLASS_$_ConductorKeySigningPolicy\n"), {"ConductorKeySigningPolicy"})

    def test_the_sample_is_built_in_and_removed_whatever_the_build_did(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            saved = (ios_boot.DERIVED, ios_boot.POLICY_DERIVED, ios_boot.PLACE)
            ios_boot.DERIVED, ios_boot.POLICY_DERIVED, ios_boot.PLACE = root / "build", root / "build-policy", root / "Packaging"
            self.addCleanup(lambda: setattr(ios_boot, "DERIVED", saved[0]) or setattr(ios_boot, "POLICY_DERIVED", saved[1]) or setattr(ios_boot, "PLACE", saved[2]))
            default = ios_boot.executable(ios_boot.DERIVED, "Debug")
            default.parent.mkdir(parents=True)
            default.write_text("")
            seen = {}

            def run(argv, timeout, **kw):
                if argv[0] == "xcodebuild":
                    seen["placed"] = sorted(p.name for p in ios_boot.PLACE.iterdir())
                    seen["argv"] = argv
                    return Done(seen.get("build", 0))
                if argv[1] == "nm":
                    return type("R", (), {"stdout": ""})()
                holds = argv[-1] != str(default) and seen.get("holds", True)
                return type("R", (), {"stdout": OTOOL if holds else OTOOL.split("0000000100010008")[0]})()

            said = []
            self.assertEqual(ios_boot.policy(run=run, say=said.append), 0, said)
            self.assertEqual(seen["placed"], ["SampleSigningPolicy.swift"])
            self.assertIn("DEAD_CODE_STRIPPING=YES", seen["argv"])
            self.assertIn("Release", seen["argv"])
            self.assertFalse(ios_boot.PLACE.exists())
            # A build that fails, or one whose executable lacks the class, fails the check, and the copy is gone either way.
            seen["build"] = 65
            self.assertEqual(ios_boot.policy(run=run, say=said.append), 1)
            self.assertFalse(ios_boot.PLACE.exists())
            seen["build"], seen["holds"] = 0, False
            self.assertEqual(ios_boot.policy(run=run, say=said.append), 1)
            self.assertIn("does not hold", said[-1])

    def test_a_default_build_with_a_policy_fails_the_check(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            saved = ios_boot.DERIVED
            ios_boot.DERIVED = Path(d)
            self.addCleanup(setattr, ios_boot, "DERIVED", saved)
            exe = ios_boot.executable(ios_boot.DERIVED, "Debug")
            exe.parent.mkdir(parents=True)
            exe.write_text("")
            said = []
            run = lambda argv, timeout, **kw: type("R", (), {"stdout": OTOOL})()
            self.assertEqual(ios_boot.policy(run=run, say=said.append), 1)
            self.assertIn("the default build holds", said[-1])
LISTING = {
    "devices": {
        "com.apple.CoreSimulator.SimRuntime.iOS-17-5": [{"udid": "OLD", "name": "iPhone 15", "state": "Shutdown", "isAvailable": True}],
        "com.apple.CoreSimulator.SimRuntime.iOS-18-2": [
            {"udid": "IPAD", "name": "iPad Pro 13-inch (M4)", "state": "Shutdown", "isAvailable": True},
            {"udid": "GONE", "name": "iPhone 16 Pro Max", "state": "Shutdown", "isAvailable": False},
            {"udid": "NEW", "name": "iPhone 16", "state": "Shutdown", "isAvailable": True},
        ],
        "com.apple.CoreSimulator.SimRuntime.watchOS-11-2": [{"udid": "WATCH", "name": "Apple Watch", "state": "Booted", "isAvailable": True}],
    }
}


class Simulator(unittest.TestCase):
    def test_the_newest_ios_iphone_available_is_picked_and_a_booted_one_first(self) -> None:
        self.assertEqual(ios_boot.pick(LISTING), "NEW")
        booted = {"devices": {**LISTING["devices"], "com.apple.CoreSimulator.SimRuntime.iOS-18-2": LISTING["devices"]["com.apple.CoreSimulator.SimRuntime.iOS-18-2"] + [{"udid": "UP", "name": "iPhone 16", "state": "Booted", "isAvailable": True}]}}
        self.assertEqual(ios_boot.pick(booted), "UP")
        self.assertIsNone(ios_boot.pick({"devices": {"com.apple.CoreSimulator.SimRuntime.iOS-18-2": [LISTING["devices"]["com.apple.CoreSimulator.SimRuntime.iOS-18-2"][0]]}}))
        self.assertEqual(ios_boot.version("com.apple.CoreSimulator.SimRuntime.iOS-18-2"), (18, 2))
        self.assertEqual(ios_boot.version("com.apple.CoreSimulator.SimRuntime.watchOS-11-2"), ())

    def test_a_simulator_that_does_not_boot_is_shut_down_and_booted_again(self) -> None:
        calls = []
        outcomes = iter(["hang", 0])

        def run(argv, timeout, **kw):
            calls.append(argv[2])
            if argv[2] == "bootstatus":
                o = next(outcomes)
                if o == "hang":
                    raise subprocess.TimeoutExpired(argv, timeout)
                return Done(o)
            return Done(1 if argv[2] == "boot" else 0)

        self.assertTrue(ios_boot.boot("NEW", run=run, sleep=lambda s: None, say=lambda m: None))
        self.assertEqual(calls, ["boot", "bootstatus", "shutdown", "boot", "bootstatus"])

    def test_the_check_runs_only_the_boot_test_unsigned_on_that_simulator(self) -> None:
        argv = ios_boot.test_argv("NEW")
        self.assertEqual(argv[:2], ["xcodebuild", "test"])
        self.assertIn("id=NEW", argv)
        self.assertIn("-only-testing:AppUITests/ShellBootsUnderItsPolicyTests", argv)
        self.assertIn("CODE_SIGNING_ALLOWED=NO", argv)
        self.assertFalse(any("DEVELOPMENT_TEAM" in a for a in argv))


if __name__ == "__main__":
    unittest.main()
