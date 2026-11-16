#!/usr/bin/env python3
"""Generate/check the Switch SVG variants; optionally export PNGs with CairoSVG.

The committed master contains outlined lettering. Normal generation and checking
need only Python's standard library, not fonts, a browser, or a network request.
"""
from __future__ import annotations

import argparse
import copy
from pathlib import Path
import re
import sys
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[1]
ASSETS = ROOT / "web/public/brand"
NS = "http://www.w3.org/2000/svg"
ET.register_namespace("", NS)
FOREST, SAGE, ORANGE = "#263D35", "#EEF1E9", "#D26B3F"
DESCRIPTION = ("The Switch: parallel routes form a C with a separate "
               "right-leaning parallelogram. This is brand artwork, not status.")


def find_id(root: ET.Element, identity: str) -> ET.Element:
    for element in root.iter():
        if element.get("id") == identity:
            return element
    raise ValueError(f"Missing SVG element: {identity}")


def svg(width: int, height: int) -> ET.Element:
    root = ET.Element(f"{{{NS}}}svg", {
        "width": str(width), "height": str(height),
        "viewBox": f"0 0 {width} {height}", "role": "img",
        "aria-labelledby": "logo-title logo-desc",
    })
    ET.SubElement(root, f"{{{NS}}}title", {"id": "logo-title"}).text = "Conductor"
    ET.SubElement(root, f"{{{NS}}}desc", {"id": "logo-desc"}).text = DESCRIPTION
    return root


def encode(root: ET.Element) -> str:
    ET.indent(root, space="  ")
    return ET.tostring(root, encoding="unicode") + "\n"


def variants(master: ET.Element) -> dict[str, str]:
    output: dict[str, str] = {}
    for name, color, accent in (
        ("conductor-logo-reversed.svg", SAGE, ORANGE),
        ("conductor-logo-mono.svg", "currentColor", "currentColor"),
    ):
        image = copy.deepcopy(master)
        find_id(image, "logo-desc").text = DESCRIPTION
        for identity in ("routes", "wordmark"):
            find_id(image, identity).set("fill", color)
        find_id(image, "junction").set("fill", accent)
        output[name] = encode(image)
    for name, color in (("conductor-mark.svg", FOREST),
                        ("conductor-mark-reversed.svg", SAGE)):
        image = svg(256, 256)
        mark = copy.deepcopy(find_id(master, "mark"))
        mark.attrib.pop("transform", None)
        find_id(mark, "routes").set("fill", color)
        image.append(mark)
        output[name] = encode(image)
    image = svg(256, 256)
    ET.SubElement(image, f"{{{NS}}}rect", {
        "width": "256", "height": "256", "fill": FOREST,
    })
    mark = copy.deepcopy(find_id(master, "mark"))
    mark.set("transform", "translate(10 10) scale(0.9)")
    find_id(mark, "routes").set("fill", SAGE)
    image.append(mark)
    output["conductor-app-icon.svg"] = encode(image)

    # Optical 16–24 px master: one heavier route and a larger separation.
    # An opaque tile works against light and dark browser chrome.
    image = svg(256, 256)
    find_id(image, "logo-desc").text = (
        "Small-size Switch: a single heavier C and a separate right-leaning "
        "terracotta parallelogram on forest.")
    ET.SubElement(image, f"{{{NS}}}rect", {
        "width": "256", "height": "256", "rx": "48", "fill": FOREST,
    })
    ET.SubElement(image, f"{{{NS}}}path", {
        "id": "routes", "fill": SAGE,
        "d": "M191 36H124A92 92 0 0 0 124 220H204A12 12 0 0 0 204 196H124A68 68 0 0 1 124 60H167Z",
    })
    ET.SubElement(image, f"{{{NS}}}path", {
        "id": "junction", "fill": ORANGE,
        "d": "M205 36H245L219 62H179Z",
    })
    output["conductor-favicon.svg"] = encode(image)
    return output


def luminance(color: str) -> float:
    channels = [int(color[i:i + 2], 16) / 255 for i in (1, 3, 5)]
    linear = [x / 12.92 if x <= 0.04045 else ((x + 0.055) / 1.055) ** 2.4
              for x in channels]
    return sum(x * w for x, w in zip(linear, (0.2126, 0.7152, 0.0722)))


