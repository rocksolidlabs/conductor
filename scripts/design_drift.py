#!/usr/bin/env python3
"""What drifted in the design since the last release (the UX review's PR-38).

`make design-capture` drives the built app on the seeded day through every
page, in both themes and at both sizes (web/e2e/look.spec.ts), and writes
the values the pages computed to web/test-results/look/look.json. This
script reads that file, the components (web/app/components/*.vue) and the
rules of docs/design/system.md with their marks, and compares them with the
baseline committed at the last release, docs/design/system.json. It prints
each drift on a line, or "Nothing drifted.":

    token `--ui-text-muted` (light): #666e67 to #5f6760
    status colour (needs, dark): #f0a020 to #e09010
    status word (finished): finished to done
    component added: `IconButton`
    page gone: `roundhouse`
    rule Toolbar: Described to Decided

    python3 scripts/design_drift.py            # the report; exits 0
    python3 scripts/design_drift.py --check    # exits 1 on any drift
    python3 scripts/design_drift.py --write    # the baseline rewritten from what is here now, keys sorted

It is a report, not a gate: CI does not run it. The baseline holds values,
never pictures; the screens stay in web/test-results/look/. Only Python's
standard library.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parent.parent
LOOK = Path("web/test-results/look/look.json")
BASELINE = Path("docs/design/system.json")
COMPONENTS = Path("web/app/components")
SYSTEM_MD = Path("docs/design/system.md")

_DECIDED = re.compile(r"^Decided (\d{4}-\d{2}-\d{2}) by the maintainers, #(\d+)\.$")
_DESCRIBED = re.compile(r"^Described, not decided\.$")


def rules(md: str) -> dict[str, str]:
    """Each `###` rule under `## The rules` with its mark: "Decided 2026-11-16, #927", "Described", or "no mark"."""
    part = re.split(r"^## The rules\s*$", md, maxsplit=1, flags=re.M)
    if len(part) < 2:
        return {}
    body = re.split(r"^## (?!#)", part[1], maxsplit=1, flags=re.M)[0]
    out: dict[str, str] = {}
    for chunk in re.split(r"^### ", body, flags=re.M)[1:]:
        heading, _, text = chunk.partition("\n")
        mark = "no mark"
        for line in text.splitlines():
            m = _DECIDED.match(line.strip())
            if m:
                mark = f"Decided {m.group(1)}, #{m.group(2)}"
                break
            if _DESCRIBED.match(line.strip()):
                mark = "Described"
                break
        out[heading.strip()] = mark
    return out


def components(folder: Path) -> list[str]:
    """The app's components by name: the .vue files directly in the folder."""
    return sorted(p.stem for p in folder.glob("*.vue"))


def current(look: dict, folder: Path, md: str) -> dict:
    """What is here now, in the baseline's shape: the capture's values, the components and the rules."""
    return {
        "components": components(folder),
        "contrast": look.get("contrast", []),
        "pages": sorted(look.get("pages", [])),
        "rules": rules(md),
        "status": look.get("status", {}),
        "tokens": look.get("tokens", {}),
    }


def _kind(mark: str) -> str:
    return mark.split(" ", 1)[0]


def drift(old: dict, new: dict) -> list[str]:
    """Every difference between the baseline and now, one line each, in a fixed order."""
    out: list[str] = []
    for theme in sorted(set(old.get("tokens", {})) | set(new.get("tokens", {}))):
        a, b = old.get("tokens", {}).get(theme, {}), new.get("tokens", {}).get(theme, {})
        for name in sorted(set(a) | set(b)):
            if name not in a:
                out.append(f"token added `{name}` ({theme}): {b[name]}")
            elif name not in b:
                out.append(f"token gone `{name}` ({theme}): {a[name]}")
            elif a[name] != b[name]:
                out.append(f"token `{name}` ({theme}): {a[name]} to {b[name]}")
    sa, sb = old.get("status", {}), new.get("status", {})
    for key in sorted(set(sa) | set(sb)):
        if key not in sa:
            out.append(f"status added: {key}")
            continue
        if key not in sb:
            out.append(f"status gone: {key}")
            continue
        wa, wb = sa[key].get("words", []), sb[key].get("words", [])
        gone, added = [w for w in wa if w not in wb], [w for w in wb if w not in wa]
        if len(gone) == 1 and len(added) == 1:
            out.append(f"status word ({key}): {gone[0]} to {added[0]}")
        else:
            out += [f"status word gone ({key}): {w}" for w in gone] + [f"status word added ({key}): {w}" for w in added]
        ca, cb = sa[key].get("colour", {}), sb[key].get("colour", {})
        for theme in sorted(set(ca) | set(cb)):
            if ca.get(theme) != cb.get(theme):
                out.append(f"status colour ({key}, {theme}): {ca.get(theme, 'none')} to {cb.get(theme, 'none')}")
    for what in ("components", "pages"):
        a, b = set(old.get(what, [])), set(new.get(what, []))
        word = what[:-1]
        out += [f"{word} added: `{n}`" for n in sorted(b - a)] + [f"{word} gone: `{n}`" for n in sorted(a - b)]
    ra, rb = old.get("rules", {}), new.get("rules", {})
    for h in sorted(set(ra) | set(rb)):
        if h not in ra:
            out.append(f"rule added: {h} ({rb[h]})")
        elif h not in rb:
            out.append(f"rule gone: {h}")
        elif ra[h] != rb[h]:
            same = _kind(ra[h]) == _kind(rb[h])
            out.append(f"rule {h}: {ra[h] if same else _kind(ra[h])} to {rb[h] if same else _kind(rb[h])}")
    return out


def dump(data: dict) -> str:
    return json.dumps(data, indent=2, sort_keys=True, ensure_ascii=False) + "\n"


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="What drifted in the design since the baseline (docs/design/system.json).")
    ap.add_argument("--root", type=Path, default=ROOT, help="the repository (the paths below are under it)")
    ap.add_argument("--look", type=Path, default=LOOK, help="the capture's values (make design-capture)")
    ap.add_argument("--baseline", type=Path, default=BASELINE)
    ap.add_argument("--components", type=Path, default=COMPONENTS)
    ap.add_argument("--system", type=Path, default=SYSTEM_MD)
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="exit 1 when anything drifted")
    mode.add_argument("--write", action="store_true", help="rewrite the baseline from what is here now")
    a = ap.parse_args(argv)
    at = lambda p: p if p.is_absolute() else a.root / p  # noqa: E731
    look_path, base_path = at(a.look), at(a.baseline)
    if not look_path.is_file():
        print(f"design_drift: no capture at {look_path}: run `make design-capture` first", file=sys.stderr)
        return 2
    now = current(json.loads(look_path.read_text(encoding="utf-8")), at(a.components), at(a.system).read_text(encoding="utf-8"))
    if a.write:
        base_path.write_text(dump(now), encoding="utf-8")
        print(f"design_drift: wrote {base_path}")
        return 0
    if not base_path.is_file():
        print(f"design_drift: no baseline at {base_path}: write one with --write", file=sys.stderr)
        return 1 if a.check else 0
    lines = drift(json.loads(base_path.read_text(encoding="utf-8")), now)
    print("\n".join(lines) if lines else "Nothing drifted.")
    return 1 if a.check and lines else 0


if __name__ == "__main__":
    sys.exit(main())
