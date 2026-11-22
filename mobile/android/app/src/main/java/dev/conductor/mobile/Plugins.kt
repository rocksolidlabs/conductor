package dev.conductor.mobile

import com.getcapacitor.JSObject
import com.getcapacitor.Plugin
import com.getcapacitor.PluginCall
import com.getcapacitor.PluginMethod
import com.getcapacitor.annotation.CapacitorPlugin

// The shell's plugins, a few lines each: every call handed, in the order it arrives, to the calls that decode it, make it and answer it
// (StoreCalls, KeyCalls, NativeHttpCalls), tested without the app. A refusal reaches the page as a rejected error with `code`, its
// words as the message, and `words` beside them under `data` (the native runtime's place for a rejection's fields on Android; the
// bridge reads them from there as from the top on the iPhone).

/** A call's answer handed back: resolved with its value, or rejected with its words, its code and its words as a field. */
fun PluginCall.answer(a: CallAnswer) {
    when (a) {
        is CallAnswer.Resolve -> resolve(JSObject.fromJSONObject(a.value))
        is CallAnswer.Reject -> {
            val data = JSObject()
            data.put("words", a.words)
            reject(a.words, a.code, data)
        }
    }
}

/** The key plugin, `ConductorKey` to the page: the person is asked in the shell's own activity. */
@CapacitorPlugin(name = "ConductorKey")
class ConductorKeyPlugin : Plugin() {
    private val runner by lazy { CallRunner { KeyCalls(deviceKey(activity)) } }

    @PluginMethod
    fun create(call: PluginCall) {
        make(call, "create")
    }

    @PluginMethod
    fun publicKey(call: PluginCall) {
        make(call, "publicKey")
    }

    @PluginMethod
    fun state(call: PluginCall) {
        make(call, "state")
    }

    @PluginMethod
    fun bind(call: PluginCall) {
        make(call, "bind")
    }

    @PluginMethod
    fun sign(call: PluginCall) {
        make(call, "sign")
    }

    @PluginMethod
    fun ending(call: PluginCall) {
        make(call, "ending")
    }

    @PluginMethod
    fun remove(call: PluginCall) {
        make(call, "remove")
    }

    private fun make(call: PluginCall, method: String) {
        runner.submit(method, call.data) { call.answer(it) }
    }
}

/** The store plugin, `ConductorStore` to the page. */
@CapacitorPlugin(name = "ConductorStore")
class ConductorStorePlugin : Plugin() {
    private val runner by lazy { CallRunner { StoreCalls(deviceStore(context) { bridge?.webView }) } }

    @PluginMethod
    fun put(call: PluginCall) {
        make(call, "put")
    }

    @PluginMethod
    fun get(call: PluginCall) {
        make(call, "get")
    }

    @PluginMethod
    fun list(call: PluginCall) {
        make(call, "list")
    }

    @PluginMethod
    fun remove(call: PluginCall) {
        make(call, "remove")
    }

    @PluginMethod
    fun clear(call: PluginCall) {
        make(call, "clear")
    }

    private fun make(call: PluginCall, method: String) {
        runner.submit(method, call.data) { call.answer(it) }
    }
}

/** The native HTTP call, `CapacitorHttp` to the page as the runtime's own is (the runtime keeps one plugin a name, the last registered). */
@CapacitorPlugin(name = "CapacitorHttp")
class ShellHttpPlugin : Plugin() {
    private val runner by lazy { CallRunner { NativeHttpCalls(bundledOrigins(context), UrlConnectionTransport()) } }

    @PluginMethod
    fun request(call: PluginCall) {
        make(call, "request")
    }

    @PluginMethod
    fun get(call: PluginCall) {
        make(call, "get")
    }

    @PluginMethod
    fun post(call: PluginCall) {
        make(call, "post")
    }

    @PluginMethod
    fun put(call: PluginCall) {
        make(call, "put")
    }

    @PluginMethod
    fun patch(call: PluginCall) {
        make(call, "patch")
    }

    @PluginMethod
    fun delete(call: PluginCall) {
        make(call, "delete")
    }

    private fun make(call: PluginCall, method: String) {
        runner.submit(method, call.data) { call.answer(it) }
    }
}

/**
 * The native runtime's own WebView and CapacitorCookies plugins the shell does not offer a page: WebView would load the shell from
 * another folder, outside its bundle and its policy, and keep that folder for every later start; CapacitorCookies reaches the web view's
 * cookie jar. The shell registers a stand-in under each name, which answers every call of the runtime's own list refused. The bridge
 * gate never forwards a call to either: the stand-ins are a second wall.
 */
object RefusedPlugins {
    val WEB_VIEW = listOf("setServerAssetPath", "setServerBasePath", "getServerBasePath", "persistServerBasePath")
    val COOKIES = listOf("getCookies", "setCookie", "deleteCookie", "clearCookies", "clearAllCookies")

    fun answer(plugin: String, method: String): CallAnswer = CallAnswer.Reject("not_in_shell", "The shell does not offer $plugin.$method.")
}

@CapacitorPlugin(name = "WebView")
class RefusedWebViewPlugin : Plugin() {
    @PluginMethod
    fun setServerAssetPath(call: PluginCall) {
        call.answer(RefusedPlugins.answer("WebView", "setServerAssetPath"))
    }

    @PluginMethod
    fun setServerBasePath(call: PluginCall) {
        call.answer(RefusedPlugins.answer("WebView", "setServerBasePath"))
    }

    @PluginMethod
    fun getServerBasePath(call: PluginCall) {
        call.answer(RefusedPlugins.answer("WebView", "getServerBasePath"))
    }

    @PluginMethod
    fun persistServerBasePath(call: PluginCall) {
        call.answer(RefusedPlugins.answer("WebView", "persistServerBasePath"))
    }
}

@CapacitorPlugin(name = "CapacitorCookies")
class RefusedCookiesPlugin : Plugin() {
    @PluginMethod
    fun getCookies(call: PluginCall) {
        call.answer(RefusedPlugins.answer("CapacitorCookies", "getCookies"))
    }

    @PluginMethod
    fun setCookie(call: PluginCall) {
        call.answer(RefusedPlugins.answer("CapacitorCookies", "setCookie"))
    }

    @PluginMethod
    fun deleteCookie(call: PluginCall) {
        call.answer(RefusedPlugins.answer("CapacitorCookies", "deleteCookie"))
    }

    @PluginMethod
    fun clearCookies(call: PluginCall) {
        call.answer(RefusedPlugins.answer("CapacitorCookies", "clearCookies"))
    }

    @PluginMethod
    fun clearAllCookies(call: PluginCall) {
        call.answer(RefusedPlugins.answer("CapacitorCookies", "clearAllCookies"))
    }
}