def validate() -> None:
    allowed = {"svg", "title", "desc", "g", "path", "rect"}
    colors = {FOREST, SAGE, ORANGE, "currentColor"}
    for path in sorted(ASSETS.glob("*.svg")):
        text = path.read_text(encoding="utf-8")
        if "<!DOCTYPE" in text.upper() or "<!ENTITY" in text.upper():
            raise ValueError(f"{path.name}: document declarations forbidden")
        image = ET.fromstring(text)
        if image.tag != f"{{{NS}}}svg" or not image.get("viewBox"):
            raise ValueError(f"{path.name}: missing SVG namespace/viewBox")
        ids: set[str] = set()
        for element in image.iter():
            if element.tag.removeprefix(f"{{{NS}}}") not in allowed:
                raise ValueError(f"{path.name}: unsupported element {element.tag}")
            for key, value in element.attrib.items():
                if key.lower().startswith("on") or "href" in key.lower() or "url(" in value.lower():
                    raise ValueError(f"{path.name}: active/external content forbidden")
                if key == "fill" and value not in colors:
                    raise ValueError(f"{path.name}: unexpected color {value}")
            identity = element.get("id")
            if identity:
                if identity in ids:
                    raise ValueError(f"{path.name}: duplicate ID {identity}")
                ids.add(identity)
        labels = image.get("aria-labelledby", "").split()
        if not labels or any(label not in ids for label in labels):
            raise ValueError(f"{path.name}: broken accessible labels")
        junction = find_id(image, "junction")
        if junction.tag != f"{{{NS}}}path" or not junction.get("d", "").endswith("Z"):
            raise ValueError(f"{path.name}: missing closed parallelogram")
    css = (ASSETS / "tokens.css").read_text(encoding="utf-8")
    # The brand's own tokens only: the switchyard's block after them sets the same names to its zinc values.
    css = css.split("[data-conductor-theme", 1)[0]
    tokens = dict(re.findall(r"(--[\w-]+):\s*(#[0-9A-Fa-f]{6});", css))
    for front, back in (("--conductor-text", "--conductor-surface"),
                        ("--conductor-muted", "--conductor-surface"),
                        ("--conductor-accent-text", "--conductor-surface"),
                        ("--conductor-on-action", "--conductor-action"),
                        ("--conductor-dark-text", "--conductor-dark-panel"),
                        ("--conductor-dark-muted", "--conductor-dark-panel")):
        a, b = sorted((luminance(tokens[front]), luminance(tokens[back])))
        ratio = (b + 0.05) / (a + 0.05)
        if ratio < 4.5:
            raise ValueError(f"{front}/{back}: {ratio:.2f} below project text target 4.5")
        print(f"contrast {front}/{back}: {ratio:.2f}:1")
    entry = (ROOT / "web/nuxt.config.ts").read_text(encoding="utf-8")
    if "'/brand/conductor-favicon.svg'" not in entry:
        raise ValueError("Web favicon not wired")
    specimen = (ASSETS / "index.html").read_text(encoding="utf-8")
    for relative in re.findall(r'(?:src|href)="([^"#]+)"', specimen):
        target = (ASSETS / relative).resolve()
        if not target.is_relative_to(ASSETS) or not target.is_file():
            raise ValueError(f"Invalid/external specimen reference: {relative}")
    print("SVG content, palette, text contrast, favicon and specimen references passed")


def export_desktop_icons(destination: Path) -> None:
    """The desktop app's icons: the full-bleed app icon rendered with ImageMagick (electron-builder makes icns and ico from a PNG)."""
    import subprocess
    destination.mkdir(parents=True, exist_ok=True)
    source = ASSETS / "conductor-app-icon.svg"
    for name, size in (("icon.png", 1024), ("icon-512.png", 512), ("tray.png", 64)):
        output = destination / name
        subprocess.run(["convert", "-background", "none", "-density", "384", str(source), "-resize", f"{size}x{size}", str(output)], check=True)
        print(f"wrote {output}")


def export_pngs(destination: Path) -> None:
    import cairosvg  # Optional export-only dependency; failures are reported below.
    destination.mkdir(parents=True, exist_ok=True)
    for path in sorted(ASSETS.glob("*.svg")):
        sizes = [16, 24, 32, 48] if path.stem == "conductor-favicon" else [512]
        if path.stem == "conductor-app-icon":
            sizes = [180, 512, 1024]
        if path.stem.startswith("conductor-logo"):
            sizes = [1600]
        for size in sizes:
            output = destination / f"{path.stem}-{size}.png"
            cairosvg.svg2png(url=str(path), write_to=str(output), output_width=size)
            print(f"exported {output.name}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="Check without modifying SVGs")
    parser.add_argument("--png-dir", type=Path, help="Optional PNG export destination")
    parser.add_argument("--desktop-icons", type=Path, help="Write the desktop app's icons (icon.png 1024, icon-512.png) from the app icon, with ImageMagick")
    args = parser.parse_args()
    if args.desktop_icons:
        export_desktop_icons(args.desktop_icons)
        return 0
    try:
        master = ET.fromstring((ASSETS / "conductor-logo.svg").read_text(encoding="utf-8"))
        for name, content in variants(master).items():
            target = ASSETS / name
            if args.check:
                if not target.is_file() or target.read_text(encoding="utf-8") != content:
                    raise ValueError(f"{name} missing/stale; run scripts/brand_assets.py")
            else:
                target.write_text(content, encoding="utf-8")
        validate()
        if args.png_dir:
            export_pngs(args.png_dir)
    except ModuleNotFoundError as exc:
        print(f"PNG export requires optional CairoSVG: {exc}", file=sys.stderr)
        return 1
    except (ValueError, OSError, ET.ParseError, KeyError) as exc:
        print(f"Brand assets: {exc}", file=sys.stderr)
        return 1
    print("Brand assets are consistent with the master")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
