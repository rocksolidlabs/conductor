package dev.conductor.mobile

import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.os.Message
import android.util.Log
import android.webkit.ConsoleMessage
import android.webkit.GeolocationPermissions
import android.webkit.JsPromptResult
import android.webkit.PermissionRequest
import android.webkit.ServiceWorkerClient
import android.webkit.ServiceWorkerController
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import com.getcapacitor.Bridge
import com.getcapacitor.BridgeWebChromeClient
import com.getcapacitor.BridgeWebViewClient
import com.getcapacitor.JSObject
import com.getcapacitor.MessageHandler
import com.getcapacitor.PluginCall
import java.io.ByteArrayInputStream
import java.net.CookieHandler
import java.util.concurrent.atomic.AtomicInteger

// One gate at each way from the shell's page into native code (docs/plans/phone-shell.md, OSS-3; the iPhone's "Every way a page reaches
// native code"), each an allowlist that fails closed, read from the native runtime's own source (@capacitor/android 8.6.0):
// - the runtime's message channel, `androidBridge`, taken over by BridgeGate: only the shell's own page, only a call of the closed
//   table in the runtime's own forms, forwarded to the runtime's own handler;
// - no JavaScript interface at all: the runtime's cookies and HTTP plugins add one each, which every frame of every origin reaches,
//   and its channel falls back to one where the web view has no web message listener; all three are taken off before the first load,
//   and a web view without the listener does not start the shell;
// - the web view client: every navigation decided by LoadGate, every request by RequestGate before the runtime's local server;
// - service workers: none served (their requests would go to the local server around the web view client);
// - the chrome client: every permission (a camera, a microphone, anything else) and the location denied, no file chooser, no prompt,
//   no new window;
// - the web view's settings: no file or content address, no mixed content, no window a script opens, no location;
// - the runtime's process-wide cookie handler (its cookies plugin sets one) taken off, and a server path a page once asked to keep
//   forgotten.
// The page loads only once every gate stands (MainActivity: the runtime's first load is posted, and runs after this).

/** What the gates refused, counted for the boot check: messages dropped, navigations cancelled, requests refused, hand-offs made. */
class ShellGates private constructor(private val handler: MessageHandler, private val pushConfigured: Boolean) {
    val dropped = AtomicInteger()
    val cancelled = AtomicInteger()
    val refused = AtomicInteger()
    val handedOff = AtomicInteger()
    val workers = AtomicInteger()

    /** One message of the channel: admitted and forwarded to the runtime's own handler, or dropped and counted. */
    fun take(page: String?, data: String?, origin: Uri, mainFrame: Boolean) {
        val sender = BridgeGate.Sender(mainFrame, origin.scheme, origin.host, origin.port, page)
        val call = BridgeGate.admits(data, sender)
        if (call == null || data == null) {
            dropped.incrementAndGet()
            return
        }
        // The push service is the packaging's (its google-services.json): without it the runtime's register fails in a way that ends the
        // app, so the shell answers it itself.
        if (call.pluginId == "PushNotifications" && call.methodName == "register" && !pushConfigured) {
            val words = "This build has no push service: notifications cannot be turned on."
            val reply = JSObject()
            reply.put("words", words)
            PluginCall(handler, call.pluginId, call.callbackId, call.methodName, JSObject()).reject(words, "no_push", reply)
            return
        }
        handler.postMessage(data)
    }

