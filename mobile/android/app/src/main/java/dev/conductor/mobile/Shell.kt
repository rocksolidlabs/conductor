package dev.conductor.mobile

import org.json.JSONObject
import java.text.ParsePosition
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.TimeZone

// What the shell's plugins share (docs/plans/phone-shell.md, OSS-3): a call's answer as the bridge (mobile/src/bridge.ts) reads it, a
// refusal's code and words, and the forms of a time and of a key the page is handed. Nothing here touches the platform, so the JVM
// tests run it as the phone does.

/** What a call answers the page: a value to resolve with, or a refusal's code and words to reject with. */
sealed class CallAnswer {
    class Resolve(val value: JSONObject) : CallAnswer()
    class Reject(val code: String, val words: String) : CallAnswer()
}

/** A refusal with its code and words, passed to the page as they are. */
open class Refusal(val code: String, val words: String) : Exception(words)

/** What a plugin hands its calls to: the key's, the store's, the native HTTP call's. */
interface ShellCalls {
    fun call(method: String, options: JSONObject): CallAnswer
}

/** The refusal of a call whose arguments are not what it takes. */
fun invalid(words: String): CallAnswer = CallAnswer.Reject("invalid_request", words)

/** Any error as a refusal: one of the shell's with its own code, anything else `native`. */
fun refusalOf(e: Throwable): CallAnswer =
    if (e is Refusal) CallAnswer.Reject(e.code, e.words) else CallAnswer.Reject("native", "The phone could not do it: ${e.javaClass.simpleName}.")

/** The answer of a call the plugin could not make at all: what it keeps could not be opened on this phone. */
fun unopened(e: Throwable): CallAnswer = CallAnswer.Reject("native", "The phone could not open what it keeps: ${e.javaClass.simpleName}.")

/** A string's UTF-8 length, in bytes. */
fun utf8Size(s: String): Int = s.toByteArray(Charsets.UTF_8).size

/** Base64 (RFC 4648), standard and url-safe without padding: written here because the platform's own needs Android 8. */
object Base64Url {
    private const val STANDARD = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
    private const val URL = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

    private fun encode(bytes: ByteArray, alphabet: String, pad: Boolean): String {
        val out = StringBuilder((bytes.size + 2) / 3 * 4)
        var i = 0
        while (i < bytes.size) {
            val b0 = bytes[i].toInt() and 0xff
            val b1 = if (i + 1 < bytes.size) bytes[i + 1].toInt() and 0xff else 0
            val b2 = if (i + 2 < bytes.size) bytes[i + 2].toInt() and 0xff else 0
            out.append(alphabet[b0 ushr 2])
            out.append(alphabet[((b0 and 0x03) shl 4) or (b1 ushr 4)])
            if (i + 1 < bytes.size) out.append(alphabet[((b1 and 0x0f) shl 2) or (b2 ushr 6)]) else if (pad) out.append('=')
            if (i + 2 < bytes.size) out.append(alphabet[b2 and 0x3f]) else if (pad) out.append('=')
            i += 3
        }
        return out.toString()
    }

    /** Base64url with no padding: a key as the page is handed it. */
    fun encode(bytes: ByteArray): String = encode(bytes, URL, false)

    /** Standard base64 with padding: what the key's record keeps. */
    fun encodeStandard(bytes: ByteArray): String = encode(bytes, STANDARD, true)

    /** Standard base64 with padding, read back; null when it is not exactly that. */
    fun decodeStandard(s: String): ByteArray? {
        if (s.length % 4 != 0) return null
        val out = java.io.ByteArrayOutputStream(s.length / 4 * 3)
        var i = 0
        while (i < s.length) {
            val c = IntArray(4)
            var pads = 0
            for (j in 0 until 4) {
                val ch = s[i + j]
                if (ch == '=') {
                    // Padding only at the end, at most two.
                    if (i + 4 != s.length || j < 2) return null
                    pads++
                    c[j] = 0
                } else {
                    if (pads > 0) return null
                    val v = STANDARD.indexOf(ch)
                    if (v < 0) return null
                    c[j] = v
                }
            }
            val n = (c[0] shl 18) or (c[1] shl 12) or (c[2] shl 6) or c[3]
            out.write((n ushr 16) and 0xff)
            if (pads < 2) out.write((n ushr 8) and 0xff)
            if (pads < 1) out.write(n and 0xff)
            i += 4
        }
        return out.toByteArray()
    }
}

/** A time as the bridge reads one: ISO 8601 with milliseconds, in UTC. */
object IsoTime {
    private fun format(pattern: String): SimpleDateFormat =
        SimpleDateFormat(pattern, Locale.ROOT).apply {
            timeZone = TimeZone.getTimeZone("UTC")
            isLenient = false
        }

    fun format(millis: Long): String = format("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'").format(Date(millis))

    /** A time the page sends, with or without milliseconds, with `Z` or an offset; null when it is not one. */
    fun parse(s: String): Long? {
        for (pattern in listOf("yyyy-MM-dd'T'HH:mm:ss.SSSXXX", "yyyy-MM-dd'T'HH:mm:ssXXX")) {
            val at = ParsePosition(0)
            val d = format(pattern).parse(s, at) ?: continue
            if (at.index == s.length) return d.time
        }
        return null
    }
}
