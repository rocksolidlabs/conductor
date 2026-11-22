package dev.conductor.mobile

import org.bouncycastle.crypto.params.Ed25519PrivateKeyParameters
import org.bouncycastle.crypto.signers.Ed25519Signer
import org.json.JSONObject
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.security.KeyFactory
import java.security.SecureRandom
import java.security.Signature
import java.security.spec.X509EncodedKeySpec

// The Android key's rules over fakes of the keystore, the person and the record (docs/plans/phone-shell.md, OSS-3, "ConductorKey.kt"):
// the iPhone key's states, calls, policy rule and presence check, the key made only in the secure hardware behind the unlock or not at
// all, and every signature checked by the JDK's own Ed25519.

/** A keystore in memory: real Ed25519 keys, and switches for what a phone's keystore can answer. */
class FakeKeyDevice : KeyDevice {
    var seed: ByteArray? = null
    var made = 0
    var removed = 0
    var refuseMake: KeyRefusal? = null
    var needsUnlockAtMake = 0
    var outcomes = ArrayDeque<Signed>()
    val signedMessages = mutableListOf<ByteArray>()

    override fun make(): DeviceKey {
        refuseMake?.let { throw it }
        if (needsUnlockAtMake > 0) {
            needsUnlockAtMake--
            throw KeyNeedsUnlock()
        }
        val s = ByteArray(32).also { SecureRandom().nextBytes(it) }
        seed = s
        made++
        return DeviceKey(DeviceKey.KEYSTORE, Ed25519PrivateKeyParameters(s, 0).generatePublicKey().encoded, null)
    }

    override fun holds(key: DeviceKey): Boolean {
        val s = seed ?: return false
        return Ed25519PrivateKeyParameters(s, 0).generatePublicKey().encoded.contentEquals(key.publicKey)
    }

    override fun sign(key: DeviceKey, message: ByteArray): Signed {
        outcomes.removeFirstOrNull()?.let { return it }
        val s = seed ?: return Signed.Lost
        signedMessages.add(message)
        val signer = Ed25519Signer()
        signer.init(true, Ed25519PrivateKeyParameters(s, 0))
        signer.update(message, 0, message.size)
        return Signed.Signature(signer.generateSignature())
    }

    override fun removeAll() {
        seed = null
        removed++
    }
}

class FakePresence : Presence {
    var answers = ArrayDeque<Boolean>()
    val asked = mutableListOf<String>()
    var during: () -> Unit = {}
    override fun confirm(reason: String): Boolean {
        asked.add(reason)
        during()
        return answers.removeFirstOrNull() ?: true
    }
}

class MemoryRecord : KeyRecordFile {
    var text: String? = null
    override fun read(): String? = text
    override fun write(text: String) {
        this.text = text
    }

    override fun delete() {
        text = null
    }
}

/** A policy that builds from the request: its "m" as the message, "presence" and "ending" as the plan's, "refuse" as a refusal. */
class FakePolicy : KeySigningPolicy {
    val asked = mutableListOf<Triple<String, String?, String>>()
    val finished = mutableListOf<ByteArray>()
    override fun plan(request: JSONObject, state: String, id: String?, publicKey: String): PlanResult {
        asked.add(Triple(state, id, publicKey))
        if (request.has("refuse")) return PlanResult.Refused(KeyPolicyRefusal("not_mine", "This policy does not sign that."))
        return PlanResult.Plan(SigningPlan(
            message = ("plan:" + request.getString("m")).toByteArray(),
            presence = request.optBoolean("presence"),
            whileEnding = request.optBoolean("ending"),
            finish = { sig -> finished.add(sig); Base64Url.encode(sig) },
        ))
    }
}

class ConductorKeyTest {
    private var clock = 1_000_000L
    private val device = FakeKeyDevice()
    private val presence = FakePresence()
    private val record = MemoryRecord()
    private val policy = FakePolicy()
    private fun key(p: KeySigningPolicy? = policy) = ConductorKey(device, presence, record, p) { clock }

    private fun refusal(code: String, f: () -> Unit) {
        try {
            f()
            fail("expected $code")
        } catch (e: KeyRefusal) {
            assertEquals(code, e.code)
        }
    }

    private fun verifies(publicKey: String, message: ByteArray, signature: String): Boolean {
        val raw = java.util.Base64.getUrlDecoder().decode(publicKey)
        val pk = KeyFactory.getInstance("Ed25519").generatePublic(X509EncodedKeySpec(ED25519_SPKI + raw))
        return Signature.getInstance("Ed25519").run {
            initVerify(pk)
            update(message)
            verify(java.util.Base64.getUrlDecoder().decode(signature))
        }
    }

    private fun req(m: String, presence: Boolean = false, ending: Boolean = false) =
        JSONObject().put("m", m).put("presence", presence).put("ending", ending)

    @Test
    fun signsNothingWithoutAPolicy() {
        val k = key(null)
        k.create(clock + 60_000)
        refusal("no_policy") { k.sign(req("x", presence = true)) }
        assertTrue("no sheet before a refusal", presence.asked.isEmpty())
    }

