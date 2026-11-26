#!/usr/bin/env python3
"""The phone shell's iOS app on a Mac with Xcode: its Swift packages resolved, and its boot check on a simulator.

    python3 scripts/ios_boot.py resolve   # xcodebuild -resolvePackageDependencies, up to three tries a minute apart
    python3 scripts/ios_boot.py policy    # a build with the sample signing policy in the packaging's place: its class in the app
    python3 scripts/ios_boot.py check     # a simulator booted (three tries), then the UI test ShellBootsUnderItsPolicyTests

The app's project fetches the native runtime's Swift package and its two
binary frameworks from GitHub; a check's own setup is part of the check
(AGENTS.md), so a failed or stalled fetch is tried again, each try under a
bound of its own, before the build. The boot check runs on the newest iPhone
simulator the machine has: booted and waited for as a step of its own,
tried again after a shutdown when it does not come up in its bound; the test
itself runs once, under a bound of its own, unsigned. The policy check copies the sample
policy (mobile/ios/fixtures) into the packaging's place for one Release build,
dead code stripped, and reads the built executable's Objective-C classes for
the key's fixed name; the default build (`make mobile-ios`'s) must hold
none. The copy is removed whatever the build did. Only Python's standard
library.
"""
from __future__ import annotations

import argparse
import json
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
# The Swift packages at the versions the committed Package.resolved pins, never resolved anew.
PINNED = ["-disableAutomaticPackageResolution", "-onlyUsePackageVersionsFromResolvedFile"]
BUILD_BOUND = 1500
POLICY_CLASS = "ConductorKeySigningPolicy"
PLACE = ROOT / "mobile/ios/ConductorNative/Sources/ConductorPackaging/Packaging"
SAMPLE = ROOT / "mobile/ios/fixtures/SampleSigningPolicy.swift"
POLICY_DERIVED = ROOT / "mobile/ios/App/build-policy"
BOOT_BOUND = 600
TEST_BOUND = 1500


def tell(message: str) -> None:
    """A line said at once, so that the job's log keeps it in order beside xcodebuild's own."""
    print(message, flush=True)


def tries(argv: list[str], bound: int, count: int = TRIES, pause: int = PAUSE, run=subprocess.run, sleep=time.sleep, say=tell) -> bool:
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
    return ["xcodebuild", "-resolvePackageDependencies", "-project", str(PROJECT), "-scheme", "App", "-derivedDataPath", str(DERIVED), *PINNED]


def classes(otool: str) -> set[str]:
    """The Objective-C class names `otool -ov` reads from an executable's class list, and those `nm` names `_OBJC_CLASS_$_<name>`."""
    return set(re.findall(r"^\s+name\s+0x[0-9a-f]+\s+(\S+)$", otool, re.M)) | set(re.findall(r"\b_OBJC_CLASS_\$_(\w+)$", otool, re.M))


def executable(derived: Path, config: str) -> Path:
    return derived / f"Build/Products/{config}-iphonesimulator/App.app/App"


def policy_build_argv() -> list[str]:
    return [
        "xcodebuild", "-project", str(PROJECT), "-scheme", "App", "-configuration", "Release", "-sdk", "iphonesimulator",
        "-destination", "generic/platform=iOS Simulator", "-derivedDataPath", str(POLICY_DERIVED), *PINNED,
        "DEAD_CODE_STRIPPING=YES", "CODE_SIGNING_ALLOWED=NO", "build",
    ]


def read_classes(exe: Path, run=subprocess.run) -> set[str]:
    found = set()
    for argv in (["xcrun", "otool", "-ov", str(exe)], ["xcrun", "nm", "-U", str(exe)]):
        found |= classes(run(argv, capture_output=True, text=True, timeout=300).stdout)
    return found


