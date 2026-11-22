package dev.conductor.mobile

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.net.CookieHandler
import java.net.CookieManager
import java.net.HttpURLConnection
import java.net.URL

// The native HTTP call's allowlist on Android (docs/plans/phone-shell.md, OSS-3; the iPhone's NativeHTTPCalls): only https on an origin
// the packaging names, the address rebuilt from that origin alone, the page's headers held to what does not move or frame the request,
// and a connection that follows no redirect, keeps no cookie and bounds the reply.

class RecordingTransport : HttpTransport {
    val sent = mutableListOf<HttpRequest>()
    var reply = HttpReply(200, mapOf("Content-Type" to "text/plain"), "", "ok".toByteArray())
    override fun send(request: HttpRequest): HttpReply {
        sent.add(request)
        return HttpReply(reply.status, reply.headers, request.url, reply.body)
    }
}

class NativeHttpTest {
    private val named = NativeHttpOrigins.parse("""{"origins":["https://api.provider.example","https://relay.provider.example:8443"]}""")

    private fun admitted(s: String): String? = try {
        named.admit(s)
    } catch (r: Refusal) {
        assertEquals("origin_refused", r.code)
        null
    }

    @Test
    fun readsOnlyExactlyTheOriginsTheSyncWrites() {
        assertTrue(!named.isEmpty)
        for (bad in listOf(
            null, "", "[]", "{}", """{"origins":"https://a.example"}""", """{"origins":["http://a.example"]}""",
            """{"origins":["https://A.example"]}""", """{"origins":["https://a.example/"]}""", """{"origins":["https://a.example:443"]}""",
            """{"origins":["https://a.example."]}""", """{"origins":["https://127.0.0.1"]}""", """{"origins":["https://a.example:0"]}""",
            """{"origins":["https://a.example"],"more":1}""", """{"origins":["https://a.example", 3]}""",
        )) {
            assertTrue("$bad is none", NativeHttpOrigins.parse(bad).isEmpty)
        }
    }

    @Test
    fun admitsOnlyHttpsOnANamedOrigin() {
        assertEquals("https://api.provider.example/v1/x?a=1", admitted("https://api.provider.example/v1/x?a=1#frag"))
        assertEquals("https://api.provider.example/", admitted("https://api.provider.example"))
        assertEquals("https://api.provider.example/x", admitted("HTTPS://API.Provider.Example/x"))
        assertEquals("https://api.provider.example/x", admitted("https://api.provider.example:443/x"))
        assertEquals("https://relay.provider.example:8443/r", admitted("https://relay.provider.example:8443/r"))
        for (bad in listOf(
            "https://other.example/x", "http://api.provider.example/x", "file:///data/data/x", "https://user@api.provider.example/x",
            "https://user:pw@api.provider.example/x", "https://api.provider.example:444/x", "https://relay.provider.example/r",
            "https://api.provider.example./x", "https://api.provider.example.evil.example/x", "https://127.0.0.1/x", "https://[::1]/x",
            "https://%61pi.provider.example/x", "https://api.provider.example:/x", "https:api.provider.example", "javascript:alert(1)",
            "https://api.provider.example\\@other.example/", "https://api.provider.example/a b", "content://x/y", "",
        )) {
            assertNull("$bad is refused", admitted(bad))
        }
        try {
            NativeHttpOrigins.NONE.admit("https://api.provider.example/")
            fail()
        } catch (r: Refusal) {
            assertEquals("origin_refused", r.code)
        }
    }

    @Test
    fun aCallIsSentOnlyWhenAdmittedAndAnsweredInTheBridgesShape() {
        val t = RecordingTransport()
        val calls = NativeHttpCalls(named, t)
        val a = calls.call("request", JSONObject().put("url", "https://api.provider.example/x").put("method", "post")
            .put("headers", JSONObject().put("Authorization", "Bearer t").put("Content-Type", "application/json")).put("data", "{\"a\":1}")
            .put("responseType", "text")) as CallAnswer.Resolve
        assertEquals(200, a.value.getInt("status"))
        assertEquals("ok", a.value.getString("data"))
        assertEquals("text/plain", a.value.getJSONObject("headers").getString("content-type"))
        assertEquals("https://api.provider.example/x", a.value.getString("url"))
        val sent = t.sent.single()
        assertEquals("POST", sent.method)
        assertEquals("Bearer t", sent.headers["Authorization"])
        assertEquals("{\"a\":1}", String(sent.body!!))
        val refused = calls.call("get", JSONObject().put("url", "https://other.example/")) as CallAnswer.Reject
        assertEquals("origin_refused", refused.code)
        assertEquals(1, t.sent.size)
        assertEquals("invalid_request", (calls.call("request", JSONObject().put("url", "https://api.provider.example/").put("params", JSONObject().put("a", "1"))) as CallAnswer.Reject).code)
        assertEquals("invalid_request", (calls.call("request", JSONObject().put("url", "https://api.provider.example/").put("method", "TRACE")) as CallAnswer.Reject).code)
        assertEquals("invalid_request", (calls.call("request", JSONObject().put("url", "https://api.provider.example/").put("data", 3)) as CallAnswer.Reject).code)
        assertEquals("unknown_call", (calls.call("options", JSONObject()) as CallAnswer.Reject).code)
        assertEquals(1, t.sent.size)
        t.reply = HttpReply(200, emptyMap(), "", byteArrayOf(0xff.toByte(), 0xfe.toByte()))
        assertEquals(JSONObject.NULL, (calls.call("get", JSONObject().put("url", "https://api.provider.example/")) as CallAnswer.Resolve).value.get("data"))
    }

