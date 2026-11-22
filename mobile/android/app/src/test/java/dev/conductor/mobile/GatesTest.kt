package dev.conductor.mobile

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// The gates between the Android shell's page and native code, as pure decisions (docs/plans/phone-shell.md, OSS-3; the iPhone's "Every
// way a page reaches native code"): who may call the bridge, what the shell loads and hands out, what its local server answers, and
// what an intent may carry in. Each case is one the runtime's own source shows native code would take.

class GatesTest {
    private val shell = BridgeGate.Sender(mainFrame = true, scheme = "https", host = "localhost", port = -1, page = "https://localhost/shell")

    private fun msg(plugin: String, method: String, options: JSONObject? = JSONObject(), callback: String = "12345", extra: Map<String, Any> = emptyMap()): String {
        val o = JSONObject().put("pluginId", plugin).put("methodName", method).put("callbackId", callback)
        if (options != null) o.put("options", options)
        for ((k, v) in extra) o.put(k, v)
        return o.toString()
    }

    @Test
    fun theBridgeGateAdmitsOnlyTheTablesPairsInTheirClosedForms() {
        // Every call the bridge makes.
        for ((plugin, methods) in BridgeGate.calls) {
            for (m in methods) {
                val options = when (m) {
                    "addListener" -> JSONObject().put("eventName", BridgeGate.events.getValue(plugin).first())
                    "removeListener" -> JSONObject().put("eventName", BridgeGate.events.getValue(plugin).first()).put("callbackId", "77")
                    "open" -> JSONObject().put("url", "https://server.example/")
                    else -> JSONObject()
                }
                assertNotNull("$plugin.$m", BridgeGate.admits(msg(plugin, m, options), shell))
            }
        }
        assertNotNull(BridgeGate.admits(msg("ConductorStore", "put", JSONObject().put("area", "drafts").put("key", "k").put("data", "x")), shell))
        assertNotNull(BridgeGate.admits(msg("ConductorKey", "state", null), shell))
        assertNotNull(BridgeGate.admits(msg("ConductorKey", "state", callback = "-1"), shell))
        assertNotNull(BridgeGate.admits(msg("Browser", "open", JSONObject().put("url", "http://192.168.1.20:8080/")), shell))
        val dropped = listOf(
            msg("WebView", "setServerBasePath", JSONObject().put("path", "/data")),
            msg("CapacitorCookies", "setCookie"),
            msg("SystemBars", "setStyle"),
            msg("PushNotifications", "getDeliveredNotifications"),
            msg("PushNotifications", "removeAllDeliveredNotifications"),
            msg("App", "exitApp"),
            msg("Browser", "close"),
            msg("ConductorKey", "addListener", JSONObject().put("eventName", "x")),
            msg("ConductorKey", "state", callback = "1'}); x({'"),
            msg("ConductorKey", "state", callback = ""),
            msg("ConductorKey", "state", callback = "1".repeat(17)),
            msg("ConductorKey", "state", extra = mapOf("type" to "message")),
            msg("ConductorKey", "state", extra = mapOf("type" to "cordova")),
            msg("ConductorKey", "state", extra = mapOf("service" to "x")),
            JSONObject().put("type", "cordova").put("service", "Device").put("action", "getDeviceInfo").put("actionArgs", "[]").put("callbackId", "1").toString(),
            JSONObject().put("type", "js.error").put("error", JSONObject()).toString(),
            JSONObject().put("pluginId", "ConductorKey").put("methodName", "state").put("callbackId", 5).toString(),
            JSONObject().put("pluginId", "ConductorKey").put("methodName", "state").put("callbackId", "1").put("options", "x").toString(),
            msg("App", "addListener", JSONObject()),
            msg("App", "addListener", JSONObject().put("eventName", "backButton")),
            msg("App", "addListener", JSONObject().put("eventName", "appUrlOpen").put("more", 1)),
            msg("App", "removeListener", JSONObject().put("eventName", "appUrlOpen").put("callbackId", "x1")),
            msg("PushNotifications", "addListener", JSONObject().put("eventName", "pushNotificationReceived")),
            msg("PushNotifications", "requestPermissions", JSONObject().put("permissions", org.json.JSONArray().put("receive"))),
            msg("App", "getLaunchUrl", JSONObject().put("x", 1)),
            msg("Browser", "open", JSONObject().put("url", "intent://scan/#Intent;scheme=zxing;end")),
            msg("Browser", "open", JSONObject().put("url", "tel:123")),
            msg("Browser", "open", JSONObject().put("url", "https://server.example/").put("toolbarColor", "#ffffff")),
            msg("Browser", "open", JSONObject().put("url", "https://user@server.example/")),
            "not json", "[]", "\"x\"", "", "{}",
        )
        for (d in dropped) assertNull(d, BridgeGate.admits(d, shell))
        assertNull(BridgeGate.admits(null, shell))
        assertNull(BridgeGate.admits(" ".repeat(BridgeGate.MAX_MESSAGE + 1), shell))
    }

