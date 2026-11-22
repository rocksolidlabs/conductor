#!/usr/bin/env python3
"""The phone shell's Android app: its build and JVM tests, the emulator's system image, and its boot check on an emulator.

    python3 scripts/android_boot.py build   # Gradle: the debug build, the JVM tests and the boot check's build, fetches tried three times
    python3 scripts/android_boot.py image   # the emulator's system image installed (three tries), before the emulator starts
    python3 scripts/android_boot.py check   # on a running emulator: the boot check (ShellBootsUnderItsPolicyTest), its log after

Gradle fetches its own distribution and every package the app is built
from; the emulator's image comes from Google's repository. A check's own
setup is part of the check (AGENTS.md), so a try that fails for a fetch (a
download refused, cut or timed out, a name that did not resolve) is made
again, a minute later, each try under a bound of its own; a try that fails
for any other reason (a compile error, a failing test) fails at once. The
boot check runs once, under a bound of its own, and its report lines are
printed from the emulator's log whatever it answered. Only Python's
standard library.
"""
from __future__ import annotations

import argparse
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import threading
import time

ROOT = Path(__file__).resolve().parents[1]
ANDROID = ROOT / "mobile/android"
TRIES = 3
PAUSE = 60
BUILD_BOUND = 2400
IMAGE_BOUND = 900
CHECK_BOUND = 1800
# The emulator the boot check runs on (ci.yml's mobile-android job names the same).
API_LEVEL = 34
IMAGE = f"system-images;android-{API_LEVEL};google_apis;x86_64"
GRADLE = ["./gradlew", "--no-daemon", "--stacktrace"]
BUILD_TASKS = [":app:assembleDebug", ":app:testDebugUnitTest", ":app:assembleDebugAndroidTest"]
CHECK_TASKS = [":app:connectedDebugAndroidTest"]
# What a try that failed for a fetch says (Gradle's, the wrapper's, sdkmanager's), as opposed to a build or a test that failed.
FETCH = re.compile(
    r"Could not (?:GET|HEAD|download|resolve|install Gradle distribution)|Could not resolve all (?:files|dependencies|artifacts)"
    r"|Connection (?:reset|refused|timed out)|Read timed out|connect timed out|Remote host terminated the handshake"
    r"|Received status code (?:429|5\d\d)|status code (?:429|5\d\d)|UnknownHostException|Temporary failure in name resolution"
    r"|Premature end of Content-Length|SSLHandshakeException|Failed to download|Error: Failed to install"
)
LOG_TAG = "ConductorBootCheck"


def tell(message: str) -> None:
    """A line said at once, so that the job's log keeps it in order beside Gradle's own."""
    print(message, flush=True)


def run_streamed(argv: list[str], cwd: Path, bound: int) -> tuple[int, str]:
    """Runs argv, its output streamed to ours and its last lines kept, under bound seconds; a run past it is stopped (its whole
    process group) and answers 124."""
    proc = subprocess.Popen(argv, cwd=cwd, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, start_new_session=True)
    kept: list[str] = []

    def pump() -> None:
        assert proc.stdout is not None
        for line in proc.stdout:
            sys.stdout.write(line)
            sys.stdout.flush()
            kept.append(line)
            if len(kept) > 4000:
                del kept[:1000]

    reader = threading.Thread(target=pump, daemon=True)
    reader.start()
    try:
        code = proc.wait(timeout=bound)
    except subprocess.TimeoutExpired:
        os.killpg(proc.pid, signal.SIGKILL)
        proc.wait()
        code = 124
    reader.join(timeout=30)
    if proc.stdout is not None:
        proc.stdout.close()
    return code, "".join(kept)


def tries(argv: list[str], cwd: Path, bound: int, count: int = TRIES, pause: int = PAUSE, run=run_streamed, sleep=time.sleep, say=tell) -> bool:
    """Runs argv until it exits 0, at most count times, each under bound seconds, pause seconds apart; only a fetch's failure is tried again."""
    for i in range(1, count + 1):
        code, output = run(argv, cwd, bound)
        if code == 0:
            return True
        fetch = code == 124 or bool(FETCH.search(output))
        say(f"android_boot: try {i} of {count} {'ran past its bound' if code == 124 else 'failed'}: {' '.join(argv[:4])}")
        if not fetch:
            say("android_boot: the failure is not a fetch's: not tried again")
            return False
        if i < count:
            sleep(pause)
    return False


def sdkmanager() -> str | None:
    """The SDK's sdkmanager, from ANDROID_HOME (or ANDROID_SDK_ROOT), or the PATH."""
    for home in (os.environ.get("ANDROID_HOME"), os.environ.get("ANDROID_SDK_ROOT")):
        if home:
            for p in sorted(Path(home).glob("cmdline-tools/*/bin/sdkmanager"), reverse=True):
                return str(p)
    return shutil.which("sdkmanager")


def build(**kw) -> int:
    if not (ANDROID / "capacitor.settings.gradle").exists():
        tell("android_boot: no mobile/android project: run make mobile-install mobile-sync first")
        return 1
    return 0 if tries([*GRADLE, *BUILD_TASKS], ANDROID, BUILD_BOUND, **kw) else 1


def image(**kw) -> int:
    tool = sdkmanager()
    if tool is None:
        tell("android_boot: no sdkmanager (ANDROID_HOME unset): the Android SDK's command-line tools are needed")
        return 1
    return 0 if tries([tool, "--install", IMAGE, "emulator", "platform-tools"], ROOT, IMAGE_BOUND, **kw) else 1


def check(run=run_streamed, say=tell) -> int:
    code, _ = run([*GRADLE, *CHECK_TASKS], ANDROID, CHECK_BOUND)
    adb = shutil.which("adb")
    if adb:
        lines = 0
        out = subprocess.run([adb, "logcat", "-d", "-s", f"{LOG_TAG}:I"], capture_output=True, text=True, timeout=120).stdout
        for line in out.splitlines():
            if LOG_TAG in line:
                say(line)
                lines += 1
        say(f"android_boot: {lines} line(s) of the boot check's log")
    if code != 0:
        say("android_boot: the boot check failed" if code != 124 else f"android_boot: the boot check ran past {CHECK_BOUND} s")
        return 1
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n\n")[0])
    ap.add_argument("step", choices=["build", "image", "check"])
    step = ap.parse_args().step
    return {"build": build, "image": image, "check": check}[step]()


if __name__ == "__main__":
    sys.exit(main())