    @Test
    fun signsOnlyThePlansBytes() {
        val k = key()
        val pk = k.create(clock + 60_000)
        val sig = k.sign(req("hello"))
        assertTrue(verifies(pk, "plan:hello".toByteArray(), sig))
        assertFalse(verifies(pk, req("hello").toString().toByteArray(), sig))
        assertArrayEquals("plan:hello".toByteArray(), device.signedMessages.single())
    }

    @Test
    fun asksPresenceWheneverThePlanDoesAndSignsNothingWhenRefused() {
        val k = key()
        k.create(clock + 60_000)
        k.sign(req("a"))
        assertTrue(presence.asked.isEmpty())
        k.sign(req("b", presence = true))
        assertEquals(listOf(PRESENCE_REASON), presence.asked)
        presence.answers.add(false)
        refusal("presence_refused") { k.sign(req("c", presence = true)) }
        assertEquals(2, policy.finished.size)
    }

    @Test
    fun passesAPolicysRefusalAsItIs() {
        val k = key()
        k.create(clock + 60_000)
        try {
            k.sign(JSONObject().put("refuse", true))
            fail()
        } catch (e: KeyRefusal) {
            assertEquals("not_mine", e.code)
            assertEquals("This policy does not sign that.", e.words)
        }
    }

    @Test
    fun anEndingKeySignsOnlyWhatThePlanMarksForEnding() {
        val k = key()
        k.create(clock + 60_000)
        k.bind("id-1")
        k.ending()
        refusal("key_ending") { k.sign(req("x")) }
        k.sign(req("end", ending = true))
        assertEquals(1, policy.finished.size)
    }

    @Test
    fun refusesANewKeyWhileOneIsUnboundBoundOrEnding() {
        val k = key()
        k.create(clock + 60_000)
        refusal("key_exists") { k.create(clock + 60_000) }
        k.bind("id-1")
        refusal("key_exists") { k.create(clock + 60_000) }
        k.ending()
        refusal("key_ending") { k.create(clock + 60_000) }
        assertEquals(1, device.made)
        k.remove()
        assertEquals(KeyState.NONE, k.state().first)
        k.create(clock + 60_000)
        assertEquals(2, device.made)
    }

    @Test
    fun bindsAnUnboundKeyToAnIdOfItsBounds() {
        val k = key()
        refusal("no_key") { k.bind("id") }
        k.create(clock + 60_000)
        for (bad in listOf("", "a b", "x".repeat(129), "é")) refusal("invalid_id") { k.bind(bad) }
        k.bind("x".repeat(128))
        assertEquals(Triple(KeyState.BOUND, "x".repeat(128), null), k.state())
        refusal("not_unbound") { k.bind("again") }
        refusal("not_bound") { key().also { it.ending() }.ending() }
    }

    @Test
    fun anUnboundKeyPastItsTimeIsGoneAndSignsNothing() {
        val k = key()
        k.create(clock + 60_000)
        clock += 60_000
        refusal("key_expired") { k.sign(req("x")) }
        assertNull(record.text)
        assertEquals(KeyState.NONE, k.state().first)
    }

    @Test
    fun atStartRemovesAKeystoreKeyWithNoRecordAndAnUnboundOnePastItsTime() {
        key().create(clock + 60_000)
        record.text = null
        key().start()
        assertNull(device.seed)
        key().create(clock + 60_000)
        clock += 60_001
        key().start()
        assertNull(record.text)
        assertNull(device.seed)
    }

    @Test
    fun signsNothingWithAKeyChangedWhileThePersonIsAsked() {
        val k = key()
        k.create(clock + 60_000)
        presence.during = {
            device.removeAll()
            record.text = null
            key().create(clock + 60_000)
        }
        refusal("key_changed") { k.sign(req("x", presence = true)) }
        assertTrue(policy.finished.isEmpty())
    }

    @Test
    fun anUnboundKeyWhoseTimeRunsOutWhileThePersonIsAskedSignsNothing() {
        val k = key()
        k.create(clock + 60_000)
        presence.during = { clock += 61_000 }
        refusal("key_expired") { k.sign(req("x", presence = true)) }
        assertTrue(policy.finished.isEmpty())
        assertNull(record.text)
    }

    @Test
    fun aRecordThatIsNotAKeyIsNoKeyAndIsRemoved() {
        val k = key()
        k.create(clock + 60_000)
        for (bad in listOf("not json", "{}", record.text!!.replace("\"v\":1", "\"v\":2"), record.text!!.replace("unbound", "lost"),
            JSONObject(record.text!!).put("extra", 1).toString())) {
            device.make()
            record.text = bad
            assertEquals(KeyState.NONE, key().state().first)
            assertNull(record.text)
            assertNull(device.seed)
        }
        // A record whose key the keystore no longer holds (its lock removed, its data cleared): no key, and the record goes.
        key().create(clock + 60_000)
        device.seed = null
        assertEquals(KeyState.NONE, key().state().first)
        assertNull(record.text)
    }