    @Test
    fun theBridgeGateAdmitsOnlyTheShellsOwnPage() {
        val call = msg("ConductorKey", "state")
        assertNotNull(BridgeGate.admits(call, shell))
        assertNotNull(BridgeGate.admits(call, shell.copy(page = "https://localhost")))
        for (other in listOf(
            shell.copy(mainFrame = false),
            shell.copy(scheme = "http"),
            shell.copy(host = "localhost.example"),
            shell.copy(host = "LOCALHOST"),
            shell.copy(port = 8443),
            shell.copy(scheme = null, host = null),
            shell.copy(page = null),
            shell.copy(page = "blob:https://localhost/5f0c"),
            shell.copy(page = "data:text/html,x"),
            shell.copy(page = "about:blank"),
            shell.copy(page = "https://localhost.evil.example/"),
            shell.copy(page = "https://localhost:8443/"),
        )) {
            assertNull("$other", BridgeGate.admits(call, other))
        }
    }

    @Test
    fun theMainFrameLoadsOnlyTheAppsOwnPageAndHandsOnOnlyALinkToTheWeb() {
        fun d(url: String, main: Boolean = true, gesture: Boolean = true, redirect: Boolean = false, fg: Boolean = true) =
            LoadGate.decide(main, gesture, redirect, url, fg)
        for (own in listOf("https://localhost", "https://localhost/", "https://localhost/shell/decisions/aTE", "https://localhost?x=1", "https://localhost#a")) {
            assertTrue(own, d(own, gesture = false) is LoadGate.Decision.Load)
        }
        for (notOwn in listOf("https://localhost:8443/", "https://localhost.example/", "https://localhostx/", "http://localhost/",
            "https://user@localhost/", "HTTPS://LOCALHOST/", "https://LOCALHOST/")) {
            assertTrue(notOwn, d(notOwn, gesture = false) is LoadGate.Decision.Cancel)
        }
        val handed = d("https://provider.example/sign-in")
        assertTrue(handed is LoadGate.Decision.HandOff)
        assertEquals("https://provider.example/sign-in", (handed as LoadGate.Decision.HandOff).url)
        assertTrue(d("http://192.168.1.20:8080/d/1") is LoadGate.Decision.HandOff)
        // No gesture (a location change, a timer), a redirect, the app in the background, every other scheme: cancelled.
        assertTrue(d("https://provider.example/", gesture = false) is LoadGate.Decision.Cancel)
        assertTrue(d("https://provider.example/", redirect = true) is LoadGate.Decision.Cancel)
        assertTrue(d("https://provider.example/", fg = false) is LoadGate.Decision.Cancel)
        for (scheme in listOf("intent://scan/#Intent;scheme=zxing;package=x;end", "tel:123", "sms:1", "mailto:a@b.example", "market://details?id=x",
            "file:///data/data/x", "content://x/y", "data:text/html,x", "blob:https://localhost/1", "javascript:alert(1)", "about:blank",
            "conductor-probe://x", "https://user@provider.example/", "https://provider.example/\u0000")) {
            assertTrue(scheme, d(scheme) is LoadGate.Decision.Cancel)
        }
    }

    @Test
    fun aFrameLoadsOnlyItsOwnDocument() {
        assertTrue(LoadGate.decide(false, false, false, "about:srcdoc", true) is LoadGate.Decision.Load)
        assertTrue(LoadGate.decide(false, false, false, "about:blank", true) is LoadGate.Decision.Load)
        for (u in listOf("https://localhost/shell", "https://example.com/", "intent://x#Intent;end", "conductor-probe://x", "data:text/html,x")) {
            assertTrue(u, LoadGate.decide(false, true, false, u, true) is LoadGate.Decision.Cancel)
        }
    }

