#!/usr/bin/env python3
"""Check the phone shell's iOS project against what it is held to (docs/plans/phone-shell.md, OSS-1a).

What a build in Xcode would not refuse but the shell must not ship, read from the files themselves:

- Info.plist: the Face ID words, no other usage description (no microphone,
  camera, location, ..., with the NS prefix or without), no background mode,
  no App Transport Security exception, no URL types or queried schemes, and
  the app's files not shared.
- The Xcode project: no team, no provisioning profile, no entitlements; iOS 16
  in every configuration; the development app id alone (and its UI tests'
  own); a UI test target, the boot check, run by the shared scheme's test
  action; every Swift file of the app and its UI tests in a target's
  sources; the native package (../ConductorNative) a
  local package whose ConductorShell product the app links.
- No entitlements or provisioning profile anywhere under mobile/ios; every
  *.xcconfig there sets no team, profile, entitlements or signing identity,
  includes no other file, and names only the development id and iOS 16;
  every scheme passes the app no argument and no environment.
- capacitor.config.ts: the development app id, navigation allowed to
  nothing but the bundle, and no other key than those (no remote server
  address, no cleartext, no changed scheme or host, no plugin switched on).
- The native runtime's package list (CapApp-SPM/Package.swift, written by
  `cap sync ios`) at the version mobile/package-lock.json pins, with each of
  the shell's plugin packages.
- A packaging's place for its native sources
  (ConductorNative/Sources/ConductorPackaging/Packaging) ships nothing here;
  the app links ConductorPackaging, with -ObjC, and names it at start.
- The plugins: the names the bridge calls (mobile/src/bridge.ts, `NativeKey`
  and `NativeStore`; mobile/src/main.ts reads them as `ConductorKey` and
  `ConductorStore`) are exactly the calls ConductorShell answers and exactly
  the methods each plugin declares and has; the bridge view controller
  registers both and wraps the web view's UI delegate in MediaCaptureRefused,
  replaces the runtime's own WebView, CapacitorCookies and CapacitorHttp by
  their names (two refusing stand-ins and the shell's own HTTP plugin, each
  declaring exactly the list of ConductorShell it answers), and never loads
  a server base path a page asked to keep (its instanceDescriptor),
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
UI_TESTS_ID = APP_ID + ".uitests"
FACE_ID = "Conductor asks for Face ID to confirm an answer you send."
# Keys Info.plist must not have: a transport exception, links the app is opened by or asks about, its files shared.
PLIST_REFUSED = ("UIBackgroundModes", "NSAppTransportSecurity", "CFBundleURLTypes", "LSApplicationQueriesSchemes", "UIFileSharingEnabled", "LSSupportsOpeningDocumentsInPlace")
# Settings no *.xcconfig or project sets: signing is the packaging's.
SIGNING = ("DEVELOPMENT_TEAM", "PROVISIONING_PROFILE", "PROVISIONING_PROFILE_SPECIFIER", "CODE_SIGN_ENTITLEMENTS")
# The keys capacitor.config.ts may have, at any depth: anything else is a change this check has not read.
CONFIG_KEYS = {"appId", "appName", "webDir", "server", "allowNavigation"}
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
        if re.search(r"UsageDescription", k) and k != "NSFaceIDUsageDescription":
            out.append(f"Info.plist: {k}: the shell asks for nothing but Face ID")
    for k in PLIST_REFUSED:
        if k in info:
            out.append(f"Info.plist: {k}: the shell has no background mode, transport exception, URL type, queried scheme or shared files")
    return out


def project(mobile: Path) -> list[str]:
    out = []
    app = mobile / "ios/App"
    pbx = (app / "App.xcodeproj/project.pbxproj").read_text()
    for setting in SIGNING:
        if re.search(rf"\b{setting}\s*=", pbx):
            out.append(f"project.pbxproj: {setting} is set: signing is the packaging's, never the repository's")
    targets = re.findall(r"IPHONEOS_DEPLOYMENT_TARGET = ([^;]+);", pbx)
    if not targets or any(t != TARGET for t in targets):
        out.append(f"project.pbxproj: IPHONEOS_DEPLOYMENT_TARGET is {sorted(set(targets))}, not {TARGET} in every configuration")
    ids = re.findall(r"PRODUCT_BUNDLE_IDENTIFIER = ([^;]+);", pbx)
    if APP_ID not in ids or any(i not in (APP_ID, UI_TESTS_ID) for i in ids):
        out.append(f"project.pbxproj: PRODUCT_BUNDLE_IDENTIFIER is {sorted(set(ids))}, not {APP_ID} (and {UI_TESTS_ID} for the boot check)")
    # The boot check: a UI test target of the app, and the shared scheme's test action runs it.
    if not re.search(r'productType = "com\.apple\.product-type\.bundle\.ui-testing";', pbx) or "TEST_TARGET_NAME = App;" not in pbx:
        out.append("project.pbxproj: no UI test target of the app: the boot check (ShellBootsUnderItsPolicyTests) has nowhere to run")
    scheme = app / "App.xcodeproj/xcshareddata/xcschemes/App.xcscheme"
    testables = scheme.read_text().split("<Testables>", 1)[1].split("</Testables>", 1)[0] if scheme.exists() and "<Testables>" in scheme.read_text() else ""
    if 'BlueprintName = "AppUITests"' not in testables:
        out.append("App.xcscheme: its test action does not run AppUITests, the boot check")
    for f in sorted([*(app / "App").rglob("*.swift"), *(app / "AppUITests").rglob("*.swift")]):
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
        elif f.suffix == ".xcconfig":
            out += xcconfig(f, f.relative_to(mobile))
        elif f.suffix == ".xcscheme":
            text = f.read_text()
            for tag in ("CommandLineArgument", "EnvironmentVariable"):
                if f"<{tag}" in text:
                    out.append(f"{f.relative_to(mobile)}: a {tag}: a scheme passes the app nothing")
    return out


def xcconfig(f: Path, name: Path) -> list[str]:
    out = []
    for line in f.read_text().splitlines():
        line = line.split("//", 1)[0].strip()
        if not line:
            continue
        if line.startswith("#include"):
            out.append(f"{name}: {line}: an xcconfig here includes no other file")
            continue
        m = re.match(r"([A-Za-z_][A-Za-z0-9_]*)(\[[^\]]*\])*\s*=\s*(.*)$", line)
        if not m:
            continue
        key, value = m.group(1), m.group(3).strip()
        if key in SIGNING or key == "CODE_SIGN_IDENTITY":
            out.append(f"{name}: {key} is set: signing is the packaging's, never the repository's")
        elif key == "PRODUCT_BUNDLE_IDENTIFIER" and value != APP_ID:
            out.append(f"{name}: PRODUCT_BUNDLE_IDENTIFIER is {value}, not {APP_ID}")
        elif key == "IPHONEOS_DEPLOYMENT_TARGET" and value != TARGET:
            out.append(f"{name}: IPHONEOS_DEPLOYMENT_TARGET is {value}, not {TARGET}")
    return out


def config(mobile: Path) -> list[str]:
    out = []
    text = (mobile / "capacitor.config.ts").read_text()
    code = re.sub(r"//[^\n]*|/\*.*?\*/", "", text, flags=re.S)
    body = code.split("const config: CapacitorConfig = {", 1)[1].split("\n}", 1)[0] if "const config: CapacitorConfig = {" in code else ""
    keys = set(re.findall(r"""['"]?([A-Za-z_$][\w$]*)['"]?\s*:""", body))
    if not body:
        out.append("capacitor.config.ts: no `const config: CapacitorConfig = {` to read")
    for k in sorted(keys - CONFIG_KEYS):
        out.append(f"capacitor.config.ts: {k}: the shell's config has nothing but the app id, its name, the bundle and navigation to nothing")
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
    resolved = mobile / "ios/App/App.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/Package.resolved"
    pins = {p.get("identity"): p.get("state", {}).get("version") for p in json.loads(resolved.read_text()).get("pins", [])} if resolved.exists() else {}
    if pins.get("capacitor-swift-pm") != ios:
        out.append(f"Package.resolved: capacitor-swift-pm is pinned at {pins.get('capacitor-swift-pm')}, not @capacitor/ios {ios}: resolve it again and commit it")
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
    if not re.search(r"MediaCaptureRefused\(wrapping: webView\??\.uiDelegate", shell) or not re.search(r"webView\??\.uiDelegate = ", shell):
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


