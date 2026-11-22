#!/usr/bin/env python3
"""Generate/check THIRD_PARTY_NOTICES: the licences of what Conductor ships.

Three parts, each from what its build carries rather than what builds it:
the Go modules compiled into the conductor binary (`go list -deps` for the
platforms released), the npm packages in the web bundle the binary embeds
(web/.nuxt/bundled-packages.json, written by web/build/bundledPackages.ts
during `make web-build`) and the desktop app's own: Electron and the
production packages electron-builder puts in the app; and the phone shell's
(mobile/): the native runtime's packages the iOS and Android apps are built
from, and every package the Android app's release build carries as Gradle
resolves it (mobile/android/release-classpath.txt). Texts a
package does not carry (the Inter font @nuxt/fonts fetches, the Lucide icons
Nuxt Icon inlines, the Gradle packages' licences) are kept in scripts/notices/.

    python3 scripts/notices.py           # write THIRD_PARTY_NOTICES
    python3 scripts/notices.py --check   # exit 1 when it is not current

Needs Go, a web build and `npm ci` in desktop/ and mobile/; only Python's standard
library otherwise.
"""
from __future__ import annotations

import argparse
import difflib
import json
import os
from pathlib import Path
import re
import textwrap
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / "THIRD_PARTY_NOTICES"
EXTRA = ROOT / "scripts/notices"
WEB = ROOT / "web"
DESKTOP = ROOT / "desktop"
MOBILE = ROOT / "mobile"
BUNDLED = WEB / ".nuxt/bundled-packages.json"
# The platforms the conductor binary is released for (the desktop packages and the image): their module sets are united.
PLATFORMS = [("linux", "amd64"), ("linux", "arm64"), ("darwin", "amd64"), ("darwin", "arm64")]
LICENCE_FILE = re.compile(r"^(licen[cs]e|copying|notice|thirdpartynotices|unlicense)([.-][a-z0-9.-]*)?$", re.I)
NOT_TEXT = re.compile(r"\.(js|mjs|cjs|ts|json|html?)$", re.I)
WIDTH = 78
# Packages that ship without their licence text: the upstream text, kept in scripts/notices.
TEXT_KEPT = {
    "embla-carousel": "embla-carousel.LICENSE",
    "embla-carousel-auto-height": "embla-carousel.LICENSE",
    "embla-carousel-auto-scroll": "embla-carousel.LICENSE",
    "embla-carousel-autoplay": "embla-carousel.LICENSE",
    "embla-carousel-class-names": "embla-carousel.LICENSE",
    "embla-carousel-fade": "embla-carousel.LICENSE",
    "embla-carousel-reactive-utils": "embla-carousel.LICENSE",
    "embla-carousel-vue": "embla-carousel.LICENSE",
    "embla-carousel-wheel-gestures": "embla-carousel-wheel-gestures.LICENSE",
    "vaul-vue": "vaul-vue.LICENSE",
    "lazy-val": "lazy-val.LICENSE",
    "@iconify-json/lucide": "lucide.LICENSE",
}
# Notices a package needs beside its own licence: what it carries built from other projects. A notice taken for one version of
# the package names it ("for"): another version stops the script until the notice is taken again for it.
SUPPLEMENTS: dict[str, list[dict[str, str]]] = {
    "@shikijs/engine-oniguruma": [{"name": "Oniguruma's COPYING (compiled into its onig.wasm)", "file": "oniguruma.COPYING"}],
    # Shiki 4.4.3 was built from tm-themes 1.12.3 and tm-grammars 1.32.3 (its pnpm-lock.yaml at v4.4.3); every theme and grammar
    # ships, as a chunk of its own.
    "@shikijs/themes": [{"name": "tm-themes 1.12.3's NOTICE (the themes it carries)", "file": "tm-themes-1.12.3.NOTICE", "for": "4.4.3"}],
    "@shikijs/langs": [{"name": "tm-grammars 1.32.3's NOTICE (the grammars it carries)", "file": "tm-grammars-1.32.3.NOTICE", "for": "4.4.3"}],
    # Monaco 0.57.0 vendors DOMPurify 3.4.15 (esm/vs/base/browser/dompurify) and ships the Codicons font (CC BY 4.0), both of
    # which its ThirdPartyNotices.txt leaves out.
    # The native runtime's iOS package compiles Apache Cordova's iOS classes into its Cordova framework (their files carry the
    # ASF's Apache-2.0 header); Cordova's NOTICE goes with them.
    "@capacitor/ios": [{"name": "Apache Cordova's NOTICE (its iOS classes, compiled into the Cordova framework, Apache-2.0)", "file": "cordova-ios.NOTICE"}],
    # The native runtime's Android library builds against Apache Cordova's Android framework, which goes into the app; its NOTICE goes
    # with it.
    "@capacitor/android": [{"name": "Apache Cordova's NOTICE (its Android framework, built into the app, Apache-2.0)", "file": "cordova-android.NOTICE"}],
    "monaco-editor": [
        {"name": "DOMPurify 3.4.15's LICENSE (vendored as esm/vs/base/browser/dompurify)", "file": "dompurify-3.4.15.LICENSE", "for": "0.57.0"},
        {"name": "Codicons (the codicon.ttf icon font, CC BY 4.0)", "file": "codicons.NOTICE", "for": "0.57.0"},
    ],
}
# A file's own copyright line: "Copyright (c) 2014 Name", "SPDX-FileCopyrightText: 2026 Name <url>", "@license X | (c) Name | …".
# A bare "(c)" or "©" counts only standing alone before a year or a name, so code in prose (f(c), lower(c)) does not.
MARK = r"(?<![\w(])(?:\([cC]\)|©)(?=\s+[0-9A-Z])"
COPYRIGHT = re.compile(r"(?:(?i:(?:spdx-file)?copyright(?:text:)?)\s*(?:\([cC]\)|©)?|" + MARK + r")\s*(?:[0-9]{4}(?:\s*[-–,]\s*(?:[0-9]{4}|present))*,?\s*)*(?:by\s+)?(.+)")
# A comment that is a notice: one with a copyright line, or a license banner.
NOTICE = re.compile(r"(?i:copyright|@license)|" + MARK)
# "(b)" beside a "(c)" makes it a list's third item, not a copyright mark.
LISTED = re.compile(r"(?<![\w(])\([bB]\)")


