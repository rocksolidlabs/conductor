#!/usr/bin/env python3
"""The Android app's shipped packages, as Gradle resolves them, against THIRD_PARTY_NOTICES.

Gradle's task `:app:conductorReleaseClasspath` (mobile/android/app/notices.gradle) writes every module of the release variant's
runtime classpath, `group:module:version`, a tab, and the licences its POM names (`name <url>`, several joined by ` | `), to
mobile/android/app/build/reports/conductor/release-classpath.txt. The committed copy, mobile/android/release-classpath.txt, is what
scripts/notices.py makes the Android part of THIRD_PARTY_NOTICES from: no list is kept by hand.

    python3 scripts/android_notices.py check    # Gradle's fresh list equals the committed one, and the notices name each package
    python3 scripts/android_notices.py update   # the fresh list committed in place of the old (then make notices)

Each licence a POM names is read as one of the kinds below, with the text kept for it in scripts/notices/; a licence of no known
kind is named with its URL and no text. Only Python's standard library.
"""
from __future__ import annotations

import argparse
import difflib
from pathlib import Path
import re
import shutil
import sys

ROOT = Path(__file__).resolve().parents[1]
LIST = ROOT / "mobile/android/release-classpath.txt"
FRESH = ROOT / "mobile/android/app/build/reports/conductor/release-classpath.txt"
NOTICES = ROOT / "THIRD_PARTY_NOTICES"
PART = "The phone shell's Android app (mobile/android):"
# A licence's kind, read from the name and URL its POM gives: its label in the notices and the text kept for it (none: named with its
# URL only, as for terms whose text is a page of their owner's to keep).
KINDS = [
    (re.compile(r"apache.*2\.0|apache-2\.0|apache\.org/licenses/license-2\.0", re.I), "Apache-2.0", "scripts/notices/apache-2.0.LICENSE"),
    (re.compile(r"bouncy ?castle", re.I), "MIT (Bouncy Castle Licence)", "scripts/notices/bouncycastle-1.82.LICENSE"),
    (re.compile(r"android software development kit license|developer\.android\.com/studio/terms", re.I), "Android Software Development Kit License", None),
    (re.compile(r"bsd-3-clause|bsd 3-clause", re.I), "BSD-3-Clause", None),
]
# A licence whose text names its holder (BSD's, MIT's) is the holder's own text: kept for the package it was taken from, by name.
PACKAGE_TEXTS = {
    # The datastore library carries a copy of Protocol Buffers' Java runtime, under Protocol Buffers' own BSD-3-Clause licence.
    "androidx.datastore:datastore-preferences-external-protobuf": "scripts/notices/protobuf.LICENSE",
}


class Package:
    def __init__(self, group: str, module: str, version: str, licences: list[str]):
        self.group, self.module, self.version, self.licences = group, module, version, licences

    @property
    def name(self) -> str:
        return f"{self.group}:{self.module}"

    def kind(self) -> tuple[str, str | None]:
        """The licence's label and its kept text's path, for the first licence of a known kind; else the POM's own words, no text."""
        for licence in self.licences:
            for pattern, label, text in KINDS:
                if pattern.search(licence):
                    return label, text or PACKAGE_TEXTS.get(self.name)
        return (" | ".join(self.licences) or "no licence named"), None


def parse(text: str) -> list[Package]:
    out = []
    for line in text.splitlines():
        if not line.strip():
            continue
        coords, _, licences = line.partition("\t")
        parts = coords.split(":")
        if len(parts) != 3 or not all(parts):
            raise ValueError(f"not a package: {line!r}")
        named = [] if licences.strip() in ("", "no licence named") else [l.strip() for l in licences.split(" | ")]
        out.append(Package(parts[0], parts[1], parts[2], named))
    return out


def unnamed(packages: list[Package], notices: str) -> list[str]:
    """The packages THIRD_PARTY_NOTICES' Android part does not name, `group:module version` as its lists write them."""
    part = notices.split(PART, 1)[1].split("\n\n", 1)[0] if PART in notices else ""
    return [f"{p.name} {p.version}" for p in packages if not re.search(rf"^\s+{re.escape(p.name)} {re.escape(p.version)}\s", part, re.M)]


def check(fresh: Path = FRESH, committed: Path = LIST, notices: Path = NOTICES, say=print) -> int:
    if not fresh.exists():
        say(f"android_notices: no {fresh}: run make mobile-android-notices (Gradle's :app:conductorReleaseClasspath) first")
        return 1
    new = fresh.read_text()
    old = committed.read_text() if committed.exists() else ""
    if new != old:
        say(f"android_notices: Gradle's list of the release variant's packages is not the committed {committed.relative_to(ROOT) if committed.is_relative_to(ROOT) else committed}:")
        for line in difflib.unified_diff(old.splitlines(), new.splitlines(), "committed", "Gradle's", lineterm=""):
            say(line)
        say("android_notices: commit Gradle's (python3 scripts/android_notices.py update), then make notices")
        return 1
    missing = unnamed(parse(new), notices.read_text())
    if missing:
        say("android_notices: THIRD_PARTY_NOTICES does not name: " + ", ".join(missing) + ": run make notices")
        return 1
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n\n")[0])
    ap.add_argument("step", choices=["check", "update"])
    if ap.parse_args().step == "update":
        if not FRESH.exists():
            print(f"android_notices: no {FRESH}", file=sys.stderr)
            return 1
        shutil.copyfile(FRESH, LIST)
        return 0
    return check(say=lambda s: print(s, file=sys.stderr))


if __name__ == "__main__":
    sys.exit(main())