# The runtime's own plugins the shell replaces by their names: the stand-in's file and class, the name, and the list of
# ConductorShell its calls are (RefusedPlugins.webView, .cookies; NativeHTTPCalls.methods).
STAND_INS = [
    ("RefusedPlugins.swift", "RefusedWebViewPlugin", "WebView", "RefusedPlugins.webView"),
    ("RefusedPlugins.swift", "RefusedCookiesPlugin", "CapacitorCookies", "RefusedPlugins.cookies"),
    ("ShellHttpPlugin.swift", "ShellHttpPlugin", "CapacitorHttp", "NativeHTTPCalls.methods"),
]


def stand_ins(mobile: Path) -> list[str]:
    out = []
    shell = (mobile / "ios/App/App/ShellViewController.swift").read_text()
    native = mobile / "ios/ConductorNative/Sources/ConductorShell"
    lists = {
        "RefusedPlugins.webView": swift_list((native / "Refused.swift").read_text(), "webView"),
        "RefusedPlugins.cookies": swift_list((native / "Refused.swift").read_text(), "cookies"),
        "NativeHTTPCalls.methods": swift_list((native / "NativeHTTP.swift").read_text(), "methods"),
    }
    for file, cls, js, source in STAND_INS:
        text = (mobile / "ios/App/App/Plugins" / file).read_text()
        body = text.split(f"class {cls}:", 1)[1].split("\n}", 1)[0] if f"class {cls}:" in text else ""
        if not body:
            out.append(f"{file}: no {cls}")
            continue
        if f'jsName = "{js}"' not in body:
            out.append(f"{file}: {cls}'s jsName is not {js}: it does not replace the runtime's own")
        if f"pluginMethods: [CAPPluginMethod] = {source}.map" not in body:
            out.append(f"{file}: {cls} does not declare the calls of {source}")
        funcs = re.findall(r"@objc func (\w+)\(_ call: CAPPluginCall\)", body)
        if not lists[source] or sorted(funcs) != sorted(lists[source]):
            out.append(f"{file}: {cls} has {sorted(funcs)}, not {source} {sorted(lists[source])}")
        if f"registerPluginInstance({cls}())" not in shell:
            out.append(f"ShellViewController.swift: {cls} is not registered: the runtime's own {js} stays")
    if not re.search(r"override func instanceDescriptor\(\) -> InstanceDescriptor \{\s*InstanceDescriptor\(\)\s*\}", shell):
        out.append("ShellViewController.swift: instanceDescriptor is not the plain one: a kept server base path could be loaded at start")
    return out