def is_notice(block: str) -> bool:
    if re.search(r"copyright|@license", block, re.I):
        return True
    return bool(re.search(MARK, block)) and not LISTED.search(block)


class Entry:
    def __init__(self, name: str, version: str, licence: str, texts: list[tuple[str, str]], note: str = ""):
        self.name, self.version, self.licence, self.texts, self.note = name, version, licence, texts, note

    @property
    def title(self) -> str:
        return f"{self.name} {self.version}".strip()


def run(argv: list[str], cwd: Path = ROOT, env: dict[str, str] | None = None) -> str:
    return subprocess.run(argv, cwd=cwd, env=env, check=True, capture_output=True, text=True).stdout


def licence_files(directory: Path) -> list[tuple[str, str]]:
    """The licence and notice files at a package's root, as (file name, text), sorted by name."""
    found = []
    for p in sorted(directory.iterdir(), key=lambda p: p.name.lower()):
        if p.is_file() and LICENCE_FILE.match(p.name) and not NOT_TEXT.search(p.name):
            found.append((p.name, p.read_text(encoding="utf-8", errors="replace")))
    return found


def classify(text: str) -> str:
    """The SPDX id a licence text reads as, or '' when it is none this script knows."""
    t = " ".join(text.split())
    if "Apache License" in t and "Version 2.0" in t:
        return "Apache-2.0"
    if "Mozilla Public License" in t and ("Version 2.0" in t or "version 2.0" in t):
        return "MPL-2.0"
    if "SIL OPEN FONT LICENSE" in t.upper():
        return "OFL-1.1"
    if "Permission is hereby granted, free of charge" in t:
        return "MIT"
    if "Permission to use, copy, modify, and/or distribute" in t or "Permission to use, copy, modify, and distribute" in t:
        return "ISC"
    if "Redistribution and use in source and binary forms" in t:
        return "BSD-3-Clause" if ("Neither the name" in t or "names of its contributors" in t) else "BSD-2-Clause"
    if "free and unencumbered software released into the public domain" in t:
        return "Unlicense"
    return ""


def comment_blocks(path: Path, limit: int = 200) -> list[str]:
    """The comments in a source file's first lines, each run of // lines or /* */ block as one text, markers, a block's common
    indent and build directives taken out (a notice can follow the package clause, a block need not star its lines)."""
    blocks: list[str] = []
    run: list[str] = []
    block: list[str] | None = None
    with path.open(encoding="utf-8", errors="replace") as f:
        for i, raw in enumerate(f):
            if i >= limit:
                break
            line = raw.rstrip("\n")
            if block is not None:
                end = line.find("*/")
                block.append(line if end < 0 else line[:end])
                if end >= 0:
                    blocks.append(textwrap.dedent("\n".join(re.sub(r"^\s*\*(?!/) ?", "", l) if l.lstrip().startswith("*") else l for l in block)).strip("\n"))
                    block = None
                continue
            s = line.strip()
            if s.startswith("//"):
                if not s.startswith(("//go:", "// +build", "//+build")):
                    run.append(re.sub(r"^//\s?", "", s))
                continue
            if run:
                blocks.append("\n".join(run).strip("\n"))
                run = []
            if s.startswith("/*"):
                rest = s[2:]
                end = rest.find("*/")
                if end >= 0:
                    blocks.append(rest[:end].strip())
                else:
                    block = [rest]
    if run:
        blocks.append("\n".join(run).strip("\n"))
    return [b for b in blocks if b]


