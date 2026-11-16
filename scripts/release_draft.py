#!/usr/bin/env python3
"""The one draft release a version's packages go into (release.yml, #372, #375).

electron-builder, publishing, uploads to the tag's draft release and makes
one when it finds none, and it looks at the first page of releases only. Two
package jobs that found none at the same moment each made a draft, and the
packages were split over two (#372). So electron-builder only builds; the
release is made once, before the package jobs start, each job uploads its
packages to it by its id, and the end of the workflow checks that the tag has
that one draft with every package on it.

The tag does not exist until the release is published (#375): the updater
reads the repository's releases.atom, which lists a pushed tag even while its
release is a draft, and an app whose channel's newest entry is a draft finds
no latest.yml there and no update at all. So the draft names a tag that does
not exist yet and the commit built (target_commitish); GitHub makes the tag
at that commit when the draft is published.

    python3 scripts/release_draft.py ensure v0.7.0-rc.18 --target <sha>
    python3 scripts/release_draft.py upload v0.7.0-rc.18 desktop/release --target <sha>
    python3 scripts/release_draft.py check v0.7.0-rc.18 packages/ --target <sha>

`ensure` makes the tag's draft as electron-builder would (named the version,
a draft, not a prerelease) at the commit, or keeps the one a run before this
made for the same commit; it refuses a tag that exists, a release that is
published, more than one, or a draft for another commit or under another
spelling of the tag. `upload` puts a build's packages (installers, their
blockmaps, the latest*.yml the updater reads) on that draft, replacing one a
failed run left, after the same refusals; it never makes a release. `check`
refuses the same, a package in the directory (the package jobs' artifacts)
the draft does not have, an installer without its blockmap, and a draft
without the updater's three latest*.yml.

The draft `ensure` makes carries a body: CHECKLIST, the by-hand steps of a
release, among them the design capture (`make design-capture`, the UX
review's PR-38). The maintainer works through it and deletes it from the
body when publishing; a draft kept from an earlier run keeps its body, and
no script edits a published release.

A package's file name is its name on the release: the Windows installer is
named without spaces (electron-builder.yml), as electron-builder would have
published it.

Reads and writes the repository in GITHUB_REPOSITORY through `gh api`
(GH_TOKEN); only Python's standard library otherwise.
"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import urllib.parse

# What electron-builder publishes for the targets in electron-builder.yml.
PACKAGE_SUFFIXES = (".dmg", ".zip", ".deb", ".rpm", ".AppImage", ".exe", ".blockmap")
# The installers whose differential updates read a blockmap beside them.
BLOCKMAPPED = (".dmg", ".zip", ".exe")
# What the updater reads on Windows, macOS and the AppImage: a release without one updates no app there.
UPDATER_FILES = ("latest.yml", "latest-mac.yml", "latest-linux.yml")
# A version is SemVer without build metadata, as electron-builder and electron-updater read it: no leading
# zero in a number, no empty prerelease part. Nothing a shell, a URL or git's ref rules would read otherwise.
_NUMBER = r"(?:0|[1-9][0-9]*)"
_PART = r"(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
VERSION = re.compile(rf"{_NUMBER}\.{_NUMBER}\.{_NUMBER}(?:-{_PART}(?:\.{_PART})*)?")
COMMIT = re.compile(r"[0-9a-f]{40}")


def version(tag: str) -> str:
    """The package version of a tag: the tag without its v (electron-builder's
    release name)."""
    return tag[1:] if tag.startswith("v") else tag


def valid(tag: str, target: str) -> None:
    """Raises ValueError unless tag is v and a version, and target a full commit id."""
    if not (tag.startswith("v") and VERSION.fullmatch(tag[1:])):
        raise ValueError(f"{tag!r} is not v and a version (0.7.0-rc.18: numbers without leading zeros, no empty part)")
    if not COMMIT.fullmatch(target):
        raise ValueError(f"{target!r} is not a full commit id")


def of_tag(releases: list[dict], tag: str) -> list[dict]:
    """The releases electron-builder takes for the tag: tagged with it, or with
    its version."""
    return [r for r in releases if r.get("tag_name") in (tag, version(tag))]


def plan(releases: list[dict], tag: str, target: str) -> tuple[str, int | None]:
    """What ensure does: ("create", None), or ("keep", id) of the draft a run
    before this made for the same commit. Raises ValueError when the tag's
    releases are not something to upload to."""
    mine = of_tag(releases, tag)
    published = [r for r in mine if not r.get("draft")]
    if published:
        raise ValueError(f"{tag} has a published release ({published[0]['id']}): packages are not built into it again")
    if len(mine) > 1:
        ids = ", ".join(str(r["id"]) for r in mine)
        raise ValueError(f"{tag} has {len(mine)} draft releases ({ids}): put their assets on one and delete the others")
    if not mine:
        return "create", None
    r = mine[0]
    if r.get("tag_name") != tag:
        raise ValueError(f"{tag}'s draft release {r['id']} is tagged {r.get('tag_name')}: delete it, or release under that tag")
    # A second build of the version from another commit would put two builds' packages on one draft.
    if r.get("target_commitish") != target:
        raise ValueError(f"{tag}'s draft release {r['id']} is for {r.get('target_commitish')}, not {target}: delete it, or release another version")
    return "keep", r["id"]


def draft(releases: list[dict], tag: str, target: str) -> dict:
    """The tag's one draft for target, which upload puts packages on. Raises
    ValueError when there is not exactly one: upload never makes a release."""
    what, _ = plan(releases, tag, target)
    if what == "create":
        raise ValueError(f"{tag} has no draft release: the release job makes it")
    return of_tag(releases, tag)[0]


def is_package(name: str) -> bool:
    return name.endswith(PACKAGE_SUFFIXES) or (name.startswith("latest") and name.endswith(".yml"))


def built(folder: Path) -> list[str]:
    """The packages at the top of a folder (a build's output, or the package
    jobs' artifacts), by name: what electron-builder publishes, without its
    logs, its effective config or the unpacked apps beneath."""
    return sorted(p.name for p in folder.iterdir() if p.is_file() and is_package(p.name))


def problems(releases: list[dict], tag: str, target: str, packages: list[str], tag_exists: bool) -> list[str]:
    """What check finds wrong: a tag that exists before its release is
    published, the tag's releases other than one draft for target, the
    packages that draft does not have, installers on it without their
    blockmap, and the updater's files it lacks."""
    out = [f"{tag} exists before its release is published: the updater lists it (#375)"] if tag_exists else []
    mine = of_tag(releases, tag)
    if len(mine) != 1:
        ids = ", ".join(str(r["id"]) for r in mine) or "none"
        return out + [f"{tag} has {len(mine)} releases ({ids}), not one draft"]
    r = mine[0]
    if not r.get("draft"):
        out.append(f"{tag}'s release {r['id']} is not a draft")
    if r.get("tag_name") != tag:
        out.append(f"{tag}'s release {r['id']} is tagged {r.get('tag_name')}")
    if r.get("target_commitish") != target:
        out.append(f"{tag}'s release {r['id']} is for {r.get('target_commitish')}, not {target}")
    have = {a["name"] for a in r.get("assets", []) if a.get("state", "uploaded") == "uploaded"}
    out += [f"{name} is not on {tag}'s release {r['id']}" for name in sorted((set(packages) | set(UPDATER_FILES)) - have)]
    out += [f"{name} has no {name}.blockmap on {tag}'s release {r['id']}"
            for name in sorted(have | set(packages)) if name.endswith(BLOCKMAPPED) and f"{name}.blockmap" not in have]
    return out


def gh(*args: str) -> str:
    # gh writes UTF-8; text=True alone decodes with the locale's (cp1252 on the Windows runner).
    return subprocess.run(["gh", *args], check=True, capture_output=True, encoding="utf-8").stdout


def releases(repo: str) -> list[dict]:
    # One release per line, every page, with only what is read here (no notes); drafts are listed to
    # a token that may push.
    out = gh("api", "--paginate", f"repos/{repo}/releases?per_page=100", "--jq",
             ".[] | {id, tag_name, target_commitish, draft, assets: [.assets[] | {name, state}]}")
    return [json.loads(line) for line in out.splitlines() if line.strip()]


def tag_exists(repo: str, tag: str) -> bool:
    # matching-refs is a prefix match (v1.0.0 lists v1.0.0-rc.1 too) and answers [] for none, never a 404.
    out = gh("api", f"repos/{repo}/git/matching-refs/tags/{tag}", "--jq", ".[].ref")
    return f"refs/tags/{tag}" in out.splitlines()


def assets(repo: str, rid: int) -> dict[str, int]:
    out = gh("api", "--paginate", f"repos/{repo}/releases/{rid}/assets?per_page=100", "--jq", ".[]")
    return {a["name"]: a["id"] for a in (json.loads(line) for line in out.splitlines() if line.strip())}


def upload(repo: str, rid: int, path: Path, tries: int = 3) -> None:
    """Puts path on release rid under its own name, in place of an asset of
    that name (a failed run's, whole or cut short)."""
    for attempt in range(1, tries + 1):
        old = assets(repo, rid).get(path.name)
        if old is not None:
            gh("api", "-X", "DELETE", f"repos/{repo}/releases/assets/{old}")
        url = f"https://uploads.github.com/repos/{repo}/releases/{rid}/assets?name={urllib.parse.quote(path.name)}"
        try:
            gh("api", "-X", "POST", url, "-H", "Content-Type: application/octet-stream", "--input", str(path))
            return
        except subprocess.CalledProcessError as err:
            print(f"release_draft: {path.name}, try {attempt} of {tries}: {err.stderr.strip()}", file=sys.stderr)
            if attempt == tries:
                raise


# The body of a draft `ensure` makes: the steps a maintainer takes by hand before publishing, then deletes.
CHECKLIST = """Before publishing (delete this checklist from the notes when done):

- [ ] Run `make design-capture` at the release commit, read what drifted, and commit `docs/design/system.json` with `--write` (`python3 scripts/design_drift.py --write`).
- [ ] Publish the capture's screens (`web/test-results/look/`) to the design-system page.
- [ ] Write the release's notes here.
"""


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="The one draft release a tag's packages go into.")
    sub = ap.add_subparsers(dest="cmd", required=True)
    e = sub.add_parser("ensure", help="make the tag's draft release at the commit, or keep the one there")
    e.add_argument("tag")
    u = sub.add_parser("upload", help="put a build's packages on the tag's draft release")
    u.add_argument("tag")
    u.add_argument("packages", type=Path)
    c = sub.add_parser("check", help="the tag has one draft holding every package built")
    c.add_argument("tag")
    c.add_argument("packages", type=Path)
    for p in (e, u, c):
        p.add_argument("--target", required=True, help="the full id of the commit built, where publishing makes the tag")
    a = ap.parse_args(argv)
    repo = os.environ.get("GITHUB_REPOSITORY", "rocksolidlabs/conductor")
    try:
        valid(a.tag, a.target)
        if a.cmd != "check" and tag_exists(repo, a.tag):
            raise ValueError(f"{a.tag} exists: the release makes its tag when it is published (#375); "
                             "delete the tag (it is in the updater's feed) or release another version")
        if a.cmd == "ensure":
            what, rid = plan(releases(repo), a.tag, a.target)
            if what == "keep":
                print(f"{a.tag}: keeping draft release {rid}")
                return 0
            made = json.loads(gh("api", f"repos/{repo}/releases", "-f", f"tag_name={a.tag}", "-f", f"target_commitish={a.target}",
                                 "-f", f"name={version(a.tag)}", "-f", f"body={CHECKLIST}", "-F", "draft=true", "-F", "prerelease=false"))
            print(f"{a.tag}: made draft release {made['id']} at {a.target}")
            return 0
        packages = built(a.packages)
        if not packages:
            print(f"release_draft: no packages under {a.packages}", file=sys.stderr)
            return 1
        if a.cmd == "upload":
            rid = draft(releases(repo), a.tag, a.target)["id"]
            for name in packages:
                upload(repo, rid, a.packages / name)
                print(f"{a.tag}: {name} on draft release {rid}")
            return 0
        found = problems(releases(repo), a.tag, a.target, packages, tag_exists(repo, a.tag))
    except ValueError as err:
        print(f"release_draft: {err}", file=sys.stderr)
        return 1
    for p in found:
        print(f"release_draft: {p}", file=sys.stderr)
    if not found:
        print(f"{a.tag}: one draft release with all {len(packages)} packages")
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main())
