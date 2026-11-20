#!/usr/bin/env python3
"""The phone shell's iOS app on a Mac with Xcode: its Swift packages resolved, tried again when a download fails.

    python3 scripts/ios_boot.py resolve   # xcodebuild -resolvePackageDependencies, up to three tries a minute apart
    python3 scripts/ios_boot.py policy    # a build with the sample signing policy in the packaging's place: its class in the app

The app's project fetches the native runtime's Swift package and its two
binary frameworks from GitHub; a check's own setup is part of the check
(AGENTS.md), so a failed or stalled fetch is tried again, each try under a
bound of its own, before the build. The policy check copies the sample
policy (mobile/ios/fixtures) into the packaging's place for one Release build,
dead code stripped, and reads the built executable's Objective-C classes for
the key's fixed name; the default build (`make mobile-ios`'s) must hold
none. The copy is removed whatever the build did. Only Python's standard
library.
"""
from __future__ import annotations

import argparse
from pathlib import Path
import re
import shutil
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
PROJECT = ROOT / "mobile/ios/App/App.xcodeproj"
DERIVED = ROOT / "mobile/ios/App/build"
TRIES = 3
PAUSE = 60
RESOLVE_BOUND = 300
BUILD_BOUND = 1500
POLICY_CLASS = "ConductorKeySigningPolicy"
PLACE = ROOT / "mobile/ios/ConductorNative/Sources/ConductorPackaging/Packaging"
SAMPLE = ROOT / "mobile/ios/fixtures/SampleSigningPolicy.swift"
POLICY_DERIVED = ROOT / "mobile/ios/App/build-policy"


def tries(argv: list[str], bound: int, count: int = TRIES, pause: int = PAUSE, run=subprocess.run, sleep=time.sleep, say=print) -> bool:
    """Runs argv until it exits 0, at most count times, each under bound seconds, pause seconds apart."""
    for i in range(1, count + 1):
        try:
            if run(argv, timeout=bound).returncode == 0:
                return True
            say(f"ios_boot: try {i} of {count} failed: {' '.join(argv[:2])}")
        except subprocess.TimeoutExpired:
            say(f"ios_boot: try {i} of {count} ran past {bound} s: {' '.join(argv[:2])}")
        if i < count:
            sleep(pause)
    return False


def resolve_argv() -> list[str]:
    return ["xcodebuild", "-resolvePackageDependencies", "-project", str(PROJECT), "-scheme", "App", "-derivedDataPath", str(DERIVED)]


def classes(otool: str) -> set[str]:
    """The Objective-C class names `otool -ov` reads from an executable's class list, and those `nm` names `_OBJC_CLASS_$_<name>`."""
    return set(re.findall(r"^\s+name\s+0x[0-9a-f]+\s+(\S+)$", otool, re.M)) | set(re.findall(r"\b_OBJC_CLASS_\$_(\w+)$", otool, re.M))


def executable(derived: Path, config: str) -> Path:
    return derived / f"Build/Products/{config}-iphonesimulator/App.app/App"


def policy_build_argv() -> list[str]:
    return [
        "xcodebuild", "-project", str(PROJECT), "-scheme", "App", "-configuration", "Release", "-sdk", "iphonesimulator",
        "-destination", "generic/platform=iOS Simulator", "-derivedDataPath", str(POLICY_DERIVED),
        "DEAD_CODE_STRIPPING=YES", "CODE_SIGNING_ALLOWED=NO", "build",
    ]


def read_classes(exe: Path, run=subprocess.run) -> set[str]:
    found = set()
    for argv in (["xcrun", "otool", "-ov", str(exe)], ["xcrun", "nm", "-U", str(exe)]):
        found |= classes(run(argv, capture_output=True, text=True, timeout=300).stdout)
    return found


def policy(run=subprocess.run, say=print) -> int:
    """The default build holds no policy; a build with the sample in the packaging's place holds it, by the fixed name."""
    default = executable(DERIVED, "Debug")
    if not default.exists():
        say(f"ios_boot: no default build at {default}: make mobile-ios first")
        return 1
    if POLICY_CLASS in read_classes(default, run):
        say(f"ios_boot: the default build holds {POLICY_CLASS}: the open-source app must sign nothing")
        return 1
    if PLACE.exists() and any(PLACE.iterdir()):
        say(f"ios_boot: {PLACE} is not empty: the check puts the sample there alone")
        return 1
    PLACE.mkdir(parents=True, exist_ok=True)
    copy = PLACE / SAMPLE.name
    shutil.copyfile(SAMPLE, copy)
    try:
        if run(policy_build_argv(), timeout=BUILD_BOUND).returncode != 0:
            say("ios_boot: the build with the sample policy failed")
            return 1
    finally:
        copy.unlink()
        PLACE.rmdir()
    if POLICY_CLASS not in read_classes(executable(POLICY_DERIVED, "Release"), run):
        say(f"ios_boot: the build with the sample policy does not hold {POLICY_CLASS}")
        return 1
    say(f"ios_boot: {POLICY_CLASS} is in the build with the sample policy and not in the default build")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n\n")[0])
    ap.add_argument("what", choices=["resolve", "policy"])
    what = ap.parse_args().what
    if what == "resolve":
        return 0 if tries(resolve_argv(), RESOLVE_BOUND) else 1
    return policy()


if __name__ == "__main__":
    sys.exit(main())