def packaging(mobile: Path) -> list[str]:
    """A packaging's place ships empty; the app links its module with -ObjC and names it at start."""
    out = []
    place = mobile / "ios/ConductorNative/Sources/ConductorPackaging/Packaging"
    if place.exists():
        for f in sorted(place.rglob("*")):
            if f.is_file():
                out.append(f"{f.relative_to(mobile)}: the packaging's place ships nothing in this repository")
    pbx = (mobile / "ios/App/App.xcodeproj/project.pbxproj").read_text()
    if not re.search(r"isa = XCSwiftPackageProductDependency;\s*package = \w{24} [^;]*;\s*productName = ConductorPackaging;", pbx):
        out.append("project.pbxproj: the app does not link ConductorPackaging: a packaging's policy would not be in it")
    app_configs = [pbx.split(f"\t\t{c} = {{", 1)[1].split("name = ", 1)[0] for c in ("504EC3171FED79650016851F /* Debug */", "504EC3181FED79650016851F /* Release */") if f"\t\t{c} = {{" in pbx]
    if len(app_configs) != 2 or not all('"-ObjC",' in c for c in app_configs):
        out.append("project.pbxproj: the app does not link with -ObjC in both configurations: a policy nothing names could be dropped")
    if "_ = ConductorPackaging.linked()" not in (mobile / "ios/App/App/ShellViewController.swift").read_text():
        out.append("ShellViewController.swift: ConductorPackaging is not named at start")
    return out


# The characters a path of the app's own scheme may be spelled with (ConductorShell's FilePrefix.pathCharacters): the bundle's files
# and the shell's routes are held to them, so that the content rules never refuse a page of the shell's own.
PATH_SPELLING = re.compile(r"^[/A-Za-z0-9._-]*$")


def bridge_calls(mobile: Path) -> dict[str, set[str]]:
    """The calls the bridge makes of each plugin (NativeLayer in bridge.ts, named as main.ts reads them), with the runtime's listener
    removal beside each plugin the bridge listens to."""
    bridge = (mobile / "src/bridge.ts").read_text()
    names = dict(re.findall(r"\b(\w+) = p\.(\w+)\b", (mobile / "src/main.ts").read_text()))
    layer = re.search(r"export interface NativeLayer \{(.*?)\n\}", bridge, re.S)
    out: dict[str, set[str]] = {}
    if not layer:
        return out
    body = layer.group(1)
    for field, kind in re.findall(r"^  (\w+): (\w+)\s*$", body, re.M):
        if field in names:
            out[names[field]] = set(interface(bridge, kind))
    for m in re.finditer(r"^  (\w+): \{(.*?)\}\s*$", body, re.M | re.S):
        field = m.group(1)
        if field not in names:
            continue
        text = m.group(2)
        out[names[field]] = set(re.findall(r"(?:^|\n|;)\s*(\w+)\(", text))
    for plugin, methods in out.items():
        if "addListener" in methods:
            methods.add("removeListener")
    return out