    companion object {
        /** The names of the JavaScript interfaces the runtime adds: none stays. */
        val INTERFACES = listOf("CapacitorCookiesAndroidInterface", "CapacitorHttpAndroidInterface", BridgeGate.HANDLER)

        /** The runtime's own message handler, which it keeps in a private field: the gate forwards to it what it admits. */
        fun runtimeHandler(bridge: Bridge): MessageHandler {
            val field = Bridge::class.java.getDeclaredField("msgHandler")
            field.isAccessible = true
            return field.get(bridge) as MessageHandler
        }

        /** Puts every gate in place on the bridge's web view, or throws: the caller then loads nothing. */
        fun install(activity: MainActivity, bridge: Bridge): ShellGates {
            check(WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)) { "no web message listener" }
            val webView = bridge.webView
            val pushConfigured = activity.resources.getIdentifier("google_app_id", "string", activity.packageName) != 0
            val gates = ShellGates(runtimeHandler(bridge), pushConfigured)
            for (name in INTERFACES) webView.removeJavascriptInterface(name)
            WebViewCompat.removeWebMessageListener(webView, BridgeGate.HANDLER)
            WebViewCompat.addWebMessageListener(webView, BridgeGate.HANDLER, setOf(APP)) { view, message, origin, mainFrame, _ ->
                gates.take(view.url, message.data, origin, mainFrame)
            }
            val settings = webView.settings
            settings.javaScriptCanOpenWindowsAutomatically = false
            settings.setSupportMultipleWindows(false)
            settings.setGeolocationEnabled(false)
            settings.allowFileAccess = false
            settings.allowContentAccess = false
            @Suppress("DEPRECATION")
            settings.allowFileAccessFromFileURLs = false
            @Suppress("DEPRECATION")
            settings.allowUniversalAccessFromFileURLs = false
            settings.mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            bridge.setWebViewClient(ShellWebViewClient(bridge, activity, gates))
            webView.webChromeClient = ShellChromeClient(bridge)
            val workers = ServiceWorkerController.getInstance()
            workers.setServiceWorkerClient(RefusingWorkers(gates))
            workers.serviceWorkerWebSettings.allowContentAccess = false
            workers.serviceWorkerWebSettings.allowFileAccess = false
            workers.serviceWorkerWebSettings.blockNetworkLoads = true
            CookieHandler.setDefault(null)
            activity.getSharedPreferences("CapWebViewSettings", Context.MODE_PRIVATE).edit().remove("serverBasePath").apply()
            return gates
        }

        /** A request refused: an empty answer that loads nothing. */
        fun refusal(): WebResourceResponse =
            WebResourceResponse("text/plain", "utf-8", 403, "Forbidden", mapOf("Cache-Control" to "no-store"), ByteArrayInputStream(ByteArray(0)))

        /** The Range header the local server reads: its own spelling first, as the server reads it. */
        fun rangeOf(headers: Map<String, String>?): String? = headers?.get("Range") ?: headers?.get("range")
    }
}

/**
 * What the Android shell adds, as a header, to every response of the bundle it serves: no frame and no object or embed from any address
 * (the diagram's frame is a srcdoc document, which loads from none). The bundle's own policy, in each page's meta element, is the one
 * both shells carry unchanged; a header's policy is enforced beside it, so a page holds to both.
 */
object ServedPolicy {
    const val POLICY = "frame-src 'none'; object-src 'none'"

    /** A served response's headers with POLICY added: as its own header, or as a second policy beside one already there. */
    fun policed(headers: Map<String, String>?): Map<String, String> {
        val out = LinkedHashMap(headers ?: emptyMap())
        val name = out.keys.firstOrNull { it.equals("Content-Security-Policy", ignoreCase = true) }
        if (name == null) out["Content-Security-Policy"] = POLICY else out[name] = "${out[name]}, $POLICY"
        return out
    }
}

/** The web view client: every navigation by LoadGate, every request by RequestGate, then the runtime's own, its answer carrying ServedPolicy's. */
class ShellWebViewClient(bridge: Bridge, private val activity: MainActivity, private val gates: ShellGates) : BridgeWebViewClient(bridge) {
    override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
        val url = request.url.toString()
        return when (val d = LoadGate.decide(request.isForMainFrame, request.hasGesture(), request.isRedirect, url, activity.foreground)) {
            is LoadGate.Decision.Load -> false
            is LoadGate.Decision.Cancel -> {
                gates.cancelled.incrementAndGet()
                true
            }
            is LoadGate.Decision.HandOff -> {
                gates.handedOff.incrementAndGet()
                activity.handOff(d.url)
                true
            }
        }
    }

    override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? {
        val u = request.url
        val headers = request.requestHeaders
        val document = RequestGate.document(request.isForMainFrame, headers)
        return when (RequestGate.decide(u.scheme, u.host, u.port, u.encodedPath, document, ShellGates.rangeOf(headers))) {
            RequestGate.Decision.SERVE -> super.shouldInterceptRequest(view, request)?.also { it.responseHeaders = ServedPolicy.policed(it.responseHeaders) }
            RequestGate.Decision.NETWORK -> null
            RequestGate.Decision.REFUSE -> {
                gates.refused.incrementAndGet()
                ShellGates.refusal()
            }
        }
    }
}

