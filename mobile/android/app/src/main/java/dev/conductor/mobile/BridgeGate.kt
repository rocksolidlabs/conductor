package dev.conductor.mobile

import org.json.JSONObject

/**
 * The gate at the native runtime's message channel (`androidBridge`, the web message listener the runtime adds), the one way a page's
 * script calls native code by message. The runtime parses a message as a JSON object and reads `type` (`cordova`, a Cordova call;
 * `js.error`, a log line; anything else a plugin call), `callbackId`, `pluginId`, `methodName` and `options`, finds the plugin by its
 * name and the method by its own, and answers through the main frame. The gate takes the channel's name over and forwards a message
 * only when:
 * - it comes from the shell's own page: the main frame, at the app's own origin (`https://localhost`), while that frame holds the
 *   app's own address (never a `blob:` or `data:` document of the same origin);
 * - it is a JSON object with no key but `callbackId`, `pluginId`, `methodName` and `options` (no `type`: the runtime's page script
 *   sends none for a plugin call, so no Cordova call and no log line);
 * - its plugin and method are one pair of a closed table, the calls the bridge (mobile/src/bridge.ts) makes and the listener calls the
 *   runtime's page script makes for the plugins the bridge listens to;
 * - its `callbackId` is of the runtime's own form, `-1` or 1 to 16 digits;
 * - its `options` is an object or absent, and for the calls of the runtime's own plugins, whose options their code reads, exactly the
 *   form the bridge gives them: a listener call an `eventName` the bridge listens for on that plugin (and for a removal the
 *   listener's `callbackId`), `Browser.open` a `url` alone that is `https` or `http`, and every other call of theirs nothing.
 * Everything else is dropped.
 */
object BridgeGate {
    /** The runtime's channel name, which the gate takes over. */
    const val HANDLER = "androidBridge"

    /** The calls the page makes: each plugin with its methods, by the names the page uses. */
    val calls: Map<String, Set<String>> = mapOf(
        "ConductorKey" to setOf("create", "publicKey", "state", "bind", "sign", "ending", "remove"),
        "ConductorStore" to setOf("put", "get", "list", "remove", "clear"),
        "CapacitorHttp" to setOf("request"),
        "PushNotifications" to setOf("checkPermissions", "requestPermissions", "register", "addListener", "removeListener"),
        "App" to setOf("getLaunchUrl", "addListener", "removeListener"),
        "Browser" to setOf("open"),
    )

    /** The events the bridge listens for, by plugin: the only `eventName` a listener call may carry. */
    val events: Map<String, Set<String>> = mapOf(
        "PushNotifications" to setOf("registration", "pushNotificationActionPerformed"),
        "App" to setOf("appUrlOpen"),
    )

    /** The shell's own plugins, whose calls decode and judge their options themselves. */
    val own: Set<String> = setOf("ConductorKey", "ConductorStore", "CapacitorHttp")

    /** The keys a message may carry. */
    val keys: Set<String> = setOf("callbackId", "pluginId", "methodName", "options")

    /** A message's bound, in characters: past it, nothing is parsed (a cache record of 1 MiB fits many times over). */
    const val MAX_MESSAGE = 8 * 1024 * 1024

    /** Who sent a message: the frame, its origin, and the address the main frame holds. */
    data class Sender(val mainFrame: Boolean, val scheme: String?, val host: String?, val port: Int, val page: String?)

    /** A call the gate let through: what the shell may still answer itself before the runtime is asked. */
    data class Call(val pluginId: String, val methodName: String, val callbackId: String)

    /** Whether the sender is the shell's own page. */
    fun fromShell(s: Sender): Boolean =
        s.mainFrame && s.scheme == "https" && s.host == "localhost" && s.port == -1 && s.page != null && LoadGate.own(s.page)

    /** `-1`, or 1 to 16 digits: the forms the runtime's page script gives a call. */
    fun callbackForm(s: String): Boolean = s == "-1" || (s.length in 1..16 && s.all { it in '0'..'9' })

    /** The call a message makes when the gate admits it, its sender aside; null when it is dropped. */
    fun read(raw: String?): Call? {
        if (raw == null || raw.length > MAX_MESSAGE) return null
        val o = try {
            JSONObject(raw)
        } catch (e: Exception) {
            return null
        }
        for (k in o.keys()) if (k !in keys) return null
        val plugin = o.opt("pluginId") as? String ?: return null
        val method = o.opt("methodName") as? String ?: return null
        val callback = o.opt("callbackId") as? String ?: return null
        if (calls[plugin]?.contains(method) != true || !callbackForm(callback)) return null
        var options = JSONObject()
        if (o.has("options")) options = o.opt("options") as? JSONObject ?: return null
        if (plugin !in own && !closedForm(plugin, method, options)) return null
        return Call(plugin, method, callback)
    }

    /** A runtime plugin's call carries exactly the options its code reads, each in the form the bridge gives it. */
    private fun closedForm(plugin: String, method: String, o: JSONObject): Boolean {
        val names = mutableSetOf<String>()
        for (k in o.keys()) names.add(k)
        return when (method) {
            "addListener" -> names == setOf("eventName") && (o.opt("eventName") as? String)?.let { events[plugin]?.contains(it) } == true
            "removeListener" -> names == setOf("eventName", "callbackId") &&
                (o.opt("eventName") as? String)?.let { events[plugin]?.contains(it) } == true &&
                (o.opt("callbackId") as? String)?.let { callbackForm(it) } == true
            "open" -> plugin == "Browser" && names == setOf("url") && (o.opt("url") as? String)?.let { webAddress(it) } == true
            else -> names.isEmpty()
        }
    }

    /** The whole decision: the sender, then the message. */
    fun admits(raw: String?, sender: Sender): Call? = if (fromShell(sender)) read(raw) else null
}
