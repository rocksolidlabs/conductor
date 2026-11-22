#!/usr/bin/env python3
"""Check the phone shell's Android project against what it is held to (docs/plans/phone-shell.md, OSS-3).

What a Gradle build would not refuse but the shell must not ship, read from the files themselves:

- AndroidManifest.xml: no backup (allowBackup off, the backup and
  data-extraction rules named), no cleartext (and the network security
  config named), not debuggable; exactly one component, the shell's
  activity, exported with the launcher's filter alone (no link filter, no
  data); no permission but the network and notifications.
- The backup and data-extraction rules exclude every domain and include
  none; the network security config permits no cleartext, trusts the
  system's authorities alone, and has no domain or debug override.
- The Gradle files: every version pinned (no `+`, no `latest`), the
  minimum Android version the native runtime's own template sets, the
  development app id, no signing; the Gradle distribution pinned by its
  checksum; the native runtime's plugin projects exactly the shell's.
- No google-services.json, keystore or local.properties in the tree, and a
  packaging's place for its native sources (app/src/packaging) empty.
- The bridge gate's table (BridgeGate.kt) is exactly the bridge's calls and
  events (mobile/src/bridge.ts, as ios_project.py reads them for the
  iPhone's); each plugin declares exactly the calls the bridge makes of it
  and is registered; the stand-ins answer exactly the runtime's own lists;
  the policy names the class behind every name the page reaches; each gate
  is put in place before the page loads.
- With the native runtime installed (mobile/node_modules, `--runtime`
  requires it): the minimum against the template the pinned @capacitor/cli
  carries; every JavaScript interface the runtime and its plugins add is
  one the gates take off; every plugin the runtime registers for every page
  is either one the gate's table leaves out or one the shell replaces; the
  stand-ins' lists are the runtime's own plugins' methods.

    python3 scripts/android_project.py             # exit 1 with each finding
    python3 scripts/android_project.py --runtime   # the runtime's sources too, required (after npm ci in mobile/)

Only Python's standard library.
"""
from __future__ import annotations

import argparse
import io
from pathlib import Path
import re
import sys
import tarfile
import xml.etree.ElementTree as ET

sys.path.insert(0, str(Path(__file__).resolve().parent))
from ios_project import bridge_calls, bridge_events, interface  # noqa: E402

ROOT = Path(__file__).resolve().parents[1]
APP_ID = "dev.conductor.mobile"
A = "{http://schemas.android.com/apk/res/android}"
PERMISSIONS = {"android.permission.INTERNET", "android.permission.POST_NOTIFICATIONS"}
BACKUP_DOMAINS = {"root", "file", "database", "sharedpref", "external"}
EXTRACTION_DOMAINS = BACKUP_DOMAINS | {"device_root", "device_file", "device_database", "device_sharedpref"}
KOTLIN = "android/app/src/main/java/dev/conductor/mobile"
# Each plugin of the shell's: its class, the name the page reaches it by, and the bridge's interface and the Kotlin list it answers.
PLUGINS = [
    ("ConductorKeyPlugin", "ConductorKey", "NativeKey", ("KeyCalls.kt", "METHODS")),
    ("ConductorStorePlugin", "ConductorStore", "NativeStore", ("StoreCalls.kt", "METHODS")),
    ("ShellHttpPlugin", "CapacitorHttp", None, ("NativeHttp.kt", "METHODS")),
    ("RefusedWebViewPlugin", "WebView", None, ("Plugins.kt", "WEB_VIEW")),
    ("RefusedCookiesPlugin", "CapacitorCookies", None, ("Plugins.kt", "COOKIES")),
]
# The runtime's own plugins the stand-ins replace, and the source each list is held to.
RUNTIME_LISTS = {"WEB_VIEW": "plugin/WebView.java", "COOKIES": "plugin/CapacitorCookies.java"}
RUNTIME_JAVA = "node_modules/@capacitor/android/capacitor/src/main/java/com/getcapacitor"


