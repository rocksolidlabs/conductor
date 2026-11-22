package dev.conductor.mobile

import android.content.Context
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.net.CookieHandler
import java.net.HttpURLConnection
import java.net.ResponseCache
import java.net.URL
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import javax.net.ssl.HttpsURLConnection

/**
 * The phone's connection for the native HTTP call: an HttpsURLConnection with the system's trust, following no redirect (the reply that
 * asks for one is the answer), with no cache, and sending and keeping no cookie: the process has no cookie handler (the shell's gates
 * take off the one the native runtime sets) and none is used while one is there. A reply's body is read only up to its bound, and the
 * whole exchange ends at two minutes, so a reply that never ends cannot hold the plugin's calls behind it.
 */
class UrlConnectionTransport(
    private val maxBody: Int = NativeHttpCalls.MAX_BODY,
    /** Opens the connection: an HttpsURLConnection on the phone (a test opens its own local server's). */
    private val opener: (String) -> HttpURLConnection? = { url -> URL(url).openConnection() as? HttpsURLConnection },
) : HttpTransport {
    private val watchdog = Executors.newSingleThreadScheduledExecutor { r -> Thread(r, "conductor-http-bound").apply { isDaemon = true } }

    override fun send(request: HttpRequest): HttpReply {
        if (CookieHandler.getDefault() != null || ResponseCache.getDefault() != null) {
            throw Refusal("native", "The phone's connection keeps cookies or a cache here: nothing was sent.")
        }
        val connection = opener(request.url) ?: throw Refusal("origin_refused", "Only an https address is sent: nothing was sent.")
        val bound = watchdog.schedule(Runnable { connection.disconnect() }, 120, TimeUnit.SECONDS)
        try {
            connection.instanceFollowRedirects = false
            connection.useCaches = false
            connection.allowUserInteraction = false
            connection.connectTimeout = 30_000
            connection.readTimeout = 60_000
            connection.requestMethod = request.method
            for ((k, v) in request.headers) connection.setRequestProperty(k, v)
            val body = request.body
            if (body != null) {
                connection.doOutput = true
                connection.setFixedLengthStreamingMode(body.size)
                connection.outputStream.use { it.write(body) }
            }
            val status = connection.responseCode
            val headers = linkedMapOf<String, String>()
            for ((k, v) in connection.headerFields) {
                if (k != null && v != null) headers[k] = v.joinToString(", ")
            }
            val stream = if (status >= 400) connection.errorStream else connection.inputStream
            val out = ByteArrayOutputStream()
            stream?.use { input ->
                val chunk = ByteArray(16 * 1024)
                while (true) {
                    val n = input.read(chunk)
                    if (n < 0) break
                    out.write(chunk, 0, n)
                    if (out.size() > maxBody) throw Refusal("too_large", "The reply is longer than $maxBody bytes.")
                }
            }
            return HttpReply(status, headers, request.url, out.toByteArray())
        } catch (r: Refusal) {
            throw r
        } catch (e: IOException) {
            throw Refusal("network", "The request could not be made: ${e.javaClass.simpleName}.")
        } finally {
            bound.cancel(false)
            connection.disconnect()
        }
    }
}

/** The origins the app's bundle names for the native HTTP call, read once: none when the file is absent, unreadable or malformed. */
fun bundledOrigins(context: Context): NativeHttpOrigins =
    NativeHttpOrigins.parse(
        try {
            context.assets.open(NativeHttpOrigins.ASSET).use { String(it.readBytes(), Charsets.UTF_8) }
        } catch (e: Exception) {
            null
        },
    )
