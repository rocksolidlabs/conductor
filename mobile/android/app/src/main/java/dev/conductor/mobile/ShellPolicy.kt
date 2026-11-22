package dev.conductor.mobile

import org.json.JSONArray

/**
 * What an intent may carry into the shell (its one exported activity takes intents from any app on the phone, and the push service's
 * notification taps): a tap's message id and its handle `h` when it is a string of at most 256 bytes (the page opens the inbox for any
 * other tap), and a link only when it is an `https` address of at most 2,048 bytes; nothing else of an intent reaches the native
 * runtime, its plugins or the page.
 */
object TapData {
    const val MESSAGE_ID = "google.message_id"
    const val HANDLE = "h"
    const val MAX_HANDLE = 256
    const val MAX_MESSAGE_ID = 256
    const val VIEW = "android.intent.action.VIEW"

    /** The extras kept from an intent's: a tap's message id, and its handle when it is one; nothing when it is no tap. */
    fun keep(extras: Map<String, Any?>): Map<String, String> {
        val id = extras[MESSAGE_ID] as? String ?: return emptyMap()
        if (id.isEmpty() || utf8Size(id) > MAX_MESSAGE_ID) return emptyMap()
        val out = linkedMapOf(MESSAGE_ID to id)
        val h = extras[HANDLE] as? String
        if (h != null && h.isNotEmpty() && utf8Size(h) <= MAX_HANDLE) out[HANDLE] = h
        return out
    }

    /** The link kept from an intent: its address when the intent is a view of an `https` address; none otherwise. */
    fun link(action: String?, data: String?): String? =
        if (action == VIEW && data != null && webAddress(data, setOf("https"))) data else null
}

/**
 * The shell's policy, checked at every start (the plan's boot check, on the phone itself): the build and the runtime are what the shell
 * was made for, or the shell loads nothing and says why. Each check answers words, one finding a line; none means the shell may load.
 */
object ShellPolicy {
    /** The native runtime's plugins the build may hold (the app's assets/capacitor.plugins.json, written by `cap sync`): these alone. */
    val RUNTIME_PLUGINS = setOf(
        "com.capacitorjs.plugins.app.AppPlugin",
        "com.capacitorjs.plugins.browser.BrowserPlugin",
        "com.capacitorjs.plugins.pushnotifications.PushNotificationsPlugin",
    )

    /** The plugin each name the page may reach is served by, and the stand-ins: what the built app's bridge holds, by class. */
    val PLUGINS = mapOf(
        "ConductorKey" to "dev.conductor.mobile.ConductorKeyPlugin",
        "ConductorStore" to "dev.conductor.mobile.ConductorStorePlugin",
        "CapacitorHttp" to "dev.conductor.mobile.ShellHttpPlugin",
        "WebView" to "dev.conductor.mobile.RefusedWebViewPlugin",
        "CapacitorCookies" to "dev.conductor.mobile.RefusedCookiesPlugin",
        "App" to "com.capacitorjs.plugins.app.AppPlugin",
        "Browser" to "com.capacitorjs.plugins.browser.BrowserPlugin",
        "PushNotifications" to "com.capacitorjs.plugins.pushnotifications.PushNotificationsPlugin",
    )

    /** What is known before the native runtime starts. */
    class Before(
        val messageListener: Boolean,
        val documentStartScript: Boolean,
        val allowBackup: Boolean,
        val cleartext: Boolean,
        val pluginsJson: String?,
    )

    /** What the native runtime started with, read from it. */
    class After(
        val scheme: String?,
        val host: String?,
        val serverUrl: String?,
        val allowNavigation: List<String>,
        val mixedContent: Boolean,
        val legacyBridge: Boolean,
        val logging: Boolean,
        val serviceWorkers: Boolean,
        val debugging: Boolean,
        val debuggable: Boolean,
        val cookieHandler: Boolean,
        val plugins: Map<String, String?>,
    )

    /** The classes a build's capacitor.plugins.json names; null when it is not that file's form. */
    fun pluginClasses(json: String?): Set<String>? {
        if (json == null) return null
        return try {
            val list = JSONArray(json)
            val out = mutableSetOf<String>()
            for (i in 0 until list.length()) {
                val o = list.getJSONObject(i)
                out.add(o.opt("classpath") as? String ?: return null)
            }
            out
        } catch (e: Exception) {
            null
        }
    }

    fun before(s: Before): List<String> {
        val out = mutableListOf<String>()
        if (!s.messageListener) out.add("This phone's web view cannot tell the shell's page from another: update Android System WebView.")
        if (!s.documentStartScript) out.add("This phone's web view cannot start the shell's page as it must: update Android System WebView.")
        if (s.allowBackup) out.add("This build lets the app be backed up.")
        if (s.cleartext) out.add("This build lets the app send cleartext.")
        val classes = pluginClasses(s.pluginsJson)
        if (classes != RUNTIME_PLUGINS) out.add("This build's plugins are not the shell's: ${classes?.sorted() ?: "unreadable"}.")
        return out
    }

    fun after(s: After): List<String> {
        val out = mutableListOf<String>()
        if (s.scheme != "https" || s.host != "localhost" || s.serverUrl != null) out.add("The native runtime does not serve the shell's bundle at its own address.")
        if (s.allowNavigation.isNotEmpty()) out.add("The native runtime lets the page navigate beyond its bundle.")
        if (s.mixedContent) out.add("The native runtime lets the page load cleartext.")
        if (s.legacyBridge) out.add("The native runtime uses a message channel every frame can reach.")
        if (s.logging) out.add("The native runtime logs the page's calls.")
        if (s.serviceWorkers) out.add("The native runtime serves service workers from its bundle outside the shell's gate.")
        if (s.debugging != s.debuggable) out.add("The web view's debugging does not match the build.")
        if (s.cookieHandler) out.add("The process keeps a cookie handler.")
        for ((name, cls) in PLUGINS) {
            if (s.plugins[name] != cls) out.add("The plugin $name is ${s.plugins[name] ?: "missing"}, not the shell's.")
        }
        return out
    }
}

/** The app's components as the system holds them: which are exported, and what permission guards each. */
object Exported {
    class Component(val name: String, val exported: Boolean, val permission: String?)

    /**
     * The exported components the shell's app may have, each with the permission that must guard it (none for the activity, which a
     * notification's tap and the launcher open): the push service's receiver (only the platform's push service holds its permission)
     * and the profile installer's (only the system's debugging bridge holds its).
     */
    val ALLOWED = mapOf(
        "dev.conductor.mobile.MainActivity" to null,
        "com.google.firebase.iid.FirebaseInstanceIdReceiver" to "com.google.android.c2dm.permission.SEND",
        "androidx.profileinstaller.ProfileInstallReceiver" to "android.permission.DUMP",
    )

    fun findings(components: List<Component>): List<String> = components.filter { it.exported }.mapNotNull { c ->
        when {
            !ALLOWED.containsKey(c.name) -> "${c.name} is exported"
            ALLOWED[c.name] != null && c.permission != ALLOWED[c.name] -> "${c.name} is exported without ${ALLOWED[c.name]}"
            else -> null
        }
    }
}