def manifest(mobile: Path) -> list[str]:
    out = []
    root = ET.parse(mobile / "android/app/src/main/AndroidManifest.xml").getroot()
    app = root.find("application")
    if app is None:
        return ["AndroidManifest.xml: no application"]
    want = {"allowBackup": "false", "usesCleartextTraffic": "false", "networkSecurityConfig": "@xml/network_security_config",
            "fullBackupContent": "@xml/backup_rules", "dataExtractionRules": "@xml/data_extraction_rules"}
    for k, v in want.items():
        if app.get(A + k) != v:
            out.append(f"AndroidManifest.xml: android:{k} is {app.get(A + k)!r}, not {v!r}")
    for k in ("debuggable", "testOnly"):
        if app.get(A + k) is not None:
            out.append(f"AndroidManifest.xml: android:{k} is set: the build type decides it")
    components = [c for c in app if c.tag in ("activity", "activity-alias", "service", "receiver", "provider")]
    if len(components) != 1 or components[0].tag != "activity" or components[0].get(A + "name") != ".MainActivity":
        out.append(f"AndroidManifest.xml: its components are {[(c.tag, c.get(A + 'name')) for c in components]}, not the shell's activity alone")
    for c in components:
        filters = c.findall("intent-filter")
        if c.get(A + "exported") == "true":
            shape = [(sorted(a.get(A + "name") for a in f.findall("action")), sorted(x.get(A + "name") for x in f.findall("category")), len(f.findall("data"))) for f in filters]
            if shape != [(["android.intent.action.MAIN"], ["android.intent.category.LAUNCHER"], 0)]:
                out.append(f"AndroidManifest.xml: {c.get(A + 'name')} is exported with {shape}, not the launcher's filter alone")
    asked = {p.get(A + "name") for p in root.iter("uses-permission")} | {p.get(A + "name") for p in root.iter("uses-permission-sdk-23")}
    if asked != PERMISSIONS:
        out.append(f"AndroidManifest.xml: it asks for {sorted(asked)}, not {sorted(PERMISSIONS)}")
    return out


def rules(mobile: Path) -> list[str]:
    out = []
    xml = mobile / "android/app/src/main/res/xml"

    def excluded(node) -> tuple[set[str], int]:
        if node is None:
            return set(), 0
        return {e.get("domain") for e in node.findall("exclude") if e.get("path") == "."}, len(node.findall("include"))

    backup = ET.parse(xml / "backup_rules.xml").getroot()
    domains, includes = excluded(backup)
    if domains != BACKUP_DOMAINS or includes:
        out.append(f"backup_rules.xml: excludes {sorted(domains)} and includes {includes}: every domain is excluded and none included")
    extraction = ET.parse(xml / "data_extraction_rules.xml").getroot()
    for part in ("cloud-backup", "device-transfer"):
        domains, includes = excluded(extraction.find(part))
        if domains != EXTRACTION_DOMAINS or includes:
            out.append(f"data_extraction_rules.xml: {part} excludes {sorted(domains)} and includes {includes}: every domain is excluded and none included")
    net = ET.parse(xml / "network_security_config.xml").getroot()
    base = net.find("base-config")
    if base is None or base.get("cleartextTrafficPermitted") != "false":
        out.append("network_security_config.xml: the base config permits cleartext")
    anchors = [c.get("src") for c in net.iter("certificates")]
    if anchors != ["system"]:
        out.append(f"network_security_config.xml: it trusts {anchors}, not the system's authorities alone")
    for tag in ("domain-config", "debug-overrides"):
        if net.find(tag) is not None:
            out.append(f"network_security_config.xml: a {tag}: the shell has none")
    return out


def template_min(mobile: Path) -> str | None:
    """The minimum Android version the pinned @capacitor/cli's own template sets (its variables.gradle)."""
    tar = mobile / "node_modules/@capacitor/cli/assets/android-template.tar.gz"
    if not tar.exists():
        return None
    with tarfile.open(tar) as t:
        for m in t.getmembers():
            if m.name.endswith("variables.gradle") and "/" not in m.name.lstrip("./"):
                f = t.extractfile(m)
                text = io.TextIOWrapper(f, encoding="utf-8").read() if f else ""
                found = re.search(r"minSdkVersion\s*=\s*(\d+)", text)
                return found.group(1) if found else None
    return None


