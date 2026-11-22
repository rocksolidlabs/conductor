"""Tests for android_project.py: the repository's project passes, and each thing the Android shell must not ship is a finding of its own."""
from __future__ import annotations

from pathlib import Path
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import android_project  # noqa: E402

K = "android/app/src/main/java/dev/conductor/mobile/"
FILES = [
    "android/app/src/main/AndroidManifest.xml",
    "android/app/src/main/res/xml/backup_rules.xml",
    "android/app/src/main/res/xml/data_extraction_rules.xml",
    "android/app/src/main/res/xml/network_security_config.xml",
    "android/variables.gradle",
    "android/build.gradle",
    "android/app/build.gradle",
    "android/gradle/wrapper/gradle-wrapper.properties",
    "android/capacitor.settings.gradle",
    K + "BridgeGate.kt",
    K + "Plugins.kt",
    K + "MainActivity.kt",
    K + "ShellPolicy.kt",
    K + "ShellGates.kt",
    K + "KeyCalls.kt",
    K + "StoreCalls.kt",
    K + "NativeHttp.kt",
    "src/bridge.ts",
    "src/main.ts",
]
RUNTIME = android_project.ROOT / "mobile/node_modules/@capacitor/android"


class AndroidProjectTest(unittest.TestCase):
    def setUp(self) -> None:
        self.dir = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.dir)
        for f in FILES:
            dst = self.dir / "mobile" / f
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy(android_project.ROOT / "mobile" / f, dst)

    def edit(self, f: str, old: str, new: str) -> None:
        p = self.dir / "mobile" / f
        text = p.read_text()
        self.assertIn(old, text, f"{f} no longer holds {old!r}: the test's break needs taking again")
        p.write_text(text.replace(old, new, 1))

    def findings(self, runtime: bool = False) -> list[str]:
        return android_project.check(self.dir, runtime)

    def one(self, needle: str, runtime: bool = False) -> None:
        found = self.findings(runtime)
        self.assertEqual(len(found), 1, found)
        self.assertIn(needle, found[0])

    def with_runtime(self) -> None:
        if not RUNTIME.exists():
            self.skipTest("the native runtime is not installed (npm ci in mobile/)")
        (self.dir / "mobile/node_modules").symlink_to(android_project.ROOT / "mobile/node_modules")

    def test_the_repositorys_project_passes(self) -> None:
        self.assertEqual(android_project.check(), [])
        self.assertEqual(self.findings(), [])

    def test_the_repositorys_project_passes_against_the_runtime(self) -> None:
        self.with_runtime()
        self.assertEqual(self.findings(runtime=True), [])

    def test_the_runtime_is_required_when_asked_for(self) -> None:
        self.assertTrue(any("not installed" in f for f in self.findings(runtime=True)))

    def test_a_backup_is_a_finding(self) -> None:
        self.edit(FILES[0], 'android:allowBackup="false"', 'android:allowBackup="true"')
        self.one("allowBackup")

    def test_cleartext_is_a_finding(self) -> None:
        self.edit(FILES[0], 'android:usesCleartextTraffic="false"', 'android:usesCleartextTraffic="true"')
        self.one("usesCleartextTraffic")

    def test_another_component_is_a_finding(self) -> None:
        self.edit(FILES[0], "    </application>", '        <provider android:name="androidx.core.content.FileProvider" android:authorities="x" android:exported="false" />\n    </application>')
        self.one("components")

    def test_a_link_filter_is_a_finding(self) -> None:
        self.edit(FILES[0], "        </activity>", '            <intent-filter><action android:name="android.intent.action.VIEW" /><category android:name="android.intent.category.BROWSABLE" /><data android:scheme="conductor" /></intent-filter>\n        </activity>')
        self.one("launcher's filter alone")

    def test_a_permission_is_a_finding(self) -> None:
        self.edit(FILES[0], '<uses-permission android:name="android.permission.INTERNET" />', '<uses-permission android:name="android.permission.INTERNET" />\n    <uses-permission android:name="android.permission.CAMERA" />')
        self.one("asks for")

    def test_a_domain_left_to_a_backup_or_a_transfer_is_a_finding(self) -> None:
        self.edit(FILES[1], '<exclude domain="sharedpref" path="." />', '<include domain="sharedpref" path="." />')
        self.one("backup_rules.xml")
        self.setUp()
        self.edit(FILES[2], '    <device-transfer>\n        <exclude domain="root" path="." />', "    <device-transfer>")
        self.one("device-transfer")

    def test_a_trusted_user_authority_or_cleartext_is_a_finding(self) -> None:
        self.edit(FILES[3], '<certificates src="system" />', '<certificates src="system" />\n            <certificates src="user" />')
        self.one("trusts")
        self.setUp()
        self.edit(FILES[3], 'cleartextTrafficPermitted="false"', 'cleartextTrafficPermitted="true"')
        self.one("cleartext")

    def test_a_version_not_pinned_is_a_finding(self) -> None:
        self.edit(FILES[4], "androidxBiometricVersion = '1.1.0'", "androidxBiometricVersion = '1.+'")
        self.one("pinned")
        self.setUp()
        self.edit(FILES[5], "kotlin-gradle-plugin:2.2.20", "kotlin-gradle-plugin:+")
        self.one("pinned")

    def test_a_minimum_other_than_the_templates_is_a_finding(self) -> None:
        self.with_runtime()
        self.edit(FILES[4], "minSdkVersion = 24", "minSdkVersion = 23")
        self.one("template", runtime=True)

    def test_signing_is_a_finding(self) -> None:
        self.edit(FILES[6], "    buildTypes {", "    signingConfigs {\n        release { storeFile file('k.jks') }\n    }\n    buildTypes {")
        self.one("signing")

    def test_a_gradle_distribution_not_pinned_by_its_checksum_is_a_finding(self) -> None:
        p = self.dir / "mobile" / FILES[7]
        p.write_text("\n".join(l for l in p.read_text().splitlines() if not l.startswith("distributionSha256Sum=")) + "\n")
        self.one("checksum")

    def test_a_file_in_the_packagings_place_or_a_services_file_is_a_finding(self) -> None:
        place = self.dir / "mobile/android/app/src/packaging/java/dev/conductor/mobile/packaging"
        place.mkdir(parents=True)
        (place / "ConductorKeySigningPolicy.kt").write_text("class ConductorKeySigningPolicy\n")
        self.one("packaging's place")
        self.setUp()
        (self.dir / "mobile/android/app/google-services.json").write_text("{}")
        self.one("google-services.json")

    def test_a_gate_table_other_than_the_bridges_calls_is_a_finding(self) -> None:
        self.edit(K + "BridgeGate.kt", '"App" to setOf("getLaunchUrl", "addListener", "removeListener"),', '"App" to setOf("getLaunchUrl", "addListener", "removeListener", "exitApp"),')
        self.one("App admits")
        self.setUp()
        self.edit(K + "BridgeGate.kt", '"App" to setOf("appUrlOpen"),', '"App" to setOf("appUrlOpen", "backButton"),')
        self.one("listener calls")

    def test_a_plugin_out_of_step_with_the_bridge_or_not_registered_is_a_finding(self) -> None:
        self.edit(K + "KeyCalls.kt", 'val METHODS = listOf("create", "publicKey", "state", "bind", "sign", "ending", "remove")', 'val METHODS = listOf("create", "publicKey", "state", "bind", "sign", "ending", "remove", "export")')
        found = self.findings()
        self.assertTrue(any("ConductorKeyPlugin declares" in f for f in found) and any("NativeKey" in f for f in found), found)
        self.setUp()
        self.edit(K + "MainActivity.kt", "        registerPlugin(RefusedCookiesPlugin::class.java)\n", "")
        self.one("RefusedCookiesPlugin is not registered")

    def test_a_gate_not_put_in_place_is_a_finding(self) -> None:
        for f, line, needle in (
            (K + "ShellGates.kt", "for (name in INTERFACES) webView.removeJavascriptInterface(name)", "JavaScript interfaces"),
            (K + "ShellGates.kt", "WebViewCompat.removeWebMessageListener(webView, BridgeGate.HANDLER)", "message listener is not taken off"),
            (K + "ShellGates.kt", "CookieHandler.setDefault(null)", "cookie handler"),
            (K + "MainActivity.kt", 'bridgeBuilder.setServerPath(ServerPath(ServerPath.PathType.ASSET_PATH, "public"))', "first load"),
            (K + "MainActivity.kt", "intent = sanitized(intent,", "unsanitized"),
        ):
            self.setUp()
            self.edit(f, line, "// " + line.replace("(", "[ "))
            self.one(needle)

    def test_an_interface_the_runtime_adds_and_the_gates_leave_is_a_finding(self) -> None:
        self.with_runtime()
        self.edit(K + "ShellGates.kt", '"CapacitorCookiesAndroidInterface", ', "")
        self.one("CapacitorCookiesAndroidInterface", runtime=True)

    def test_a_stand_in_out_of_step_with_the_runtimes_plugin_is_a_finding(self) -> None:
        self.with_runtime()
        self.edit(K + "Plugins.kt", 'val COOKIES = listOf("getCookies", "setCookie", "deleteCookie", "clearCookies", "clearAllCookies")', 'val COOKIES = listOf("getCookies", "setCookie", "deleteCookie", "clearCookies")')
        found = self.findings(runtime=True)
        self.assertTrue(any("RefusedPlugins.COOKIES" in f for f in found), found)

    def test_a_runtime_plugin_the_page_reaches_unreplaced_is_a_finding(self) -> None:
        self.with_runtime()
        self.edit(K + "BridgeGate.kt", '"Browser" to setOf("open"),', '"Browser" to setOf("open"),\n        "SystemBars" to setOf("hide"),')
        found = self.findings(runtime=True)
        self.assertTrue(any("the runtime's own SystemBars" in f for f in found), found)


if __name__ == "__main__":
    unittest.main()
