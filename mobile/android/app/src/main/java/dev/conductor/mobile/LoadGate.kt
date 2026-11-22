package dev.conductor.mobile

import java.net.URI

/** The shell's own page: the native runtime serves the bundle at this address, and nothing else is the app's. */
const val APP = "https://localhost"

/** An address a person may be handed to, out of the shell: https or http, with a host, no user part, at most 2,048 bytes, no control
 * character. Anything the parser cannot read is none. */
fun webAddress(url: String, schemes: Set<String> = setOf("https", "http")): Boolean {
    if (utf8Size(url) > 2048 || url.any { it.code < 0x20 || it.code in 0x7f..0x9f }) return false
    val u = try {
        URI(url)
    } catch (e: Exception) {
        return false
    }
    val scheme = u.scheme?.lowercase() ?: return false
    return scheme in schemes && !u.isOpaque && !u.host.isNullOrEmpty() && u.rawUserInfo == null
}

/**
 * The shell's decision over every navigation its web view asks about (the web view client's shouldOverrideUrlLoading), ahead of the
 * native runtime's own rule, which hands any address outside the app to any app that takes it:
 * - the main frame: the app's own address goes on (the runtime's local server answers it); a link the person activated (a navigation
 *   with the person's gesture, which a script's own click within a tap also has) to an `https` or `http` address, not a redirect, while
 *   the app is in the foreground, is handed to the system, which opens it as for a link tapped anywhere (the person's browser, or the
 *   app the system ties to that address); anything else is cancelled (a location change, a form, a redirect, every other scheme);
 * - a frame the web view asks about (it asks only for schemes that are not http or https): `about:srcdoc` (the diagram's frame) and
 *   `about:blank` load; anything else is cancelled. A frame's http or https document is refused where it is requested (RequestGate),
 *   and the page's own policy names no frame source.
 * No other scheme is ever handed on (`intent:`, `tel:`, `sms:`, `mailto:`, `market:`, `file:`, `content:`, `data:`, `blob:`,
 * `javascript:`, `about:`, a custom one).
 */
object LoadGate {
    sealed class Decision {
        object Load : Decision()
        object Cancel : Decision()
        class HandOff(val url: String) : Decision()
    }

    /**
     * Whether an address is the app's own page, read as the string the web view hands over (it writes the scheme and host in lower case,
     * and never decodes): the app's address exactly, or it followed by `/`, `?` or `#`. A host with a port, a user part, a longer name
     * or another scheme is not the app's.
     */
    fun own(url: String): Boolean =
        url == APP || (url.length > APP.length && url.startsWith(APP) && url[APP.length] in "/?#")

    fun decide(mainFrame: Boolean, gesture: Boolean, redirect: Boolean, url: String, foreground: Boolean): Decision {
        if (!mainFrame) return if (url == "about:srcdoc" || url == "about:blank") Decision.Load else Decision.Cancel
        if (own(url)) return Decision.Load
        return if (gesture && !redirect && foreground && webAddress(url)) Decision.HandOff(url) else Decision.Cancel
    }
}