    @Test
    fun aHeaderThatNamesWhereARequestGoesOrHowItIsFramedIsRefusedAndNothingSent() {
        val t = RecordingTransport()
        val calls = NativeHttpCalls(named, t)
        for ((name, value) in listOf(
            "Host" to "other.example", "host" to "x", "Connection" to "close", "Keep-Alive" to "1", "Proxy-Authorization" to "x",
            "proxy-connection" to "x", "Content-Length" to "1", "Transfer-Encoding" to "chunked", "TE" to "trailers", "Upgrade" to "h2c",
            "Trailer" to "x", "Bad Name" to "x", "X-Ok" to "line\r\nInjected: 1", "X-Ok" to "nul\u0000",
        )) {
            val a = calls.call("request", JSONObject().put("url", "https://api.provider.example/").put("headers", JSONObject().put(name, value)))
            assertEquals("$name refused", "invalid_request", (a as CallAnswer.Reject).code)
        }
        assertTrue(t.sent.isEmpty())
        assertTrue(NativeHttpCalls.headerAllowed("Authorization", "Bearer x"))
        assertTrue(NativeHttpCalls.headerAllowed("X-Conductor-Sig", "a.b-c_d~"))
    }

    private fun transport(maxBody: Int = NativeHttpCalls.MAX_BODY) =
        UrlConnectionTransport(maxBody) { url -> URL(url).openConnection() as HttpURLConnection }

    @Test(timeout = 60_000)
    fun theConnectionFollowsNoRedirectKeepsNoCookieAndBoundsTheBody() {
        LoopbackServer { path ->
            when (path) {
                "/away" -> LoopbackServer.reply(302, "", "Location: http://127.0.0.1:1/elsewhere", "Set-Cookie: sid=1; Path=/")
                "/big" -> LoopbackServer.reply(200, "x".repeat(2048))
                else -> LoopbackServer.reply(200, "fine")
            }
        }.use { server ->
            val t = transport(maxBody = 1024)
            val away = t.send(HttpRequest("${server.base}/away", "GET", emptyMap(), null))
            assertEquals(302, away.status)
            assertEquals("http://127.0.0.1:1/elsewhere", away.headers["Location"])
            assertEquals("fine", String(t.send(HttpRequest("${server.base}/next", "GET", emptyMap(), null)).body))
            // The redirect was not followed (its address has nobody), and the cookie it set was not sent back.
            assertEquals(listOf("/away", "/next"), server.paths())
            assertEquals(listOf<String?>(null, null), server.requests.map { it["cookie"] })
            try {
                t.send(HttpRequest("${server.base}/big", "GET", emptyMap(), null))
                fail()
            } catch (r: Refusal) {
                assertEquals("too_large", r.code)
            }
            // A process with a cookie handler (the native runtime's cookies plugin sets one) sends nothing at all.
            CookieHandler.setDefault(CookieManager())
            try {
                t.send(HttpRequest("${server.base}/next", "GET", emptyMap(), null))
                fail()
            } catch (r: Refusal) {
                assertEquals("native", r.code)
            } finally {
                CookieHandler.setDefault(null)
            }
            assertEquals(listOf("/away", "/next", "/big"), server.paths())
        }
    }

    @Test(timeout = 60_000)
    fun anAdmittedCallArrivesAsAdmittedAndARefusedOneNeverLeaves() {
        LoopbackServer { _ -> LoopbackServer.reply(200, "ok", "X-Answer: 1") }.use { server ->
            val port = server.port
            val origins = NativeHttpOrigins.parse("""{"origins":["https://api.provider.example:$port"]}""")
            // The phone's connection is https; the test's opener sends the admitted address to its own server instead, unchanged otherwise.
            val calls = NativeHttpCalls(origins, UrlConnectionTransport { url ->
                URL(url.replace("https://api.provider.example:$port", server.base)).openConnection() as HttpURLConnection
            })
            val a = calls.call("request", JSONObject().put("url", "https://API.provider.example:$port/v1/inbox?after=7#x")
                .put("headers", JSONObject().put("Authorization", "Bearer t"))) as CallAnswer.Resolve
            assertEquals(200, a.value.getInt("status"))
            assertEquals("ok", a.value.getString("data"))
            assertEquals("1", a.value.getJSONObject("headers").getString("x-answer"))
            assertEquals(listOf("/v1/inbox?after=7"), server.paths())
            assertEquals("Bearer t", server.requests.single()["authorization"])
            for (u in listOf("https://other.example:$port/v1/inbox", "http://api.provider.example:$port/", "https://api.provider.example/",
                "https://u@api.provider.example:$port/")) {
                assertEquals(u, "origin_refused", (calls.call("get", JSONObject().put("url", u)) as CallAnswer.Reject).code)
            }
            assertEquals(1, server.requests.size)
        }
    }
}