def notice_comments(path: Path) -> list[str]:
    """The comments near a file's top that have a copyright line in them (a file can carry its own and the code's origin's)."""
    return [b for b in comment_blocks(path) if is_notice(b)]


def file_notices(files: list[Path], base: Path, licence_text: str) -> list[tuple[str, str]]:
    """The notices of the files whose copyright holder their module's licence does not name (code taken from elsewhere, an
    author's own line): each distinct comment once, as (where it is, the comment)."""
    known = " ".join(licence_text.split()).lower()
    found: dict[str, list[str]] = {}
    for path in files:
        for head in notice_comments(path):
            for m in COPYRIGHT.finditer(head):
                if not m.group(0).lower().startswith(("copyright", "spdx")) and LISTED.search(head):
                    continue
                holder = re.sub(r"<[^>]*>|\(?https?://\S+\)?|all rights reserved\.?|@license.*$", "", m.group(1).split(" | ")[0], flags=re.I)
                holder = " ".join(holder.split()).strip(" ,.")
                # Named by the licence: the holder as written, or its first words ("Yuxi (Evan) You" of "… and Vue contributors").
                if holder and holder.lower()[:40] not in known and " ".join(holder.lower().split()[:3]) not in known:
                    found.setdefault(head, []).append(path.relative_to(base).as_posix())
                    break
    out = []
    for head, paths in sorted(found.items(), key=lambda kv: sorted(kv[1])[0]):
        paths.sort()
        where = paths[0] if len(paths) == 1 else f"{paths[0]} and {len(paths) - 1} more"
        out.append((f"{where} (the file's own notice)", head))
    return out


def go_env() -> dict[str, str]:
    """The environment that runs the toolchain go.mod pins, exactly: the notices name it, so a newer go on the PATH must not
    scan its own sources under that name."""
    toolchain = re.search(r"^toolchain (go\S+)", (ROOT / "go.mod").read_text(), re.M)
    if not toolchain:
        raise SystemExit("notices: go.mod pins no toolchain")
    env = {**os.environ, "GOTOOLCHAIN": toolchain.group(1)}
    have = run(["go", "env", "GOVERSION"], env=env).strip()
    if have != toolchain.group(1):
        raise SystemExit(f"notices: go runs {have}, not the {toolchain.group(1)} go.mod pins")
    return env


def go_entries() -> list[Entry]:
    base_env = go_env()
    mods: dict[tuple[str, str], Path] = {}
    sources: dict[str, set[Path]] = {}  # module path ("std" for Go's own) -> the source files compiled
    fmt = ("{{.Dir}}\t{{with .Module}}{{if .Main}}main\t\t{{else}}{{.Path}}\t{{.Version}}\t{{with .Replace}}{{.Dir}}{{else}}{{.Dir}}"
           "{{end}}{{end}}{{else}}std\t\t{{end}}\t{{join .GoFiles \" \"}} {{join .SFiles \" \"}}")
    for goos, goarch in PLATFORMS:
        env = {**base_env, "GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0", "GOFLAGS": "-mod=readonly"}
        for line in run(["go", "list", "-deps", "-f", fmt, "./cmd/conductor"], env=env).splitlines():
            if not line.strip():
                continue
            pkgdir, path, version, directory, names = line.split("\t")
            if path == "main":
                continue
            if path != "std":
                mods[(path, version)] = Path(directory)
            sources.setdefault(path, set()).update(Path(pkgdir) / n for n in names.split())
    entries = []
    for (path, version), directory in sorted(mods.items()):
        texts = licence_files(directory)
        kinds = sorted({classify(t) for n, t in texts if not n.lower().startswith("notice")} - {""})
        if not kinds:
            raise SystemExit(f"notices: no licence this script knows for the Go module {path} {version} in {directory}")
        # More than one licence text: each applies to the files it names (a project's COPYING says which); MPL-2.0 files' source is named.
        note = f"the source of its MPL-2.0 files: https://{path} at {version}" if "MPL-2.0" in kinds else ""
        texts += file_notices(sorted(sources.get(path, ())), directory, " ".join(t for _, t in texts))
        entries.append(Entry(path, version, ", ".join(kinds), texts, note))
    goroot = Path(run(["go", "env", "GOROOT"], env=base_env).strip())
    licence = (goroot / "LICENSE").read_text()
    std = file_notices(sorted(sources.get("std", ())), goroot / "src", licence)
    entries.insert(0, Entry("Go (the standard library and runtime)", base_env["GOTOOLCHAIN"], "BSD-3-Clause",
                            [("LICENSE", licence)] + std))
    return entries