/** Service workers: every request refused (the shell's pages register none). */
class RefusingWorkers(private val gates: ShellGates) : ServiceWorkerClient() {
    override fun shouldInterceptRequest(request: WebResourceRequest): WebResourceResponse? {
        gates.workers.incrementAndGet()
        return ShellGates.refusal()
    }
}

/**
 * The chrome client: the runtime's own for an alert and a confirm, and the rest refused: every permission request (a camera, a
 * microphone, a protected media id, anything else) denied with no prompt, the location denied, no file chooser, every text-input prompt
 * answered with nothing, and no new window. The page's console is kept in the device's log in a debug build only.
 */
class ShellChromeClient(bridge: Bridge) : BridgeWebChromeClient(bridge) {
    override fun onPermissionRequest(request: PermissionRequest?) {
        request?.deny()
    }

    override fun onGeolocationPermissionsShowPrompt(origin: String?, callback: GeolocationPermissions.Callback?) {
        callback?.invoke(origin, false, false)
    }

    override fun onShowFileChooser(webView: WebView?, filePathCallback: ValueCallback<Array<Uri>>?, fileChooserParams: WebChromeClient.FileChooserParams?): Boolean = false

    override fun onJsPrompt(view: WebView?, url: String?, message: String?, defaultValue: String?, result: JsPromptResult?): Boolean {
        result?.cancel()
        return true
    }

    override fun onCreateWindow(view: WebView?, isDialog: Boolean, isUserGesture: Boolean, resultMsg: Message?): Boolean = false

    /** The page's console: in a debug build (the boot check's) to the device's log under its own tag; in any other build, nowhere. */
    override fun onConsoleMessage(consoleMessage: ConsoleMessage?): Boolean {
        if (BuildConfig.DEBUG && consoleMessage != null) {
            Log.i("ConductorPage", "${consoleMessage.messageLevel()} ${consoleMessage.message()} (${consoleMessage.sourceId()}:${consoleMessage.lineNumber()})")
            return true
        }
        return super.onConsoleMessage(consoleMessage)
    }
}

/**
 * An intent as the shell lets it in: a tap's message id and handle, a link only when it is an `https` address, nothing else (TapData).
 * What another app sends the shell's activity reaches neither the native runtime (which reads an intent's extras as its preferences)
 * nor its plugins nor the page in any other form. The intent stays the one it was in everything `Intent.filterEquals` compares (its
 * action, categories, type, package and component, and its data when that is kept), so whatever follows the activity by its intent
 * (the system's task, a test's scenario) still finds it; its extras, a data address that is not kept, its clip data and its grants of
 * access go.
 */
fun sanitized(intent: Intent?, component: ComponentName): Intent {
    val out = if (intent != null) Intent(intent) else Intent(Intent.ACTION_MAIN)
    out.component = component
    out.selector = null
    out.clipData = null
    out.flags = out.flags and (
        Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION or
            Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION or Intent.FLAG_GRANT_PREFIX_URI_PERMISSION
        ).inv()
    val link = TapData.link(intent?.action, intent?.dataString)
    if (link == null && out.data != null) out.data = null
    val extras = mutableMapOf<String, Any?>()
    try {
        val bundle = intent?.extras
        if (bundle != null) {
            for (k in listOf(TapData.MESSAGE_ID, TapData.HANDLE)) {
                @Suppress("DEPRECATION")
                extras[k] = bundle.get(k)
            }
        }
    } catch (e: Exception) {
        extras.clear()
    }
    out.replaceExtras(Bundle())
    for ((k, v) in TapData.keep(extras)) out.putExtra(k, v)
    return out
}