def gradle(mobile: Path, runtime: bool) -> list[str]:
    out = []
    android = mobile / "android"
    variables = (android / "variables.gradle").read_text()
    for name, value in re.findall(r"^\s*(\w+)\s*=\s*(.+?)\s*$", variables.split("ext {", 1)[1], re.M):
        if not re.fullmatch(r"'[0-9][0-9A-Za-z.\-]*'|[0-9]+", value):
            out.append(f"variables.gradle: {name} = {value}: every version is pinned")
    minimum = re.search(r"minSdkVersion\s*=\s*(\d+)", variables)
    template = template_min(mobile)
    if runtime and template is None:
        out.append("mobile/node_modules: the native runtime's template is not installed: run npm ci in mobile/")
    if template is not None and (minimum is None or minimum.group(1) != template):
        out.append(f"variables.gradle: minSdkVersion is {minimum.group(1) if minimum else 'missing'}, not the runtime's template's {template}")
    for f in ("build.gradle", "app/build.gradle"):
        text = (android / f).read_text()
        for dep in re.findall(r"""(?:classpath|implementation|testImplementation|androidTestImplementation)\s+["']([^"']+)["']""", text):
            if dep.count(":") == 2 and not re.fullmatch(r"[\w.\-]+:[\w.\-]+:(?:\$\w+|[0-9][0-9A-Za-z.\-]*)", dep):
                out.append(f"{f}: {dep}: every version is pinned")
        if re.search(r"\bsigningConfigs?\b", text):
            out.append(f"{f}: a signing configuration: signing is the packaging's, never the repository's")
    app = (android / "app/build.gradle").read_text()
    if f'applicationId "{APP_ID}"' not in app or f'namespace = "{APP_ID}"' not in app:
        out.append(f"app/build.gradle: the app id is not {APP_ID}")
    if "apply from: 'notices.gradle'" not in app:
        out.append("app/build.gradle: the release build's packages are not listed for the notices (notices.gradle)")
    if "java.srcDir 'src/packaging/java'" not in app:
        out.append("app/build.gradle: a packaging's place (src/packaging/java) is not compiled into the app")
    wrapper = (android / "gradle/wrapper/gradle-wrapper.properties").read_text()
    if not re.search(r"^distributionUrl=https\\://services\.gradle\.org/distributions/gradle-[0-9.]+-(?:all|bin)\.zip$", wrapper, re.M):
        out.append("gradle-wrapper.properties: the Gradle distribution is not a pinned release")
    if not re.search(r"^distributionSha256Sum=[0-9a-f]{64}$", wrapper, re.M):
        out.append("gradle-wrapper.properties: the Gradle distribution is not pinned by its checksum")
    settings = (android / "capacitor.settings.gradle").read_text()
    projects = dict(re.findall(r"project\(':([\w-]+)'\)\.projectDir = new File\('([^']+)'\)", settings))
    want = {
        "capacitor-android": "../node_modules/@capacitor/android/capacitor",
        "capacitor-app": "../node_modules/@capacitor/app/android",
        "capacitor-browser": "../node_modules/@capacitor/browser/android",
        "capacitor-push-notifications": "../node_modules/@capacitor/push-notifications/android",
    }
    if projects != want:
        out.append(f"capacitor.settings.gradle: its projects are {projects}, not the shell's: run npx cap sync android")
    return out


def tree(mobile: Path) -> list[str]:
    out = []
    android = mobile / "android"
    for f in sorted(android.rglob("*")):
        if not f.is_file() or any(p in f.parts for p in ("build", ".gradle", "capacitor-cordova-android-plugins")):
            continue
        if f.name in ("google-services.json", "local.properties") or f.suffix in (".jks", ".keystore", ".p12"):
            out.append(f"{f.relative_to(mobile)}: the packaging's or the machine's, never the repository's")
    place = android / "app/src/packaging"
    if place.exists():
        for f in sorted(place.rglob("*")):
            if f.is_file():
                out.append(f"{f.relative_to(mobile)}: the packaging's place ships nothing in this repository")
    return out


def kotlin_map(text: str, name: str) -> dict[str, set[str]]:
    m = re.search(rf"val {name}: Map<String, Set<String>> = mapOf\((.*?)\n    \)", text, re.S)
    if not m:
        return {}
    return {k: set(re.findall(r'"(\w+)"', v)) for k, v in re.findall(r'"(\w+)" to setOf\(([^)]*)\)', m.group(1))}


