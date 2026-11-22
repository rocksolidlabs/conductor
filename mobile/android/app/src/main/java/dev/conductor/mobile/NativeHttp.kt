package dev.conductor.mobile

import org.json.JSONArray
import org.json.JSONObject
import java.net.URI
import java.nio.ByteBuffer
import java.nio.charset.CharacterCodingException
import java.nio.charset.CodingErrorAction

// The native HTTP call on Android (the bridge's `http.request`, `CapacitorHttp` by name, as on the iPhone): a request is sent only when
// its address is https on an origin the packaging named at build (`make mobile-sync MOBILE_NATIVE_ORIGINS=...`, written into the app's
// bundle and read once at start), and anything else is refused before anything is opened. It keeps no cookie, no cache and no
// credential, follows no redirect, and reads a reply only up to 1 MiB. This repository's build names none: a shell with no provider
// makes no native HTTP call.

/** The origins a packaging names for the native HTTP call: none unless the bundle's file names them, each exactly. */
class NativeHttpOrigins private constructor(private val origins: Set<Origin>) {
    data class Origin(val host: String, val port: Int)

    val isEmpty: Boolean get() = origins.isEmpty()

    companion object {
        val NONE = NativeHttpOrigins(emptySet())

        /** Where `make mobile-sync` writes the list in the bundle, which `cap sync` copies into the app's assets. */
        const val ASSET = "public/shell/native-http.json"

        /** `{"origins": ["https://host[:port]", ...]}`: any other shape, or any entry not exactly an https origin, is none. */
        fun parse(text: String?): NativeHttpOrigins {
            if (text == null) return NONE
            return try {
                val o = JSONObject(text)
                if (o.length() != 1) return NONE
                val list = o.opt("origins") as? JSONArray ?: return NONE
                val out = mutableSetOf<Origin>()
                for (i in 0 until list.length()) {
                    val s = list.opt(i) as? String ?: return NONE
                    out.add(exactly(s) ?: return NONE)
                }
                NativeHttpOrigins(out)
            } catch (e: Exception) {
                NONE
            }
        }

        /**
         * A host as the shell names one: ASCII labels of a-z, 0-9 and '-', none empty (so no trailing dot), and a name, never an
         * address (no host whose every label reads as a number, as an IPv4 address in any of its spellings does), so the name judged is
         * the name the connection asks the system to resolve.
         */
        fun plainHost(h: String): Boolean {
            val labels = h.split(".")
            if (labels.isEmpty() || labels.any { l -> l.isEmpty() || !l.all { it in 'a'..'z' || it in '0'..'9' || it == '-' } }) return false
            val numeric = labels.all { l -> l.all { it in '0'..'9' } || (l.startsWith("0x") && l.drop(2).all { it in '0'..'9' || it in 'a'..'f' }) }
            return !numeric
        }

        /** s exactly an https origin as the sync writes one: `https://`, a plain lower-case host, and a port only when not 443. */
        fun exactly(s: String): Origin? {
            if (!s.startsWith("https://")) return null
            val parts = s.removePrefix("https://").split(":")
            if (parts.size > 2 || !plainHost(parts[0])) return null
            var port = 443
            if (parts.size == 2) {
                val p = parts[1].toIntOrNull() ?: return null
                if (p.toString() != parts[1] || p <= 0 || p > 65535 || p == 443) return null
                port = p
            }
            return Origin(parts[0], port)
        }
    }

    /**
     * The address a request to s is sent to: s's own path and query on its origin, when s is https on a named origin. A user part, a host
     * that is not plain (escaped, not ASCII, a trailing dot, an address), another scheme, another port or another host: refused.
     */
    fun admit(s: String): String {
        if (isEmpty) throw Refusal("origin_refused", "This shell's packaging names no origin for its native HTTP call: nothing was sent.")
        val refused = Refusal("origin_refused", "Only an https address on an origin this shell's packaging names is sent: nothing was sent.")
        val u = try {
            URI(s)
        } catch (e: Exception) {
            throw refused
        }
        val scheme = u.scheme ?: throw refused
        if (!scheme.equals("https", ignoreCase = true) || u.rawUserInfo != null || u.isOpaque) throw refused
        val rawHost = u.host ?: throw refused
        val host = rawHost.lowercase()
        val port = if (u.port == -1) 443 else u.port
        // The authority exactly the host, or the host and its port: nothing the parser set aside.
        val authority = u.rawAuthority ?: throw refused
        if (authority != rawHost && authority != "$rawHost:${u.port}") throw refused
        if (!plainHost(host) || Origin(host, port) !in origins) throw refused
        val path = u.rawPath?.ifEmpty { "/" } ?: "/"
        val query = u.rawQuery?.let { "?$it" } ?: ""
        return "https://" + host + (if (port == 443) "" else ":$port") + path + query
    }
}

/** A request as the native HTTP call sends it: the admitted address, the method, the headers the page set and the body. */
class HttpRequest(val url: String, val method: String, val headers: Map<String, String>, val body: ByteArray?)

