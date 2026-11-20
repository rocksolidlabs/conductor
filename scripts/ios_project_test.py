"""Tests for ios_project.py: the repository's project passes, and each thing the shell must not ship is a finding of its own."""
from __future__ import annotations

from pathlib import Path
import plistlib
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import ios_project  # noqa: E402

FILES = [
    "ios/App/App/Info.plist",
    "ios/App/App.xcodeproj/project.pbxproj",
    "ios/App/App.xcodeproj/xcshareddata/xcschemes/App.xcscheme",
    "ios/App/AppUITests/ShellBootsUnderItsPolicyTests.swift",
    "ios/App/App/BootCheckView.swift",
    "ios/App/App/AppDelegate.swift",
    "ios/App/App/SceneDelegate.swift",
    "ios/App/App/ShellViewController.swift",
    "ios/App/App/Base.lproj/Main.storyboard",
    "ios/App/App/Plugins/Answer.swift",
    "ios/App/App/Plugins/ConductorKeyPlugin.swift",
    "ios/App/App/Plugins/ConductorStorePlugin.swift",
    "ios/App/App/Plugins/RefusedPlugins.swift",
    "ios/App/App/Plugins/ShellHttpPlugin.swift",
    "ios/ConductorNative/Sources/ConductorShell/Refused.swift",
    "ios/ConductorNative/Sources/ConductorShell/NativeHTTP.swift",
    "ios/App/CapApp-SPM/Package.swift",
    "ios/ConductorNative/Sources/ConductorShell/Calls.swift",
    "capacitor.config.ts",
    "ios/debug.xcconfig",
    "package-lock.json",
    "src/bridge.ts",
    "src/main.ts",
]