    @Test
    fun theLocalServerAnswersOnlyTheBundlesSpellingAndNeverItsPrefixes() {
        fun d(path: String?, scheme: String = "https", host: String = "localhost", port: Int = -1, main: Boolean = false, range: String? = null) =
            RequestGate.decide(scheme, host, port, path, main, range)
        for (ok in listOf("/", "", null, "/index.html", "/_nuxt/entry.Ab-1_2.js", "/shell/decisions/b3JnL3JlcG8jMTIgYTpifsOp", "/shell/native-http.json")) {
            assertEquals("$ok", RequestGate.Decision.SERVE, d(ok))
        }
        assertEquals(RequestGate.Decision.SERVE, d("/", main = true))
        assertEquals(RequestGate.Decision.SERVE, d("/a.mp4", range = "bytes=0-"))
        assertEquals(RequestGate.Decision.SERVE, d("/a.mp4", range = "bytes=10-20"))
        for (bad in listOf("/_capacitor_file_/data/user/0/dev.conductor.mobile/no_backup/conductor/drafts/d", "/_capacitor_content_/media/x",
            "/_capacitor_http_interceptor_?u=https://x", "/_capacitor_https_interceptor_/x", "/%5Fcapacitor_file_/etc/hosts", "/a/%2E%2E/b",
            "/..", "/a/../b", "/./x", "/x%20y", "/shell/decisions/org%2Frepo", "/é", "/a;b", "/a b", "/a\\b")) {
            assertEquals(bad, RequestGate.Decision.REFUSE, d(bad))
        }
        for (r in listOf("bytes=x", "bytes=-500", "bytes=1-2,3-4", "items=0-1", "bytes=0-1 ", "")) assertEquals(r, RequestGate.Decision.REFUSE, d("/a.mp4", range = r))
        // The end is a number the server's own parse (an int) takes, or the server's process ends: the edge served, past it refused.
        assertEquals(RequestGate.Decision.SERVE, d("/a.mp4", range = "bytes=0-2147483647"))
        assertEquals(RequestGate.Decision.SERVE, d("/a.mp4", range = "bytes=999999999999999-"))
        for (r in listOf("bytes=0-2147483648", "bytes=0-9999999999", "bytes=0-999999999999999")) assertEquals(r, RequestGate.Decision.REFUSE, d("/a.mp4", range = r))
        // The server's host on another scheme or port: refused, whatever the path.
        assertEquals(RequestGate.Decision.REFUSE, d("/", scheme = "http"))
        assertEquals(RequestGate.Decision.REFUSE, d("/", port = 8443))
        assertEquals(RequestGate.Decision.REFUSE, d("/", host = "LocalHost"))
        // Another origin: a provider's request goes on; a document, in the main frame or in a frame, never loads in the shell's page.
        assertEquals(RequestGate.Decision.NETWORK, d("/v1/inbox", host = "api.provider.example"))
        assertEquals(RequestGate.Decision.REFUSE, d("/form", host = "api.provider.example", main = true))
        val frame = RequestGate.document(false, mapOf("Upgrade-Insecure-Requests" to "1", "Accept" to "text/html,application/xhtml+xml"))
        assertEquals(RequestGate.Decision.REFUSE, d("/", host = "example.com", main = frame))
        assertTrue(RequestGate.document(false, mapOf("upgrade-insecure-requests" to "1")))
        assertTrue(RequestGate.document(false, mapOf("accept" to "TEXT/HTML")))
        assertTrue(RequestGate.document(true, null))
        assertTrue(!RequestGate.document(false, mapOf("Accept" to "application/json")))
        assertTrue(!RequestGate.document(false, null))
        // A frame of the bundle's own is the bundle's: served when it is spelled as the bundle's files are.
        assertEquals(RequestGate.Decision.SERVE, d("/shell", main = frame))
    }

    @Test
    fun anIntentCarriesInOnlyATapsHandleAndAnHttpsLink() {
        assertEquals(mapOf(TapData.MESSAGE_ID to "0:1", TapData.HANDLE to "h-1"),
            TapData.keep(mapOf(TapData.MESSAGE_ID to "0:1", TapData.HANDLE to "h-1", "from" to "x", "collapse_key" to "y", "google.sent_time" to 5L)))
        assertEquals(mapOf(TapData.MESSAGE_ID to "0:1"), TapData.keep(mapOf(TapData.MESSAGE_ID to "0:1", TapData.HANDLE to "x".repeat(257))))
        assertEquals(mapOf(TapData.MESSAGE_ID to "0:1", TapData.HANDLE to "é".repeat(128)), TapData.keep(mapOf(TapData.MESSAGE_ID to "0:1", TapData.HANDLE to "é".repeat(128))))
        assertEquals(mapOf(TapData.MESSAGE_ID to "0:1"), TapData.keep(mapOf(TapData.MESSAGE_ID to "0:1", TapData.HANDLE to "é".repeat(129))))
        assertEquals(mapOf(TapData.MESSAGE_ID to "0:1"), TapData.keep(mapOf(TapData.MESSAGE_ID to "0:1", TapData.HANDLE to 7)))
        assertEquals(emptyMap<String, String>(), TapData.keep(mapOf(TapData.HANDLE to "h-1", "data" to "x")))
        assertEquals(emptyMap<String, String>(), TapData.keep(mapOf(TapData.MESSAGE_ID to 3, TapData.HANDLE to "h-1")))
        assertEquals("https://push.example/d/1", TapData.link(TapData.VIEW, "https://push.example/d/1"))
        for (bad in listOf("http://push.example/d/1", "intent://x#Intent;end", "conductor://x", "file:///x", "https://u@push.example/", "https://")) {
            assertNull(bad, TapData.link(TapData.VIEW, bad))
        }
        assertNull(TapData.link("android.intent.action.MAIN", "https://push.example/d/1"))
        assertNull(TapData.link(TapData.VIEW, null))
    }

