package dev.conductor.mobile

/**
 * The shell's decision over every request its web view makes (the web view client's shouldInterceptRequest, and the service workers'),
 * ahead of the native runtime's local server. The server answers any address of its host (`localhost`, over `https` and `http`) from
 * the bundle, reading the decoded path, and under its prefixes any file the app can read (`/_capacitor_file_`), any content address
 * (`/_capacitor_content_`) and a proxy (`/_capacitor_http_interceptor_`); a Range header it reads with `Integer.parseInt` of its end,
 * which nothing catches. So:
 * - an address of the app's own origin (`https://localhost`, no port) goes to the server only when its path is spelled with the
 *   characters decoding leaves unchanged and the bundle's files and the shell's routes use (letters, digits, `.`, `_`, `-`, `/`; the
 *   query is not read as the path), with no `.` or `..` segment and under none of the server's prefixes, and when a `Range` header it
 *   carries is of the one form the server reads without failing (`bytes=<from>-<to>`, `<to>` optional and, when given, a number the
 *   server's parse takes: at most 2,147,483,647): the string judged is the string the server reads;
 * - any other address of that host (over `http`, with a port) is refused;
 * - a document of any other origin is refused, in the main frame or in a frame (the web view asks its client about a frame's
 *   navigation only for schemes that are not http or https, so a frame is stopped here, where its document is requested; a request
 *   is taken as a document's as the runtime itself tells one, by the main frame or the navigation's own header, and also by an Accept
 *   that asks for HTML, the stricter reading);
 * - any other request (a provider's own origin, which the page's policy bounds) goes to the network as the web view makes it.
 */
object RequestGate {
    enum class Decision { SERVE, NETWORK, REFUSE }

    val prefixes = listOf("/_capacitor_file_", "/_capacitor_content_", "/_capacitor_http_interceptor_", "/_capacitor_https_interceptor_")

    private val spelling = Regex("^[/A-Za-z0-9._-]*$")
    private val range = Regex("^bytes=[0-9]{1,15}-([0-9]{1,15})?$")

    /** Whether a request is a document's: the main frame's, a navigation's (its Upgrade-Insecure-Requests header), or one asking for HTML. */
    fun document(mainFrame: Boolean, headers: Map<String, String>?): Boolean {
        if (mainFrame) return true
        for ((k, v) in headers ?: emptyMap()) {
            if (k.equals("Upgrade-Insecure-Requests", ignoreCase = true)) return true
            if (k.equals("Accept", ignoreCase = true) && v.contains("text/html", ignoreCase = true)) return true
        }
        return false
    }

    /** A Range header the local server reads without failing: `bytes=<from>-<to>`, `<to>` absent or an int its parse takes. */
    fun rangeReadable(header: String): Boolean {
        val m = range.matchEntire(header) ?: return false
        val end = m.groupValues[1]
        return end.isEmpty() || end.toIntOrNull() != null
    }

    fun decide(scheme: String?, host: String?, port: Int, encodedPath: String?, document: Boolean, rangeHeader: String?): Decision {
        if (host != null && host.equals("localhost", ignoreCase = true)) {
            if (scheme != "https" || host != "localhost" || port != -1) return Decision.REFUSE
            val path = if (encodedPath.isNullOrEmpty()) "/" else encodedPath
            if (!spelling.matches(path)) return Decision.REFUSE
            if (path.split("/").any { it == "." || it == ".." }) return Decision.REFUSE
            if (prefixes.any { path.startsWith(it) }) return Decision.REFUSE
            if (rangeHeader != null && !rangeReadable(rangeHeader)) return Decision.REFUSE
            return Decision.SERVE
        }
        return if (document) Decision.REFUSE else Decision.NETWORK
    }
}
