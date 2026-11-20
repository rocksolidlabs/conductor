#!/usr/bin/env python3
"""Check the phone shell's iOS project against what it is held to (docs/plans/phone-shell.md, OSS-1a).

What a build in Xcode would not refuse but the shell must not ship, read from the files themselves:

- Info.plist: the Face ID words, no other usage description (no microphone,
  camera, location, ...), no background mode.
- The Xcode project: no team, no provisioning profile, no entitlements; iOS 16
  in every configuration; the development app id alone; every Swift file of
  the app in the app's sources; the native package (../ConductorNative) a
  local package whose ConductorShell product the app links.
- No entitlements or provisioning profile anywhere under mobile/ios.
- capacitor.config.ts: the development app id and navigation allowed to
  nothing but the bundle.
- The native runtime's package list (CapApp-SPM/Package.swift, written by
  `cap sync ios`) at the version mobile/package-lock.json pins, with each of
  the shell's plugin packages.
- The plugins: the names the bridge calls (mobile/src/bridge.ts, `NativeKey`
  and `NativeStore`; mobile/src/main.ts reads them as `ConductorKey` and
  `ConductorStore`) are exactly the calls ConductorShell answers and exactly
  the methods each plugin declares and has; the bridge view controller
  registers both and wraps the web view's UI delegate in MediaCaptureRefused,
  and it is the controller the scene's window and the main storyboard both
  give the window.

    python3 scripts/ios_project.py   # exit 1 with each finding

Only Python's standard library.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import plistlib
import re
import sys

ROOT = Path(__file__).resolve().parents[1]
APP_ID = "dev.conductor.mobile"
TARGET = "16.0"
FACE_ID = "Conductor asks for Face ID to confirm an answer you send."
# The shell's plugin packages with an iOS part: each must be in the runtime's package list.
IOS_PLUGINS = {"@capacitor/app": "CapacitorApp", "@capacitor/browser": "CapacitorBrowser", "@capacitor/push-notifications": "CapacitorPushNotifications"}
# Each plugin: its file, the name the page reaches it by, and the interface of the bridge and the list of ConductorShell it answers.
PLUGINS = [
    ("ConductorKeyPlugin.swift", "ConductorKey", "NativeKey", "key"),
    ("ConductorStorePlugin.swift", "ConductorStore", "NativeStore", "store"),
]


def info_plist(mobile: Path) -> list[str]:
    out = []
    p = mobile / "ios/App/App/Info.plist"
    with p.open("rb") as f:
        info = plistlib.load(f)
    if info.get("NSFaceIDUsageDescription") != FACE_ID:
        out.append(f"Info.plist: NSFaceIDUsageDescription is not {FACE_ID!r}")
    for k in sorted(info):
        if re.fullmatch(r"NS\w*UsageDescription", k) and k != "NSFaceIDUsageDescription":
            out.append(f"Info.plist: {k}: the shell asks for nothing but Face ID")
    if "UIBackgroundModes" in info:
        out.append("Info.plist: UIBackgroundModes: the shell has no background mode")
    return out


def project(mobile: Path) -> list[str]:
    out = []
    app = mobile / "ios/App"
    pbx = (app / "App.xcodeproj/project.pbxproj").read_text()
    for setting in ("DEVELOPMENT_TEAM", "PROVISIONING_PROFILE", "PROVISIONING_PROFILE_SPECIFIER", "CODE_SIGN_ENTITLEMENTS"):
        if re.search(rf"\b{setting}\s*=", pbx):
            out.append(f"project.pbxproj: {setting} is set: signing is the packaging's, never the repository's")
    targets = re.findall(r"IPHONEOS_DEPLOYMENT_TARGET = ([^;]+);", pbx)
    if not targets or any(t != TARGET for t in targets):
        out.append(f"project.pbxproj: IPHONEOS_DEPLOYMENT_TARGET is {sorted(set(targets))}, not {TARGET} in every configuration")
    ids = re.findall(r"PRODUCT_BUNDLE_IDENTIFIER = ([^;]+);", pbx)
    if not ids or any(i != APP_ID for i in ids):
        out.append(f"project.pbxproj: PRODUCT_BUNDLE_IDENTIFIER is {sorted(set(ids))}, not {APP_ID}")
    for f in sorted((app / "App").rglob("*.swift")):
        name = f.name
        if not re.search(rf"/\* {re.escape(name)} \*/ = {{isa = PBXFileReference;[^}}]*path = {re.escape(name)};", pbx):
            out.append(f"project.pbxproj: {f.relative_to(app)} is not in the project")
        elif f"/* {name} in Sources */" not in pbx.split("/* Begin PBXSourcesBuildPhase section */")[-1].split("/* End PBXSourcesBuildPhase section */")[0]:
            out.append(f"project.pbxproj: {f.relative_to(app)} is not in the app's sources")
    local = re.search(r"(\w{24}) /\* XCLocalSwiftPackageReference \"[^\"]*\" \*/ = \{\s*isa = XCLocalSwiftPackageReference;\s*relativePath = \.\./ConductorNative;", pbx)
    if not local:
        out.append("project.pbxproj: ../ConductorNative is not a local package of the project")
    elif not re.search(rf"isa = XCSwiftPackageProductDependency;\s*package = {local.group(1)} [^;]*;\s*productName = ConductorShell;", pbx):
        out.append("project.pbxproj: the app does not link ConductorShell from ../ConductorNative")
    for f in sorted((mobile / "ios").rglob("*")):
        if ".build" in f.parts or "node_modules" in f.parts:
            continue
        if f.suffix in (".entitlements", ".mobileprovision", ".p12", ".cer"):
            out.append(f"{f.relative_to(mobile)}: signing is the packaging's, never the repository's")
    return out


def config(mobile: Path) -> list[str]:
    out = []
    text = (mobile / "capacitor.config.ts").read_text()
    if not re.search(rf"appId:\s*'{re.escape(APP_ID)}'", text):
        out.append(f"capacitor.config.ts: the app id is not {APP_ID}")
    if not re.search(r"allowNavigation:\s*\[\s*\]", text):
        out.append("capacitor.config.ts: navigation inside the web view is allowed to more than the bundle (server.allowNavigation not empty)")
    lock = json.loads((mobile / "package-lock.json").read_text())["packages"]
    spm = (mobile / "ios/App/CapApp-SPM/Package.swift").read_text()
    ios = lock.get("node_modules/@capacitor/ios", {}).get("version")
    m = re.search(r'capacitor-swift-pm\.git", exact: "([^"]+)"', spm)
    if not m or m.group(1) != ios:
        out.append(f"CapApp-SPM/Package.swift: capacitor-swift-pm is {m.group(1) if m else 'missing'}, not @capacitor/ios {ios}: run npx cap sync ios")
    for pkg, product in IOS_PLUGINS.items():
        if f'.package(name: "{product}", path: "../../../node_modules/{pkg}")' not in spm:
            out.append(f"CapApp-SPM/Package.swift: {pkg} is not in the native runtime's packages: run npx cap sync ios")
    return out


def interface(bridge: str, name: str) -> list[str]:
    m = re.search(rf"export interface {name} \{{(.*?)\n\}}", bridge, re.S)
    return re.findall(r"^\s+(\w+)\(", m.group(1), re.M) if m else []


def swift_list(calls: str, name: str) -> list[str]:
    m = re.search(rf"static let {name} = \[([^\]]*)\]", calls)
    return re.findall(r'"(\w+)"', m.group(1)) if m else []


def plugins(mobile: Path) -> list[str]:
    out = []
    bridge = (mobile / "src/bridge.ts").read_text()
    main = (mobile / "src/main.ts").read_text()
    calls = (mobile / "ios/ConductorNative/Sources/ConductorShell/Calls.swift").read_text()
    shell = (mobile / "ios/App/App/ShellViewController.swift").read_text()
    for file, js, iface, kind in PLUGINS:
        want = interface(bridge, iface)
        if not want:
            out.append(f"bridge.ts: no interface {iface}")
            continue
        if f"p.{js}" not in main:
            out.append(f"main.ts: the bridge does not read the plugin {js}")
        if sorted(swift_list(calls, kind)) != sorted(want):
            out.append(f"Calls.swift: PluginMethods.{kind} is {swift_list(calls, kind)}, not the bridge's {iface} {want}")
        text = (mobile / "ios/App/App/Plugins" / file).read_text()
        cls = file.removesuffix(".swift")
        if f'jsName = "{js}"' not in text:
            out.append(f"{file}: jsName is not {js}")
        declared = re.findall(r'CAPPluginMethod\(name: "(\w+)", returnType: CAPPluginReturnPromise\)', text)
        if sorted(declared) != sorted(want):
            out.append(f"{file}: declares {declared}, not the bridge's {iface} {want}")
        made = re.findall(r'@objc func (\w+)\(_ call: CAPPluginCall\) \{ make\(call, "(\w+)"\) \}', text)
        if sorted(made) != sorted((n, n) for n in want):
            out.append(f"{file}: its methods {made} do not each make the call of their own name for {want}")
        if f"registerPluginInstance({cls}())" not in shell:
            out.append(f"ShellViewController.swift: {cls} is not registered")
    if not re.search(r"MediaCaptureRefused\(wrapping: webView\?\.uiDelegate", shell) or "webView?.uiDelegate = " not in shell:
        out.append("ShellViewController.swift: the web view's UI delegate is not wrapped in MediaCaptureRefused")
    # Every way the window gets its controller gives it the shell's: the scene's own window and the storyboard the scene names.
    scene = (mobile / "ios/App/App/SceneDelegate.swift").read_text()
    if "rootViewController = ShellViewController()" not in scene:
        out.append("SceneDelegate.swift: the window's controller is not the ShellViewController")
    board = (mobile / "ios/App/App/Base.lproj/Main.storyboard").read_text()
    classes = re.findall(r'<viewController [^>]*customClass="(\w+)"', board)
    if classes != ["ShellViewController"]:
        out.append(f"Main.storyboard: its controller is {classes}, not the ShellViewController")
    return out


def check(root: Path = ROOT) -> list[str]:
    mobile = root / "mobile"
    return info_plist(mobile) + project(mobile) + config(mobile) + plugins(mobile)


def main() -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n\n")[0])
    ap.add_argument("--root", type=Path, default=ROOT, help="the repository (default: this one)")
    findings = check(ap.parse_args().root)
    for f in findings:
        print(f"ios_project: {f}", file=sys.stderr)
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