/** A reply as received: its status, its headers by name, the address it came from, and its body (at most the bound). */
class HttpReply(val status: Int, val headers: Map<String, String>, val url: String, val body: ByteArray)

/** What sends an admitted request: the phone's own connection, or a test's fake. It throws `Refusal` for `too_large` and `network`. */
interface HttpTransport {
    fun send(request: HttpRequest): HttpReply
}

/**
 * The native HTTP plugin's calls, by the names of the runtime's own (`request`, and `get` to `delete` with their method): the address
 * whole (`url`), `method`, `headers` (strings) and `data` (a string); answered `{status, headers, url, data}`, the headers' names in
 * lower case and the body as its text (null when it is not UTF-8). `params` is refused: the address is taken whole. A header that
 * names where the request goes or how it is framed, a name that is not an HTTP token and a value with a control character are refused,
 * so that what the gate judged (the address) is what the request is sent to.
 */
class NativeHttpCalls(val origins: NativeHttpOrigins, private val transport: HttpTransport) : ShellCalls {
    companion object {
        const val MAX_BODY = 1024 * 1024
        val METHODS = listOf("request", "get", "post", "put", "patch", "delete")
        val VERBS = setOf("GET", "HEAD", "POST", "PUT", "PATCH", "DELETE")
        /** The headers, by their names in lower case, that say where a request goes or how it is framed: the connection's alone. */
        val FRAMING = setOf("host", "connection", "keep-alive", "content-length", "transfer-encoding", "te", "upgrade", "trailer")
        private const val TOKEN_SYMBOLS = "!#$%&'*+-.^_`|~"

        /** Whether the page may set this header: its name an HTTP token and none of the framing ones, its value with no control character. */
        fun headerAllowed(name: String, value: String): Boolean {
            if (name.isEmpty() || !name.all { it in '0'..'9' || it in 'a'..'z' || it in 'A'..'Z' || it in TOKEN_SYMBOLS }) return false
            val lower = name.lowercase()
            if (lower in FRAMING || lower.startsWith("proxy-")) return false
            return value.all { it.code >= 0x20 && it.code != 0x7f }
        }

        /** The body as its text when it is UTF-8, null when it is not. */
        fun text(body: ByteArray): String? = try {
            Charsets.UTF_8.newDecoder()
                .onMalformedInput(CodingErrorAction.REPORT)
                .onUnmappableCharacter(CodingErrorAction.REPORT)
                .decode(ByteBuffer.wrap(body)).toString()
        } catch (e: CharacterCodingException) {
            null
        }
    }

    override fun call(method: String, options: JSONObject): CallAnswer {
        val verb = when (method) {
            "request" -> ((options.opt("method") as? String) ?: "GET").uppercase()
            "get", "post", "put", "patch", "delete" -> method.uppercase()
            else -> return CallAnswer.Reject("unknown_call", "The native HTTP call has no call $method.")
        }
        if (verb !in VERBS) return invalid("A request's method is one of GET, HEAD, POST, PUT, PATCH and DELETE.")
        if (options.has("method") && options.opt("method") !is String) return invalid("A request's method is a string.")
        val address = options.opt("url") as? String ?: return invalid("A request takes url, a string.")
        val params = options.opt("params")
        if (params != null && params != JSONObject.NULL && !(params is JSONObject && params.length() == 0)) {
            return invalid("A request takes its address whole, with no params.")
        }
        val headers = linkedMapOf<String, String>()
        val h = options.opt("headers")
        if (h != null && h != JSONObject.NULL) {
            if (h !is JSONObject) return invalid("A request's headers are an object of strings.")
            for (k in h.keys()) {
                val v = h.opt(k) as? String ?: return invalid("A request's headers are an object of strings.")
                if (!headerAllowed(k, v)) {
                    return invalid("A request's header is not the page's to set, or is not a header: where a request goes and how it is framed are the connection's.")
                }
                headers[k] = v
            }
        }
        var body: ByteArray? = null
        val d = options.opt("data")
        if (d != null && d != JSONObject.NULL) {
            if (d !is String) return invalid("A request's data is a string.")
            body = d.toByteArray(Charsets.UTF_8)
        }
        val url = try {
            origins.admit(address)
        } catch (r: Refusal) {
            return CallAnswer.Reject(r.code, r.words)
        }
        return try {
            val reply = transport.send(HttpRequest(url, verb, headers, body))
            val out = JSONObject()
            for ((k, v) in reply.headers) out.put(k.lowercase(), v)
            CallAnswer.Resolve(
                JSONObject()
                    .put("status", reply.status)
                    .put("headers", out)
                    .put("url", reply.url)
                    .put("data", text(reply.body) ?: JSONObject.NULL),
            )
        } catch (r: Refusal) {
            CallAnswer.Reject(r.code, r.words)
        } catch (e: Exception) {
            CallAnswer.Reject("network", "The request could not be made: ${e.javaClass.simpleName}.")
        }
    }
}