# Source files whose comments the file-notice scan reads (a JSON grammar or theme has none; its package's notice covers it).
SCANNED = re.compile(r"\.(m?js|cjs|ts|css)$")


def npm_entry(directory: Path, note: str = "", files: list[Path] | None = None) -> Entry:
    pkg = json.loads((directory / "package.json").read_text())
    licence = pkg.get("license") or ""
    if isinstance(licence, dict):
        licence = licence.get("type", "")
    texts = licence_files(directory)
    if not texts and pkg["name"] in TEXT_KEPT:
        texts = [("LICENSE", (EXTRA / TEXT_KEPT[pkg["name"]]).read_text())]
    if not texts:
        raise SystemExit(f"notices: the npm package {pkg['name']} {pkg.get('version', '')} carries no licence file; keep one in scripts/notices")
    kind = licence or classify(texts[0][1])
    for s in SUPPLEMENTS.get(pkg["name"], []):
        if s.get("for") and s["for"] != pkg.get("version"):
            raise SystemExit(f"notices: {s['file']} was taken for {pkg['name']} {s['for']}, not {pkg.get('version')}: take it again for this version")
        texts.append((s["name"], (EXTRA / s["file"]).read_text()))
    scanned = [f for f in (files or []) if SCANNED.search(f.name) and f.is_file()]
    texts += file_notices(scanned, directory, " ".join(t for _, t in texts))
    return Entry(pkg["name"], pkg.get("version", ""), kind, texts, note)


def unique(entries: list[Entry]) -> list[Entry]:
    seen: dict[str, Entry] = {}
    for e in entries:
        seen.setdefault(e.title, e)
    return sorted(seen.values(), key=lambda e: (e.name.lower(), e.version))


def web_entries() -> list[Entry]:
    if not BUNDLED.exists():
        raise SystemExit(f"notices: no {BUNDLED.relative_to(ROOT)}: run make web-build first")
    bundled = json.loads(BUNDLED.read_text())
    dirs, modules = bundled["packages"], bundled["modules"]
    def files_of(d: str) -> list[Path]:
        # A package's own files: not those of a package nested inside it, which is its own entry.
        return [WEB / m for m in modules if m.startswith(d + "/") and "/node_modules/" not in m[len(d):]]
    entries = [npm_entry(WEB / d, files=files_of(d)) for d in dirs]
    entries.append(npm_entry(WEB / "node_modules/@iconify-json/lucide", note="the icons, inlined by Nuxt Icon"))
    entries = unique(entries)
    entries.append(Entry("Inter (font)", "", "OFL-1.1", [("LICENSE", (EXTRA / "inter.LICENSE").read_text())], note="fetched from Google Fonts by @nuxt/fonts at build time"))
    return entries


def desktop_entries() -> list[Entry]:
    if not (DESKTOP / "node_modules/electron").exists():
        raise SystemExit("notices: no desktop/node_modules: run npm ci in desktop/ first")
    lines = run(["npm", "ls", "--omit=dev", "--all", "--parseable"], cwd=DESKTOP).splitlines()
    entries = [npm_entry(Path(line)) for line in lines if line.strip() and Path(line) != DESKTOP]
    entries.append(npm_entry(DESKTOP / "node_modules/electron", note="Chromium's and Node.js's own notices ship in the app's resources as LICENSES.chromium.html"))
    return unique(entries)


def mobile_entries() -> list[Entry]:
    if not (MOBILE / "node_modules/@capacitor/ios").exists():
        raise SystemExit("notices: no mobile/node_modules: run npm ci in mobile/ first")
    lines = run(["npm", "ls", "--omit=dev", "--all", "--parseable"], cwd=MOBILE).splitlines()
    notes = {
        MOBILE / "node_modules/@capacitor/ios": "the app's Capacitor and Cordova frameworks, fetched by the Swift Package Manager from its capacitor-swift-pm at the same version",
        MOBILE / "node_modules/@capacitor/android": "the Android app's Capacitor library, built by Gradle from this package",
    }
    entries = [npm_entry(Path(line), note=notes.get(Path(line), "")) for line in lines if line.strip() and Path(line) != MOBILE]
    return unique(entries)


