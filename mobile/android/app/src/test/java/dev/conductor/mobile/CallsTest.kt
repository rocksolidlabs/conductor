package dev.conductor.mobile

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import java.util.Collections
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

// The plugins' calls as the bridge makes them (mobile/src/bridge.ts `NativeStore`), and their order: one at a time, in the order they
// arrive, each answered before the next starts, waits held at a gate in the fake, never a sleep.

class CallsTest {
    private fun resolved(a: CallAnswer): JSONObject {
        assertTrue("expected a value, got ${(a as? CallAnswer.Reject)?.code}", a is CallAnswer.Resolve)
        return (a as CallAnswer.Resolve).value
    }

    private fun code(a: CallAnswer): String = (a as CallAnswer.Reject).code

    @Test
    fun theTestsParseWithARealJsonParserNotThePlatformsStub() {
        // The platform's org.json is a stub on a unit test's classpath; the real one (a test dependency) must come first, or every
        // test here fails at its first JSON call with "not mocked". Said once, plainly, here.
        val o = try {
            JSONObject("""{"a":1,"b":"x"}""")
        } catch (e: RuntimeException) {
            throw AssertionError("org.json is the platform's stub on this classpath: ${e.message}")
        }
        assertEquals(1, o.getInt("a"))
        assertEquals("x", o.getString("b"))
    }

    @Test
    fun theStoresCallsAnswerAsTheBridgeReadsThem() {
        val calls = StoreCalls(ConductorStore(File("/s"), MemoryFiles(), CountedCaches()) { 1_700_000_000_123L })
        resolved(calls.call("put", JSONObject().put("area", "drafts").put("key", "d1").put("data", "text")))
        val got = resolved(calls.call("get", JSONObject().put("area", "drafts").put("key", "d1")))
        assertEquals("text", got.getString("data"))
        assertEquals("2023-11-14T22:13:20.123Z", got.getString("savedAt"))
        assertEquals(JSONObject.NULL, resolved(calls.call("get", JSONObject().put("area", "drafts").put("key", "none"))).get("data"))
        val keys = resolved(calls.call("list", JSONObject().put("area", "drafts"))).getJSONArray("keys")
        assertEquals("d1", keys.getJSONObject(0).getString("key"))
        resolved(calls.call("remove", JSONObject().put("area", "drafts").put("key", "d1")))
        resolved(calls.call("clear", JSONObject()))
    }

    @Test
    fun theStoreRefusesArgumentsItDoesNotTakeWithItsOwnCodes() {
        val calls = StoreCalls(ConductorStore(File("/s"), MemoryFiles(), CountedCaches()))
        assertEquals("invalid_request", code(calls.call("put", JSONObject().put("area", "drafts").put("key", "d1"))))
        assertEquals("invalid_request", code(calls.call("put", JSONObject().put("area", "drafts").put("key", 3).put("data", "x"))))
        assertEquals("invalid_key", code(calls.call("put", JSONObject().put("area", "drafts").put("key", "../x").put("data", "x"))))
        assertEquals("invalid_area", code(calls.call("list", JSONObject().put("area", "keys"))))
        assertEquals("unknown_call", code(calls.call("format", JSONObject())))
    }

    @Test
    fun aRefusalKeepsItsCodeAndWords() {
        val a = refusalOf(StoreRefusal("full", "This phone keeps 32 drafts already.")) as CallAnswer.Reject
        assertEquals("full", a.code)
        assertEquals("This phone keeps 32 drafts already.", a.words)
        assertEquals("native", (refusalOf(IllegalStateException("x")) as CallAnswer.Reject).code)
    }

    @Test(timeout = 60_000)
    fun callsRunOneAtATimeInTheOrderTheyArrive() {
        val gate = CountDownLatch(1)
        val running = Collections.synchronizedList(mutableListOf<String>())
        val answered = Collections.synchronizedList(mutableListOf<String>())
        val done = CountDownLatch(3)
        val runner = CallRunner {
            object : ShellCalls {
                override fun call(method: String, options: JSONObject): CallAnswer {
                    running.add(method)
                    if (method == "first") gate.await()
                    return CallAnswer.Resolve(JSONObject().put("m", method))
                }
            }
        }
        for (m in listOf("first", "second", "third")) runner.submit(m, JSONObject()) { answered.add(m); done.countDown() }
        // The first holds its turn: the others wait behind it, and nothing has been answered.
        while (running.isEmpty()) Thread.yield()
        assertEquals(listOf("first"), running.toList())
        assertEquals(2, runner.queued)
        assertTrue(answered.isEmpty())
        gate.countDown()
        assertTrue(done.await(30, TimeUnit.SECONDS))
        assertEquals(listOf("first", "second", "third"), answered.toList())
        assertEquals(listOf("first", "second", "third"), running.toList())
    }

    @Test(timeout = 60_000)
    fun callsWhoseCallsCannotBeOpenedAreEachAnsweredNative() {
        val done = CountDownLatch(2)
        val codes = Collections.synchronizedList(mutableListOf<String>())
        val runner = CallRunner { throw IllegalStateException("no store here") }
        repeat(2) { runner.submit("get", JSONObject()) { a -> codes.add(code(a)); done.countDown() } }
        assertTrue(done.await(30, TimeUnit.SECONDS))
        assertEquals(listOf("native", "native"), codes.toList())
    }

    @Test(timeout = 60_000)
    fun aPutMadeBeforeAClearIsRemovedByItAndOneMadeAfterIsKept() {
        val files = MemoryFiles()
        val calls = StoreCalls(ConductorStore(File("/s"), files, CountedCaches()))
        val runner = CallRunner { calls }
        val done = CountDownLatch(3)
        runner.submit("put", JSONObject().put("area", "drafts").put("key", "before").put("data", "x")) { done.countDown() }
        runner.submit("clear", JSONObject()) { done.countDown() }
        runner.submit("put", JSONObject().put("area", "drafts").put("key", "after").put("data", "y")) { done.countDown() }
        assertTrue(done.await(30, TimeUnit.SECONDS))
        assertEquals(listOf("after"), files.names("drafts"))
    }

    @Test
    fun timesAreReadWithOrWithoutMillisecondsAndWrittenWithThem() {
        assertEquals(1_700_000_000_123L, IsoTime.parse("2023-11-14T22:13:20.123Z"))
        assertEquals(1_700_000_000_000L, IsoTime.parse("2023-11-14T22:13:20Z"))
        assertEquals(1_700_000_000_000L, IsoTime.parse("2023-11-14T23:13:20+01:00"))
        assertEquals(null, IsoTime.parse("2023-11-14 22:13:20"))
        assertEquals(null, IsoTime.parse("2023-11-14T22:13:20Zjunk"))
        assertEquals("2023-11-14T22:13:20.123Z", IsoTime.format(1_700_000_000_123L))
    }

    @Test
    fun base64IsTheStandardsAndBase64urlHasNoPadding() {
        val enc = java.util.Base64.getEncoder()
        for (n in 0..70) {
            val b = ByteArray(n) { (it * 37 + n).toByte() }
            assertEquals(enc.encodeToString(b), Base64Url.encodeStandard(b))
            assertEquals(java.util.Base64.getUrlEncoder().withoutPadding().encodeToString(b), Base64Url.encode(b))
            assertTrue(b.contentEquals(Base64Url.decodeStandard(enc.encodeToString(b))!!))
        }
        assertEquals(null, Base64Url.decodeStandard("abc"))
        assertEquals(null, Base64Url.decodeStandard("a=bc"))
        assertEquals(null, Base64Url.decodeStandard("ab-_"))
    }
}