def kotlin_list(text: str, name: str) -> list[str]:
    m = re.search(rf"\bval {name} = listOf\(([^)]*)\)", text)
    return re.findall(r'"(\w+)"', m.group(1)) if m else []


def plugin_classes(text: str) -> dict[str, tuple[str, list[str]]]:
    """Each plugin class of Plugins.kt: its page name and the methods it declares with @PluginMethod."""
    out = {}
    for m in re.finditer(r'@CapacitorPlugin\(name = "(\w+)"\)\nclass (\w+) : Plugin\(\) \{(.*?)\n\}', text, re.S):
        out[m.group(2)] = (m.group(1), re.findall(r"@PluginMethod\n\s+fun (\w+)\(call: PluginCall\)", m.group(3)))
    return out


def runtime_methods(java: str) -> list[str]:
    return re.findall(r"@PluginMethod\s+public void (\w+)\(", java)


def code(mobile: Path, runtime: bool) -> list[str]:
    out = []
    k = mobile / KOTLIN
    bridge = (mobile / "src/bridge.ts").read_text()
    gate = (k / "BridgeGate.kt").read_text()
    table, events = kotlin_map(gate, "calls"), kotlin_map(gate, "events")
    calls, listened = bridge_calls(mobile), bridge_events(mobile)
    for plugin in sorted(set(table) | set(calls)):
        if table.get(plugin, set()) != calls.get(plugin, set()):
            out.append(f"BridgeGate.kt: {plugin} admits {sorted(table.get(plugin, set()))}, not the bridge's calls {sorted(calls.get(plugin, set()))}")
    for plugin in sorted(set(events) | set(listened)):
        if events.get(plugin, set()) != listened.get(plugin, set()):
            out.append(f"BridgeGate.kt: {plugin}'s listener calls admit {sorted(events.get(plugin, set()))}, not the bridge's events {sorted(listened.get(plugin, set()))}")
    plugins = plugin_classes((k / "Plugins.kt").read_text())
    activity = (k / "MainActivity.kt").read_text()
    policy = (k / "ShellPolicy.kt").read_text()
    for cls, name, iface, (file, listed) in PLUGINS:
        if cls not in plugins:
            out.append(f"Plugins.kt: no {cls}")
            continue
        page, methods = plugins[cls]
        if page != name:
            out.append(f"Plugins.kt: {cls} is {page} to the page, not {name}")
        want = kotlin_list((k / file).read_text(), listed)
        if not want or sorted(methods) != sorted(want):
            out.append(f"Plugins.kt: {cls} declares {sorted(methods)}, not {file}'s {listed} {sorted(want)}")
        if iface and sorted(interface(bridge, iface)) != sorted(want):
            out.append(f"{file}: {listed} is {sorted(want)}, not the bridge's {iface} {sorted(interface(bridge, iface))}")
        if f"registerPlugin({cls}::class.java)" not in activity:
            out.append(f"MainActivity.kt: {cls} is not registered")
        if f'"{name}" to "dev.conductor.mobile.{cls}"' not in policy:
            out.append(f"ShellPolicy.kt: its policy does not hold {name} to {cls}")
    for name in sorted(table):
        if f'"{name}" to "' not in policy:
            out.append(f"ShellPolicy.kt: its policy does not name the class behind {name}")
    gates = (k / "ShellGates.kt").read_text()
    for line, what, text in (
        ("WebViewCompat.removeWebMessageListener(webView, BridgeGate.HANDLER)", "the runtime's message listener is not taken off", gates),
        ("WebViewCompat.addWebMessageListener(webView, BridgeGate.HANDLER, setOf(APP))", "the bridge gate is not put in its place", gates),
        ("for (name in INTERFACES) webView.removeJavascriptInterface(name)", "the runtime's JavaScript interfaces are not taken off", gates),
        ("check(WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER))", "a web view without the message listener is not refused", gates),
        ("bridge.setWebViewClient(ShellWebViewClient(", "the load and request gates are not put in front of the runtime's client", gates),
        ("webView.webChromeClient = ShellChromeClient(", "the chrome client is not the shell's", gates),
        ("workers.setServiceWorkerClient(RefusingWorkers(", "service workers are served", gates),
        ("CookieHandler.setDefault(null)", "the runtime's cookie handler stays", gates),
        ("settings.mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW", "mixed content is not refused", gates),
        ("settings.allowFileAccess = false", "file addresses are reachable", gates),
        ("settings.allowContentAccess = false", "content addresses are reachable", gates),
        ("settings.javaScriptCanOpenWindowsAutomatically = false", "a script may open a window", gates),
        ("bridgeBuilder.setServerPath(ServerPath(ServerPath.PathType.ASSET_PATH, \"public\"))", "the first load is not held until the gates stand", activity),
        (".setAllowNavigation(null)", "navigation is allowed beyond the bundle", activity),
        (".setLoggingEnabled(false)", "the runtime logs the page's calls", activity),
        (".setResolveServiceWorkerRequests(false)", "the runtime serves service workers", activity),
        ("ShellGates.install(this, b)", "the gates are not installed", activity),
        ("ShellPolicy.before(", "the policy is not checked before the runtime starts", activity),
        ("ShellPolicy.after(", "the policy is not checked after the runtime starts", activity),
        ("intent = sanitized(intent,", "an intent reaches the runtime unsanitized", activity),
    ):
        if line not in text:
            out.append(f"{'ShellGates.kt' if text is gates else 'MainActivity.kt'}: {what}")
    listed = re.search(r"val INTERFACES = listOf\(([^)]*)\)", gates)
    interfaces = set(re.findall(r'"(\w+)"', listed.group(1))) if listed else set()
    if listed and "BridgeGate.HANDLER" in listed.group(1):
        interfaces.add("androidBridge")
    java = mobile / RUNTIME_JAVA
    if runtime and not java.exists():
        out.append("mobile/node_modules: the native runtime's sources are not installed: run npm ci in mobile/")
    if java.exists():
        added = set()
        for src in [*java.rglob("*.java"), *(mobile / "node_modules/@capacitor").glob("*/android/src/main/java/**/*.java")]:
            added |= set(re.findall(r'addJavascriptInterface\(this, "(\w+)"\)', src.read_text(errors="replace")))
        if not added <= interfaces:
            out.append(f"ShellGates.kt: the runtime adds the JavaScript interfaces {sorted(added - interfaces)}, which the gates leave in place")
        core = re.search(r"private void registerAllPlugins\(\) \{(.*?)\n    \}", (java / "Bridge.java").read_text(), re.S)
        for cls in re.findall(r"com\.getcapacitor\.plugin\.(\w+)\.class", core.group(1) if core else ""):
            src = (java / f"plugin/{cls}.java").read_text()
            named = re.search(r'@CapacitorPlugin\([^)]*name\s*=\s*"(\w+)"', src)
            name = named.group(1) if named else cls
            replaced = any(page == name for page, _ in plugins.values())
            if name in table and not replaced:
                out.append(f"BridgeGate.kt: the page reaches the runtime's own {name}, which the shell does not replace")
        plugins_kt = (k / "Plugins.kt").read_text()
        for listed, src in RUNTIME_LISTS.items():
            theirs = runtime_methods((java / src).read_text())
            if sorted(kotlin_list(plugins_kt, listed)) != sorted(theirs):
                out.append(f"Plugins.kt: RefusedPlugins.{listed} is {kotlin_list(plugins_kt, listed)}, not the runtime's {src} {theirs}")
    return out


def check(root: Path = ROOT, runtime: bool = False) -> list[str]:
    mobile = root / "mobile"
    return manifest(mobile) + rules(mobile) + gradle(mobile, runtime) + tree(mobile) + code(mobile, runtime)


def main() -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n\n")[0])
    ap.add_argument("--root", type=Path, default=ROOT, help="the repository (default: this one)")
    ap.add_argument("--runtime", action="store_true", help="require the native runtime's sources (mobile/node_modules) and check against them")
    args = ap.parse_args()
    findings = check(args.root, args.runtime)
    for f in findings:
        print(f"android_project: {f}", file=sys.stderr)
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
