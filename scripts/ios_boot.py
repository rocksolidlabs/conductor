#!/usr/bin/env python3
"""The phone shell's iOS app on a Mac with Xcode: its Swift packages resolved, tried again when a download fails.

    python3 scripts/ios_boot.py resolve   # xcodebuild -resolvePackageDependencies, up to three tries a minute apart

The app's project fetches the native runtime's Swift package and its two
binary frameworks from GitHub; a check's own setup is part of the check
(AGENTS.md), so a failed or stalled fetch is tried again, each try under a
bound of its own, before the build. Only Python's standard library.
"""
from __future__ import annotations

import argparse
from pathlib import Path
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
PROJECT = ROOT / "mobile/ios/App/App.xcodeproj"
DERIVED = ROOT / "mobile/ios/App/build"
TRIES = 3
PAUSE = 60
RESOLVE_BOUND = 300


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


def main() -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n\n")[0])
    ap.add_argument("what", choices=["resolve"])
    ap.parse_args()
    return 0 if tries(resolve_argv(), RESOLVE_BOUND) else 1


if __name__ == "__main__":
    sys.exit(main())