ANDROID = MOBILE / "android"
# A text taken for one version of a package: another version stops the script until the text is taken again for it.
ANDROID_TEXT_FOR = {"org.bouncycastle:bcprov-jdk18on": "1.82"}


# The release build's packages as Gradle resolves them (scripts/android_notices.py, mobile/android/app/notices.gradle), committed: the
# Android part is made from it alone.
ANDROID_LIST = ANDROID / "release-classpath.txt"


def android_heading(listed: Path | None = None) -> str:
    return "The phone shell's Android app (mobile/android): every package its release build carries, as Gradle resolves them"


def android_listed(listed: Path) -> list[Entry]:
    sys.path.insert(0, str(Path(__file__).resolve().parent))
    import android_notices

    entries = []
    for p in android_notices.parse(listed.read_text()):
        label, text = p.kind()
        if text is not None:
            if ANDROID_TEXT_FOR.get(p.name, p.version) != p.version:
                raise SystemExit(f"notices: {text} was taken for {p.name} {ANDROID_TEXT_FOR[p.name]}, not {p.version}: take it again for this version")
            texts = [("LICENSE", (ROOT / text).read_text())]
        else:
            named = " | ".join(p.licences) or "no licence named"
            texts = [("LICENSE", f"Its POM names: {named}. The licence's text is its owner's, at that address; it is not copied here.")]
        if p.name == "org.apache.cordova:framework":
            texts += [(s["name"], (EXTRA / s["file"]).read_text()) for s in SUPPLEMENTS.get("@capacitor/android", [])]
        entries.append(Entry(p.name, p.version, label, texts))
    return sorted(entries, key=lambda e: (e.name, e.version))


def android_entries(listed: Path | None = None) -> list[Entry]:
    listed = listed or ANDROID_LIST
    if not listed.exists():
        raise SystemExit(f"notices: no {listed.relative_to(ROOT) if listed.is_relative_to(ROOT) else listed}: Gradle's list of the Android release build's packages (make mobile-android-notices, then python3 scripts/android_notices.py update)")
    return android_listed(listed)


def render(parts: list[tuple[str, list[Entry]]]) -> str:
    out = [
        "THIRD-PARTY NOTICES",
        "",
        "Conductor is licensed under the Apache License, Version 2.0 (LICENSE, with",
        "NOTICE). It includes the software below, each under its own licence, whose",
        "texts follow the lists. Written by scripts/notices.py from what each build",
        "ships; do not edit it by hand.",
        "",
    ]
    for heading, entries in parts:
        out += [heading, "-" * len(heading)]
        for e in entries:
            line = f"  {e.title:<56} {e.licence}"
            out.append(line.rstrip())
            if e.note:
                out.append(f"    ({e.note})")
        out.append("")
    printed: dict[str, str] = {}
    for _, entries in parts:
        for e in entries:
            out += ["=" * WIDTH, e.title, "=" * WIDTH]
            for name, text in e.texts:
                body = text.replace("\r\n", "\n").strip("\n")
                key = " ".join(body.split())
                out += ["", f"--- {name}", ""]
                if key in printed:
                    out.append(f"The same text as {printed[key]}'s above.")
                else:
                    printed[key] = e.title
                    out += [l.rstrip() for l in body.split("\n")]
            out.append("")
    return "\n".join(out).rstrip("\n") + "\n"


def main() -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n\n")[0])
    ap.add_argument("--check", action="store_true", help="exit 1 when THIRD_PARTY_NOTICES is not current")
    args = ap.parse_args()
    text = render([
        ("The conductor binary: Go modules", go_entries()),
        ("The workbench, embedded in the binary: npm packages in the web bundle", web_entries()),
        ("The desktop app: Electron and its packages", desktop_entries()),
        ("The phone shell (mobile/): the native runtime's packages", mobile_entries()),
        (android_heading(), android_entries()),
    ])
    if args.check:
        have = OUT.read_text() if OUT.exists() else ""
        if have == text:
            return 0
        diff = difflib.unified_diff(have.splitlines(), text.splitlines(), "THIRD_PARTY_NOTICES", "generated", n=1, lineterm="")
        sys.stderr.write("\n".join(list(diff)[:60]) + "\nTHIRD_PARTY_NOTICES is not current: run make notices and commit it\n")
        return 1
    OUT.write_text(text)
    return 0


if __name__ == "__main__":
    sys.exit(main())
