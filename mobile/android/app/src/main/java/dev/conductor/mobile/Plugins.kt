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