    @Test
    fun thePolicyStopsTheShellOnAnythingTheBuildOrTheRuntimeChanged() {
        val plugins = """[{"pkg":"@capacitor/app","classpath":"com.capacitorjs.plugins.app.AppPlugin"},{"pkg":"@capacitor/browser","classpath":"com.capacitorjs.plugins.browser.BrowserPlugin"},{"pkg":"@capacitor/push-notifications","classpath":"com.capacitorjs.plugins.pushnotifications.PushNotificationsPlugin"}]"""
        val good = ShellPolicy.Before(messageListener = true, documentStartScript = true, allowBackup = false, cleartext = false, pluginsJson = plugins)
        assertEquals(emptyList<String>(), ShellPolicy.before(good))
        fun b(f: (ShellPolicy.Before) -> ShellPolicy.Before) = ShellPolicy.before(f(good)).size
        assertEquals(1, b { ShellPolicy.Before(false, it.documentStartScript, it.allowBackup, it.cleartext, it.pluginsJson) })
        assertEquals(1, b { ShellPolicy.Before(it.messageListener, false, it.allowBackup, it.cleartext, it.pluginsJson) })
        assertEquals(1, b { ShellPolicy.Before(it.messageListener, it.documentStartScript, true, it.cleartext, it.pluginsJson) })
        assertEquals(1, b { ShellPolicy.Before(it.messageListener, it.documentStartScript, it.allowBackup, true, it.pluginsJson) })
        assertEquals(1, b { ShellPolicy.Before(it.messageListener, it.documentStartScript, it.allowBackup, it.cleartext, null) })
        assertEquals(1, b { ShellPolicy.Before(it.messageListener, it.documentStartScript, it.allowBackup, it.cleartext,
            plugins.replace("]", ",{\"pkg\":\"x\",\"classpath\":\"com.example.Camera\"}]")) })
        val ok = ShellPolicy.After(scheme = "https", host = "localhost", serverUrl = null, allowNavigation = emptyList(), mixedContent = false,
            legacyBridge = false, logging = false, serviceWorkers = false, debugging = true, debuggable = true, cookieHandler = false,
            plugins = ShellPolicy.PLUGINS)
        assertEquals(emptyList<String>(), ShellPolicy.after(ok))
        fun a(serverUrl: String? = null, nav: List<String> = emptyList(), mixed: Boolean = false, legacy: Boolean = false, logging: Boolean = false,
              workers: Boolean = false, debugging: Boolean = true, cookies: Boolean = false, plugins: Map<String, String?> = ShellPolicy.PLUGINS) =
            ShellPolicy.after(ShellPolicy.After("https", "localhost", serverUrl, nav, mixed, legacy, logging, workers, debugging, true, cookies, plugins)).size
        assertEquals(1, a(serverUrl = "https://evil.example"))
        assertEquals(1, a(nav = listOf("*")))
        assertEquals(1, a(mixed = true))
        assertEquals(1, a(legacy = true))
        assertEquals(1, a(logging = true))
        assertEquals(1, a(workers = true))
        assertEquals(1, a(debugging = false))
        assertEquals(1, a(cookies = true))
        assertEquals(1, a(plugins = ShellPolicy.PLUGINS + ("WebView" to "com.getcapacitor.plugin.WebView")))
        assertEquals(1, a(plugins = ShellPolicy.PLUGINS - "ConductorKey"))
    }

    @Test
    fun onlyTheActivityAndTheGuardedReceiversAreExported() {
        val good = listOf(
            Exported.Component("dev.conductor.mobile.MainActivity", true, null),
            Exported.Component("com.google.firebase.iid.FirebaseInstanceIdReceiver", true, "com.google.android.c2dm.permission.SEND"),
            Exported.Component("com.capacitorjs.plugins.browser.BrowserControllerActivity", false, null),
            Exported.Component("com.capacitorjs.plugins.pushnotifications.MessagingService", false, null),
        )
        assertEquals(emptyList<String>(), Exported.findings(good))
        assertEquals(1, Exported.findings(good + Exported.Component("androidx.core.content.FileProvider", true, null)).size)
        assertEquals(1, Exported.findings(good + Exported.Component("com.google.firebase.iid.FirebaseInstanceIdReceiver", true, null)).size)
    }
}