def policy(run=subprocess.run, say=tell) -> int:
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
def version(runtime: str) -> tuple[int, ...]:
    """A simulator runtime's iOS version from its identifier (com.apple.CoreSimulator.SimRuntime.iOS-18-2): (18, 2); none is ()."""
    m = re.search(r"\.iOS-([0-9-]+)$", runtime)
    return tuple(int(p) for p in m.group(1).split("-")) if m else ()


def pick(listing: dict) -> str | None:
    """The udid of an available iPhone on the newest iOS runtime of `xcrun simctl list devices available -j`; one already booted first."""
    best = None
    for runtime, devices in (listing.get("devices") or {}).items():
        v = version(runtime)
        if not v:
            continue
        for d in devices:
            if not d.get("isAvailable", True) or not str(d.get("name", "")).startswith("iPhone"):
                continue
            rank = (v, d.get("state") == "Booted", d.get("name", ""))
            if best is None or rank > best[0]:
                best = (rank, d["udid"])
    return best[1] if best else None


# What bootstatus says of a simulator whose first start did not finish setting it up. It has said so and still answered 0
# (2026-11-25, #281): the app could then not be launched on it ("Failed to get background assertion for target").
MIGRATION_FAILED = "Data Migration Failed"


def boot(udid: str, run=subprocess.run, sleep=time.sleep, say=tell) -> bool:
    """Boots udid and waits until it has booted, three tries, a shutdown between them. bootstatus is judged by its exit code
    and by its words: a simulator whose data migration failed is not booted, and is erased before the next try."""
    for i in range(1, TRIES + 1):
        run(["xcrun", "simctl", "boot", udid], timeout=BOOT_BOUND)  # refused when already booted; the wait below decides
        unset = False
        try:
            status = run(["xcrun", "simctl", "bootstatus", udid, "-b"], capture_output=True, text=True, timeout=BOOT_BOUND)
            said = (getattr(status, "stdout", "") or "") + (getattr(status, "stderr", "") or "")
            if said.strip():
                say(said.rstrip())
            unset = MIGRATION_FAILED in said
            if status.returncode == 0 and not unset:
                return True
            say(f"ios_boot: the simulator {'was not set up: its data migration failed' if unset else 'did not boot'} (try {i} of {TRIES})")
        except subprocess.TimeoutExpired:
            say(f"ios_boot: the simulator was not booted after {BOOT_BOUND} s (try {i} of {TRIES})")
        run(["xcrun", "simctl", "shutdown", udid], timeout=BOOT_BOUND)
        if unset:
            run(["xcrun", "simctl", "erase", udid], timeout=BOOT_BOUND)
        if i < TRIES:
            sleep(PAUSE)
    return False


def test_argv(udid: str) -> list[str]:
    return [
        "xcodebuild", "test", "-project", str(PROJECT), "-scheme", "App", "-destination", f"id={udid}",
        "-derivedDataPath", str(DERIVED), *PINNED, "-only-testing:AppUITests/ShellBootsUnderItsPolicyTests", "CODE_SIGNING_ALLOWED=NO",
    ]


def check(run=subprocess.run, say=tell) -> int:
    listing = run(["xcrun", "simctl", "list", "devices", "available", "-j"], capture_output=True, text=True, timeout=120)
    udid = pick(json.loads(listing.stdout or "{}"))
    if not udid:
        say("ios_boot: no iPhone simulator on this machine")
        return 1
    say(f"ios_boot: the boot check on simulator {udid}")
    if not boot(udid, run=run, say=say):
        return 1
    try:
        return run(test_argv(udid), timeout=TEST_BOUND).returncode
    except subprocess.TimeoutExpired:
        say(f"ios_boot: the boot check ran past {TEST_BOUND} s")
        return 1


def main() -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n\n")[0])
    ap.add_argument("what", choices=["resolve", "check", "policy"])
    what = ap.parse_args().what
    if what == "resolve":
        return 0 if tries(resolve_argv(), RESOLVE_BOUND) else 1
    return check() if what == "check" else policy()


if __name__ == "__main__":
    sys.exit(main())