class IOSProjectTest(unittest.TestCase):
    def setUp(self) -> None:
        self.dir = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.dir)
        for f in FILES:
            dst = self.dir / "mobile" / f
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy(ios_project.ROOT / "mobile" / f, dst)

    def edit(self, f: str, old: str, new: str) -> None:
        p = self.dir / "mobile" / f
        text = p.read_text()
        self.assertIn(old, text, f"{f} no longer holds {old!r}: the test's break needs taking again")
        p.write_text(text.replace(old, new))

    def plist(self, change) -> None:
        p = self.dir / "mobile/ios/App/App/Info.plist"
        with p.open("rb") as f:
            info = plistlib.load(f)
        change(info)
        with p.open("wb") as f:
            plistlib.dump(info, f)

    def findings(self) -> list[str]:
        return ios_project.check(self.dir)

    def only(self, *parts: str) -> None:
        got = self.findings()
        self.assertEqual(len(got), len(parts), got)
        for f, part in zip(got, parts):
            self.assertIn(part, f)

    def test_the_repositorys_project_passes(self) -> None:
        self.assertEqual(ios_project.check(ios_project.ROOT), [])
        self.assertEqual(self.findings(), [])

    def test_the_face_id_words_are_held(self) -> None:
        self.plist(lambda i: i.__setitem__("NSFaceIDUsageDescription", "Face ID."))
        self.only("NSFaceIDUsageDescription is not")
        self.plist(lambda i: i.pop("NSFaceIDUsageDescription"))
        self.only("NSFaceIDUsageDescription is not")

    def test_no_other_usage_and_no_background_mode(self) -> None:
        self.plist(lambda i: i.update(NSMicrophoneUsageDescription="x", NSCameraUsageDescription="y", UIBackgroundModes=["remote-notification"]))
        self.only("NSCameraUsageDescription", "NSMicrophoneUsageDescription", "UIBackgroundModes")

    def test_no_plist_key_the_shell_does_not_need(self) -> None:
        self.plist(lambda i: i.update(NSAppTransportSecurity={"NSAllowsArbitraryLoads": True}, CFBundleURLTypes=[{"CFBundleURLSchemes": ["conductor"]}], LSApplicationQueriesSchemes=["x"], NFCReaderUsageDescription="n", UIFileSharingEnabled=True))
        got = self.findings()
        for part in ("NFCReaderUsageDescription", "NSAppTransportSecurity", "CFBundleURLTypes", "LSApplicationQueriesSchemes", "UIFileSharingEnabled"):
            self.assertTrue(any(part in f for f in got), (part, got))
        self.assertEqual(len(got), 5, got)

    def test_no_xcconfig_or_scheme_signs_or_passes_the_app_anything(self) -> None:
        f = "ios/debug.xcconfig"
        self.edit(f, "CAPACITOR_DEBUG = true", "CAPACITOR_DEBUG = true\nDEVELOPMENT_TEAM = ABCDE12345")
        self.only("debug.xcconfig: DEVELOPMENT_TEAM is set")
        self.edit(f, "DEVELOPMENT_TEAM = ABCDE12345", "CODE_SIGN_ENTITLEMENTS[sdk=iphoneos*] = App.entitlements")
        self.only("debug.xcconfig: CODE_SIGN_ENTITLEMENTS is set")
        self.edit(f, "CODE_SIGN_ENTITLEMENTS[sdk=iphoneos*] = App.entitlements", '#include? "Signing.xcconfig"')
        self.only("an xcconfig here includes no other file")
        self.edit(f, '#include? "Signing.xcconfig"', "PRODUCT_BUNDLE_IDENTIFIER = com.example.shell")
        self.only("debug.xcconfig: PRODUCT_BUNDLE_IDENTIFIER is com.example.shell")
        self.edit(f, "PRODUCT_BUNDLE_IDENTIFIER = com.example.shell", "")
        self.edit("ios/App/App.xcodeproj/xcshareddata/xcschemes/App.xcscheme", "   <LaunchAction\n", '   <LaunchAction\n      <CommandLineArguments><CommandLineArgument argument = "--x" isEnabled = "YES"/></CommandLineArguments>\n')
        self.only("App.xcscheme: a CommandLineArgument")

    def test_the_config_has_no_key_it_was_not_read_for(self) -> None:
        for old, new, part in [
            ("allowNavigation: [],", "allowNavigation: [],\n    url: 'http://192.0.2.1:3000',\n    cleartext: true,", "cleartext"),
            ("  webDir: 'www',", "  webDir: 'www',\n  ios: { scheme: 'App' },", "ios"),
            ("  webDir: 'www',", "  webDir: 'www',\n  plugins: { CapacitorHttp: { enabled: true } },", "plugins"),
            ("    allowNavigation: [],", "    'hostname': 'elsewhere.example',\n    allowNavigation: [],", "hostname"),
        ]:
            self.setUp()
            self.edit("capacitor.config.ts", old, new)
            got = self.findings()
            self.assertTrue(got and any(f"capacitor.config.ts: {part}:" in f for f in got), (part, got))

    def test_no_signing_in_the_project(self) -> None:
        f = "ios/App/App.xcodeproj/project.pbxproj"
        self.edit(f, "CODE_SIGN_STYLE = Automatic;\n\t\t\t\tCURRENT_PROJECT_VERSION = 1;", "CODE_SIGN_STYLE = Automatic;\n\t\t\t\tDEVELOPMENT_TEAM = ABCDE12345;\n\t\t\t\tCURRENT_PROJECT_VERSION = 1;")
        self.only("DEVELOPMENT_TEAM is set")
        (self.dir / "mobile/ios/App/App/App.entitlements").write_text("<plist/>")
        self.assertEqual(len(self.findings()), 2)
        self.assertIn("App.entitlements: signing is the packaging's", self.findings()[1])

    def test_ios_16_and_the_development_id_everywhere(self) -> None:
        f = "ios/App/App.xcodeproj/project.pbxproj"
        p = self.dir / "mobile" / f
        p.write_text(p.read_text().replace("IPHONEOS_DEPLOYMENT_TARGET = 16.0;", "IPHONEOS_DEPLOYMENT_TARGET = 15.0;", 1))
        self.only("IPHONEOS_DEPLOYMENT_TARGET is ['15.0', '16.0']")
        p.write_text(p.read_text().replace("IPHONEOS_DEPLOYMENT_TARGET = 15.0;", "IPHONEOS_DEPLOYMENT_TARGET = 16.0;").replace("PRODUCT_BUNDLE_IDENTIFIER = dev.conductor.mobile;", "PRODUCT_BUNDLE_IDENTIFIER = com.example.shell;", 1))
        self.only("PRODUCT_BUNDLE_IDENTIFIER is ['com.example.shell', 'dev.conductor.mobile', 'dev.conductor.mobile.uitests']")

    def test_every_swift_file_is_in_the_apps_sources(self) -> None:
        (self.dir / "mobile/ios/App/App/Plugins/Extra.swift").write_text("")
        self.only("Plugins/Extra.swift is not in the project")
        (self.dir / "mobile/ios/App/App/Plugins/Extra.swift").unlink()
        self.edit("ios/App/App.xcodeproj/project.pbxproj", "\t\t\t\tC0DC70000000000000000007 /* ConductorKeyPlugin.swift in Sources */,\n", "")
        self.only("Plugins/ConductorKeyPlugin.swift is not in the app's sources")

    def test_the_boot_check_has_its_target_and_the_scheme_runs_it(self) -> None:
        self.edit("ios/App/App.xcodeproj/xcshareddata/xcschemes/App.xcscheme", 'BlueprintName = "AppUITests"', 'BlueprintName = "Other"')
        self.only("its test action does not run AppUITests")
        self.setUp()
        self.edit("ios/App/App.xcodeproj/project.pbxproj", 'productType = "com.apple.product-type.bundle.ui-testing";', 'productType = "com.apple.product-type.bundle.unit-test";')
        self.only("no UI test target of the app")
        self.setUp()
        self.edit("ios/App/App.xcodeproj/project.pbxproj", "\t\t\t\tC0DC70000000000000000012 /* ShellBootsUnderItsPolicyTests.swift in Sources */,\n", "")
        self.only("AppUITests/ShellBootsUnderItsPolicyTests.swift is not in the app's sources")
        self.setUp()
        p = self.dir / "mobile/ios/App/App.xcodeproj/project.pbxproj"
        p.write_text(p.read_text().replace("PRODUCT_BUNDLE_IDENTIFIER = dev.conductor.mobile.uitests;", "PRODUCT_BUNDLE_IDENTIFIER = com.example.uitests;", 1))
        self.only("PRODUCT_BUNDLE_IDENTIFIER is")

    def test_the_app_links_the_native_package(self) -> None:
        f = "ios/App/App.xcodeproj/project.pbxproj"
        self.edit(f, "productName = ConductorShell;", "productName = ConductorKey;")
        self.only("does not link ConductorShell")
        self.edit(f, "relativePath = ../ConductorNative;", "relativePath = ../Elsewhere;")
        self.only("../ConductorNative is not a local package")

    def test_the_config_keeps_its_id_and_its_navigation(self) -> None:
        self.edit("capacitor.config.ts", "allowNavigation: [],", "allowNavigation: ['*.example.com'],")
        self.only("allowNavigation not empty")
        self.edit("capacitor.config.ts", "appId: 'dev.conductor.mobile'", "appId: 'com.example.shell'")
        self.assertIn("the app id is not dev.conductor.mobile", self.findings()[0])

    def test_the_runtimes_packages_follow_the_lock(self) -> None:
        f = "ios/App/CapApp-SPM/Package.swift"
        self.edit(f, 'exact: "8.6.0"', 'exact: "8.5.3"')
        self.only("capacitor-swift-pm is 8.5.3, not @capacitor/ios 8.6.0")
        self.edit(f, 'exact: "8.5.3"', 'exact: "8.6.0"')
        self.edit(f, '        .package(name: "CapacitorBrowser", path: "../../../node_modules/@capacitor/browser"),\n', "")
        self.only("@capacitor/browser is not in the native runtime's packages")

    def test_the_plugins_answer_exactly_the_bridges_calls(self) -> None:
        self.edit("ios/App/App/Plugins/ConductorKeyPlugin.swift", '        CAPPluginMethod(name: "ending", returnType: CAPPluginReturnPromise),\n', "")
        self.only("ConductorKeyPlugin.swift: declares")
        self.edit("ios/App/App/Plugins/ConductorKeyPlugin.swift", '@objc func ending(_ call: CAPPluginCall) { make(call, "ending") }', '@objc func ending(_ call: CAPPluginCall) { make(call, "remove") }')
        self.assertEqual(len(self.findings()), 2)
        self.assertIn("do not each make the call of their own name", self.findings()[1])

    def test_a_call_the_bridge_adds_is_a_finding_until_answered(self) -> None:
        self.edit("src/bridge.ts", "  clear(): Promise<void>\n}", "  clear(): Promise<void>\n  size(): Promise<number>\n}")
        self.only("Calls.swift: PluginMethods.store", "ConductorStorePlugin.swift: declares", "ConductorStorePlugin.swift: its methods")

    def test_the_page_reaches_each_plugin_by_its_name(self) -> None:
        self.edit("ios/App/App/Plugins/ConductorStorePlugin.swift", 'jsName = "ConductorStore"', 'jsName = "Store"')
        self.only("jsName is not ConductorStore")

    def test_both_plugins_registered_and_media_capture_refused(self) -> None:
        f = "ios/App/App/ShellViewController.swift"
        self.edit(f, "bridge?.registerPluginInstance(ConductorStorePlugin())", "")
        self.only("ConductorStorePlugin is not registered")
        self.edit(f, "webView?.uiDelegate = d", "")
        self.assertIn("not wrapped in MediaCaptureRefused", self.findings()[-1])

    def test_every_window_gets_the_shells_controller(self) -> None:
        self.edit("ios/App/App/Base.lproj/Main.storyboard", 'customClass="ShellViewController" customModule="App"', 'customClass="CAPBridgeViewController" customModule="Capacitor"')
        self.only("Main.storyboard: its controller is ['CAPBridgeViewController']")
        self.edit("ios/App/App/SceneDelegate.swift", "rootViewController = ShellViewController()", "rootViewController = CAPBridgeViewController()")
        self.assertIn("SceneDelegate.swift: the window's controller", self.findings()[0])

    def test_the_runtimes_own_plugins_are_replaced_and_no_kept_base_path_is_loaded(self) -> None:
        f = "ios/App/App/ShellViewController.swift"
        self.edit(f, "        bridge?.registerPluginInstance(RefusedWebViewPlugin())\n", "")
        self.only("RefusedWebViewPlugin is not registered: the runtime's own WebView stays")
        self.edit(f, "        bridge?.registerPluginInstance(ShellHttpPlugin())\n", "")
        self.assertIn("ShellHttpPlugin is not registered", self.findings()[1])
        self.setUp()
        self.edit(f, "        InstanceDescriptor()\n    }", "        super.instanceDescriptor()\n    }")
        self.only("instanceDescriptor is not the plain one")
        self.setUp()
        self.edit("ios/App/App/Plugins/RefusedPlugins.swift", 'public let jsName = "CapacitorCookies"', 'public let jsName = "Cookies"')
        self.only("RefusedCookiesPlugin's jsName is not CapacitorCookies")
        self.setUp()
        self.edit("ios/ConductorNative/Sources/ConductorShell/NativeHTTP.swift", '"patch", "delete"]', '"patch", "delete", "head"]')
        self.only("ShellHttpPlugin has")

    def test_the_packagings_place_ships_empty_and_the_app_links_it(self) -> None:
        place = self.dir / "mobile/ios/ConductorNative/Sources/ConductorPackaging/Packaging"
        place.mkdir(parents=True)
        (place / "Policy.swift").write_text("")
        self.only("Packaging/Policy.swift: the packaging's place ships nothing")
        self.setUp()
        self.edit("ios/App/App.xcodeproj/project.pbxproj", "productName = ConductorPackaging;", "productName = ConductorKey;")
        self.only("the app does not link ConductorPackaging")
        self.setUp()
        p = self.dir / "mobile/ios/App/App.xcodeproj/project.pbxproj"
        p.write_text(p.read_text().replace('\t\t\t\t\t"-ObjC",\n', "", 1))
        self.only("the app does not link with -ObjC")
        self.setUp()
        self.edit("ios/App/App/ShellViewController.swift", "        _ = ConductorPackaging.linked()\n", "")
        self.only("ConductorPackaging is not named at start")


if __name__ == "__main__":
    unittest.main()