    @Test
    fun thePolicyIsHandedTheKeysOwnPublicKey() {
        val k = key()
        val pk = k.create(clock + 60_000)
        assertEquals(43, pk.length)
        k.sign(req("x"))
        assertEquals(Triple("unbound", null, pk), policy.asked.single())
        assertEquals(pk, k.publicKey())
    }

    @Test
    fun refusesARequestPastItsBound() {
        val k = key()
        k.create(clock + 60_000)
        refusal("too_large") { k.sign(JSONObject().put("m", "x".repeat(MAX_SIGN_REQUEST))) }
        assertTrue(policy.asked.isEmpty())
    }

    @Test
    fun aKeyTheKeystoreCannotKeepBehindTheUnlockIsNeverMade() {
        device.refuseMake = KeyRefusal("key_unprotected", "This phone cannot keep a key in its secure hardware behind its unlock: no key was made.")
        val k = key()
        refusal("key_unprotected") { k.create(clock + 60_000) }
        assertNull(record.text)
        assertEquals(KeyState.NONE, k.state().first)
        assertEquals("key_unprotected", (KeyCalls(key()).call("create", JSONObject().put("until", "2030-01-01T00:00:00Z")) as CallAnswer.Reject).code)
    }

    @Test
    fun makingAKeyThatNeedsTheUnlockAsksThePerson() {
        device.needsUnlockAtMake = 1
        val k = key()
        k.create(clock + 60_000)
        assertEquals(listOf(MAKE_REASON), presence.asked)
        assertEquals(KeyState.UNBOUND, k.state().first)
        k.remove()
        device.needsUnlockAtMake = 1
        presence.answers.add(false)
        refusal("presence_refused") { k.create(clock + 60_000) }
        assertNull(record.text)
    }

    @Test
    fun aPlanWithoutPresencePastTheUnlocksWindowAsksThePersonOnce() {
        val k = key()
        val pk = k.create(clock + 60_000)
        device.outcomes.add(Signed.NeedsUnlock)
        val sig = k.sign(req("x"))
        assertEquals(listOf(PRESENCE_REASON), presence.asked)
        assertTrue(verifies(pk, "plan:x".toByteArray(), sig))
        // Asked already, and the keystore still wants the unlock (a weak biometric): nothing signed.
        device.outcomes.add(Signed.NeedsUnlock)
        refusal("presence_refused") { k.sign(req("y", presence = true)) }
        device.outcomes.add(Signed.NeedsUnlock)
        presence.answers.add(false)
        refusal("presence_refused") { k.sign(req("z")) }
    }

    @Test
    fun aKeyLostToAChangedLockIsRemovedAndSignsNothing() {
        val k = key()
        k.create(clock + 60_000)
        device.outcomes.add(Signed.Lost)
        refusal("no_key") { k.sign(req("x")) }
        assertNull(record.text)
        assertEquals(1, device.removed)
    }

    @Test
    fun theKeysCallsAnswerAsTheBridgeReadsThem() {
        val calls = KeyCalls(key())
        fun value(a: CallAnswer) = (a as CallAnswer.Resolve).value
        assertEquals("none", value(calls.call("state", JSONObject())).getString("state"))
        assertEquals(JSONObject.NULL, value(calls.call("publicKey", JSONObject())).get("publicKey"))
        val pk = value(calls.call("create", JSONObject().put("until", "2030-01-01T00:00:00.000Z"))).getString("publicKey")
        val state = value(calls.call("state", JSONObject()))
        assertEquals("unbound", state.getString("state"))
        assertEquals("2030-01-01T00:00:00.000Z", state.getString("until"))
        value(calls.call("bind", JSONObject().put("id", "acct-1")))
        assertEquals("acct-1", value(calls.call("state", JSONObject())).getString("id"))
        val sig = value(calls.call("sign", JSONObject().put("request", req("m")))).getString("signature")
        assertTrue(verifies(pk, "plan:m".toByteArray(), sig))
        assertEquals("invalid_request", (calls.call("create", JSONObject().put("until", "tomorrow")) as CallAnswer.Reject).code)
        assertEquals("invalid_request", (calls.call("sign", JSONObject().put("request", "bytes")) as CallAnswer.Reject).code)
        assertEquals("invalid_request", (calls.call("bind", JSONObject()) as CallAnswer.Reject).code)
        assertEquals("unknown_call", (calls.call("export", JSONObject()) as CallAnswer.Reject).code)
        value(calls.call("ending", JSONObject()))
        value(calls.call("remove", JSONObject()))
        assertEquals("none", value(calls.call("state", JSONObject())).getString("state"))
    }

    @Test
    fun anEd25519PublicKeyIsReadFromItsStandardEncoding() {
        val pair = java.security.KeyPairGenerator.getInstance("Ed25519").generateKeyPair()
        val raw = rawEd25519(pair.public.encoded)!!
        assertEquals(32, raw.size)
        assertArrayEquals(pair.public.encoded, ED25519_SPKI + raw)
        assertNull(rawEd25519(ByteArray(44)))
        assertNull(rawEd25519(pair.public.encoded.copyOf(43)))
    }

    @Test
    fun theOpenSourceBuildHasNoSigningPolicy() {
        assertNull(ConductorKey.packagedPolicy())
    }
}