def gate_table(native: Path) -> dict[str, set[str]]:
    gate = (native / "BridgeGate.swift").read_text()
    m = re.search(r"static let calls: \[String: Set<String>\] = \[(.*?)\n    \]", gate, re.S)
    if not m:
        return {}
    return {plugin: set(re.findall(r'"(\w+)"', methods)) for plugin, methods in re.findall(r'"(\w+)": \[([^\]]*)\]', m.group(1))}


def gates(mobile: Path) -> list[str]:
    """One gate at each way from the page into native code, each an allowlist (docs/plans/phone-shell.md)."""
    out = []
    shell = (mobile / "ios/App/App/ShellViewController.swift").read_text()
    for line, what in (
        ("runtime.contentController.removeScriptMessageHandler(forName: BridgeGate.handler)", "the runtime's message handler is not taken off"),
        ("runtime.contentController.add(g, name: BridgeGate.handler)", "the bridge gate is not put in its place"),
        ("BridgeGateHandler(forwardingTo: runtime, view: webView, app: app)", "the bridge gate does not know the shell's view and origin"),
        ("webView.navigationDelegate = n", "the load gate is not put in front of the runtime's navigation rule"),
        ("d.newWindow = ", "a new window is not decided by the load gate"),
        ("javaScriptCanOpenWindowsAutomatically = false", "a script may open a window on its own"),
        ("override func router() -> Router", "the runtime's router is not the shell's"),
        ("encodedContentRuleList: FilePrefix.rules(app: app)", "the content rules do not hold the app's own spelling"),
        ("compileContentRuleList(forIdentifier: FilePrefix.identifier", "the runtime's file prefixes are not blocked"),
        ("guard gate != nil, loads != nil", "the page loads whether or not the gates stand"),
    ):
        if line not in shell:
            out.append(f"ShellViewController.swift: {what}")
    native = mobile / "ios/ConductorNative/Sources/ConductorShell"
    ui = (native / "MediaCapture.swift").read_text()
    m = re.search(r"runJavaScriptTextInputPanelWithPrompt prompt: String,.*?\{\s*completionHandler\(nil\)\s*\}", ui, re.S)
    if not m:
        out.append("MediaCapture.swift: a text-input prompt is not answered nil by the shell's own delegate")
    table = gate_table(native)
    calls = bridge_calls(mobile)
    for plugin in sorted(set(table) | set(calls)):
        if table.get(plugin, set()) != calls.get(plugin, set()):
            out.append(f"BridgeGate.swift: {plugin} admits {sorted(table.get(plugin, set()))}, not the bridge's calls {sorted(calls.get(plugin, set()))}")
    if "CallRunner" not in (mobile / "ios/App/App/Plugins/ShellHttpPlugin.swift").read_text():
        out.append("ShellHttpPlugin.swift: its calls are not run one at a time through a CallRunner")
    out += spellings(mobile)
    return out


def spellings(mobile: Path) -> list[str]:
    """The built bundle's files (mobile/www, else internal/web/dist, when built) and the shell's routes, each spelled as the content
    rules let through."""
    out = []
    root = mobile.parent
    for bundle in (mobile / "www", root / "internal/web/dist"):
        if (bundle / "index.html").exists():
            for f in sorted(bundle.rglob("*")):
                rel = "/" + f.relative_to(bundle).as_posix()
                if f.is_file() and f.name != ".gitkeep" and not PATH_SPELLING.match(rel):
                    out.append(f"{bundle.relative_to(root)}{rel}: a path the content rules would refuse")
            break
    pages = root / "web/app/pages/shell"
    if pages.exists():
        for f in sorted(pages.rglob("*.vue")):
            route = "/shell/" + f.relative_to(pages).with_suffix("").as_posix()
            route = re.sub(r"/index$", "", re.sub(r"\[\w+\]", "id", route))
            if not PATH_SPELLING.match(route):
                out.append(f"web/app/pages/shell/{f.relative_to(pages).as_posix()}: a route the content rules would refuse ({route})")
    return out


def check(root: Path = ROOT) -> list[str]:
    mobile = root / "mobile"
    return info_plist(mobile) + project(mobile) + config(mobile) + plugins(mobile) + stand_ins(mobile) + packaging(mobile) + gates(mobile)


def main() -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n\n")[0])
    ap.add_argument("--root", type=Path, default=ROOT, help="the repository (default: this one)")
    findings = check(ap.parse_args().root)
    for f in findings:
        print(f"ios_project: {f}", file=sys.stderr)
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
