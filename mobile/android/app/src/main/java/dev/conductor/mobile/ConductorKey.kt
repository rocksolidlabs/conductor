package dev.conductor.mobile

import org.json.JSONObject

// The phone shell's key on Android (docs/plans/phone-shell.md, OSS-3, "ConductorKey.kt"), with the iPhone's states, calls, policy rule
// and presence check: one Ed25519 key, kept by the platform's keystore in its secure hardware (from Android 13 the key itself; before
// it, or where the keystore has no Ed25519, a key sealed by a keystore AES-GCM key), usable only behind the person's unlock, never
// leaving the keystore, never logged, never in a backup. It knows no service, no contract and no route: it signs only what a native
// signing policy builds (a class a packaging adds under the fixed name `dev.conductor.mobile.packaging.ConductorKeySigningPolicy`; the
// open-source build has none, so it signs nothing), and when a plan asks for the person's presence it asks the person itself, for
// that one call, before it signs. A key that cannot be made in the secure hardware and behind the person's unlock is not made: the
// call is refused with words, never answered with a weaker key. Where the key is kept, how the person is asked and what time it is
// are seams (`KeyDevice`, `Presence`, `KeyRecordFile`, the clock), so its rules are tested with fakes; the phone's own are in
// KeyDevice.kt.

/** The key's state: none; made, not yet bound to an id (`unbound`); bound to an id; kept only to end what it was bound to. */
enum class KeyState(val word: String) {
    NONE("none"), UNBOUND("unbound"), BOUND("bound"), ENDING("ending");

    companion object {
        fun of(word: String): KeyState? = values().firstOrNull { it.word == word }
    }
}

/** A refusal of the key's: a code and its words, passed to the page as they are. */
class KeyRefusal(code: String, words: String) : Refusal(code, words)

/** A policy's refusal of a request: its code and words, passed back as they are. */
class KeyPolicyRefusal(val code: String, val words: String)

/** What a policy builds for a request: the bytes to sign, whether the person confirms first, whether an ending key may sign it, and
 * what the signature (64 bytes) becomes as the string answered. */
class SigningPlan(val message: ByteArray, val presence: Boolean, val whileEnding: Boolean, val finish: (ByteArray) -> String)

/** A policy's answer: a plan, or a refusal. */
sealed class PlanResult {
    class Plan(val plan: SigningPlan) : PlanResult()
    class Refused(val refusal: KeyPolicyRefusal) : PlanResult()
}

/**
 * A packaging's signing policy: it builds, it never signs. Found once at start by its fixed class name, made with its constructor of
 * no argument. Each plan is asked with the request, the key's state, its id (none until bound) and its public key, exactly as
 * `publicKey()` answers it (the 32 raw bytes in base64url with no padding), read from the same record `sign` signs with: never from
 * the page.
 */
interface KeySigningPolicy {
    fun plan(request: JSONObject, state: String, id: String?, publicKey: String): PlanResult
}

/** The key as the keystore holds it, and what is kept beside it: its form, its public key, and for a sealed key the sealed seed. */
class DeviceKey(val form: String, val publicKey: ByteArray, val seed: ByteArray?) {
    fun same(other: DeviceKey): Boolean =
        form == other.form && publicKey.contentEquals(other.publicKey) &&
            ((seed == null && other.seed == null) || (seed != null && other.seed != null && seed.contentEquals(other.seed)))

    companion object {
        /** An Ed25519 key kept by the keystore itself (Android 13 and later, where its secure hardware has Ed25519). */
        const val KEYSTORE = "keystore"
        /** An Ed25519 seed sealed by a keystore AES-GCM key: the keystore's key opens it for one signature, behind the unlock. */
        const val SEALED = "sealed"
    }
}

/** A signature's outcome from the keystore: made; the person's unlock needed first; the key unusable for good. */
sealed class Signed {
    class Signature(val bytes: ByteArray) : Signed()
    object NeedsUnlock : Signed()
    object Lost : Signed()
}

/** Thrown by `KeyDevice.make` when making the key needs the person's unlock first (a sealed key's seal is a use of the keystore's key). */
class KeyNeedsUnlock : Exception("the key needs the person's unlock")

/** The platform's keystore, as the key uses it. */
interface KeyDevice {
    /** Makes the key, in the secure hardware and behind the person's unlock, or refuses (`KeyRefusal`) with words. */
    fun make(): DeviceKey

    /** Whether the keystore still holds the key a record names. */
    fun holds(key: DeviceKey): Boolean

    /** Signs message with the key, inside the window the person's last unlock opened. */
    fun sign(key: DeviceKey, message: ByteArray): Signed

    /** Every key of the shell's the keystore holds, gone. */
    fun removeAll()
}

/** The person's presence, asked for one call: true when confirmed; false when cancelled, failed or not possible. Blocking. */
interface Presence {
    fun confirm(reason: String): Boolean
}

/** Where the key's record (its state, its id, an unbound key's time, its form) is kept: one file, read, written whole, or deleted. */
interface KeyRecordFile {
    fun read(): String?
    fun write(text: String)
    fun delete()
}

/** The words the person is asked to confirm an answer with. */
const val PRESENCE_REASON = "Confirm your answer in Conductor"

/** The words the person is asked with when keeping a new key needs their unlock. */
const val MAKE_REASON = "Confirm with your unlock to keep Conductor's key on this phone"

/** A request handed to `sign` is a JSON object of at most this many bytes. */
const val MAX_SIGN_REQUEST = 1024

/** A bound id is 1 to this many bytes of printable ASCII, no space. */
const val MAX_KEY_ID = 128

/** The bytes an X.509 SubjectPublicKeyInfo of an Ed25519 key puts before its 32 raw bytes (RFC 8410). */
val ED25519_SPKI = byteArrayOf(0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00)

/** An Ed25519 public key's 32 raw bytes, from its X.509 encoding; null when it is not one. */
fun rawEd25519(encoded: ByteArray?): ByteArray? {
    if (encoded == null || encoded.size != ED25519_SPKI.size + 32) return null
    if (!encoded.copyOfRange(0, ED25519_SPKI.size).contentEquals(ED25519_SPKI)) return null
    return encoded.copyOfRange(ED25519_SPKI.size, encoded.size)
}

/** Whether s is a bound id: 1 to 128 bytes of printable ASCII, no space. */
fun validKeyId(s: String): Boolean = s.isNotEmpty() && s.length <= MAX_KEY_ID && s.all { it.code in 0x21..0x7e }

/** The key's record: its key as the keystore holds it, its state, its id, an unbound key's time. */
class KeyRecord(val key: DeviceKey, val state: KeyState, val id: String?, val until: Long?) {
    fun text(): String {
        val o = JSONObject().put("v", 1).put("form", key.form).put("publicKey", Base64Url.encodeStandard(key.publicKey)).put("state", state.word)
        if (key.seed != null) o.put("sealed", Base64Url.encodeStandard(key.seed))
        if (id != null) o.put("id", id)
        if (until != null) o.put("until", until)
        return o.toString()
    }

    companion object {
        /** A record read back; null when the text is not one this build writes. */
        fun parse(text: String): KeyRecord? {
            val o = try {
                JSONObject(text)
            } catch (e: Exception) {
                return null
            }
            val keys = setOf("v", "form", "publicKey", "sealed", "state", "id", "until")
            for (k in o.keys()) if (k !in keys) return null
            val version = o.opt("v") as? Number
            val form = o.opt("form") as? String
            val pk = (o.opt("publicKey") as? String)?.let { Base64Url.decodeStandard(it) }
            val state = (o.opt("state") as? String)?.let { KeyState.of(it) }
            var seed: ByteArray? = null
            if (o.has("sealed")) seed = (o.opt("sealed") as? String)?.let { Base64Url.decodeStandard(it) } ?: return null
            var id: String? = null
            if (o.has("id")) id = o.opt("id") as? String ?: return null
            var until: Long? = null
            if (o.has("until")) until = (o.opt("until") as? Number)?.toLong() ?: return null
            if (version == null || version.toInt() != 1 || form == null || pk == null || pk.size != 32 || state == null) return null
            if (!((form == DeviceKey.KEYSTORE && seed == null) || (form == DeviceKey.SEALED && seed != null))) return null
            if (state == KeyState.NONE || (state == KeyState.UNBOUND && until == null) || (state != KeyState.UNBOUND && id == null)) return null
            if (id != null && !validKeyId(id)) return null
            return KeyRecord(DeviceKey(form, pk, seed), state, id, until)
        }
    }
}

/**
 * The key. Its calls run one at a time (the plugin's runner, and the lock here), and a sign that waits for the person's presence reads
 * the key again after the wait, so a key removed, ended or replaced meanwhile signs nothing.
 */
class ConductorKey(
    private val device: KeyDevice,
    private val presence: Presence,
    private val records: KeyRecordFile,
    private val policy: KeySigningPolicy?,
    private val now: () -> Long = { System.currentTimeMillis() },
) {
    private fun refuse(code: String, words: String) = KeyRefusal(code, words)

    private fun removeAll() {
        device.removeAll()
        records.delete()
    }

    /**
     * The record kept, or null. One that cannot be read as a key (a form this build does not know), or whose key the keystore no longer
     * holds, is removed with the keystore's keys, as if absent: no form of the record can leave the phone with a key it can neither use
     * nor clear.
     */
    private fun stored(): KeyRecord? {
        val text = records.read() ?: return null
        val r = KeyRecord.parse(text)
        if (r == null || !device.holds(r.key)) {
            removeAll()
            return null
        }
        return r
    }

    private fun expired(r: KeyRecord): Boolean = r.state == KeyState.UNBOUND && (r.until ?: 0) <= now()

    /** The record, unbound past its time removed as if absent. */
    private fun current(): KeyRecord? {
        val r = stored() ?: return null
        if (expired(r)) {
            removeAll()
            return null
        }
        return r
    }

    /**
     * Run before the first call: a keystore key with no record is an earlier one's and is removed; so is an unbound key past its time.
     * (The keystore's keys and the no-backup folder both go with the app, and neither is in a backup, so no earlier install's key can
     * come back with a new one.)
     */
    @Synchronized
    fun start() {
        if (records.read() == null) {
            device.removeAll()
            return
        }
        current()
    }

    /**
     * Makes the key, unbound until `until`, and answers its public key (32 bytes, base64url). Only from none: a key unbound or bound is
     * refused `key_exists`, one ending `key_ending`, so a new sign-in never replaces a key whose end is not confirmed. A key the keystore
     * cannot keep in its secure hardware behind the person's unlock is not made (the device's refusal, with its words).
     */
    @Synchronized
    fun create(until: Long): String {
        current()?.let { r ->
            if (r.state == KeyState.ENDING) throw refuse("key_ending", "This phone's key is still ending what it was bound to.")
            throw refuse("key_exists", "This phone has a key already.")
        }
        val key = try {
            device.make()
        } catch (e: KeyNeedsUnlock) {
            if (!presence.confirm(MAKE_REASON)) {
                device.removeAll()
                throw refuse("presence_refused", "Not confirmed: no key was made.")
            }
            try {
                device.make()
            } catch (again: KeyNeedsUnlock) {
                device.removeAll()
                throw refuse("presence_refused", "Not confirmed with the phone's unlock: no key was made.")
            }
        }
        try {
            records.write(KeyRecord(key, KeyState.UNBOUND, null, until).text())
        } catch (e: Exception) {
            removeAll()
            throw e
        }
        return Base64Url.encode(key.publicKey)
    }

    /** The public key (base64url), or null with none. */
    @Synchronized
    fun publicKey(): String? = current()?.let { Base64Url.encode(it.key.publicKey) }

    /** The state, the id once bound, and an unbound key's time. */
    @Synchronized
    fun state(): Triple<KeyState, String?, Long?> {
        val r = current() ?: return Triple(KeyState.NONE, null, null)
        return Triple(r.state, r.id, r.until)
    }

    /** Binds an unbound key to id (1 to 128 bytes of printable ASCII). */
    @Synchronized
    fun bind(id: String) {
        if (!validKeyId(id)) throw refuse("invalid_id", "An id is 1 to $MAX_KEY_ID printable characters.")
        val r = current() ?: throw refuse("no_key", "This phone has no key.")
        if (r.state != KeyState.UNBOUND) throw refuse("not_unbound", "This phone's key is bound already.")
        records.write(KeyRecord(r.key, KeyState.BOUND, id, null).text())
    }

    /** A bound key, kept only to end what it was bound to. */
    @Synchronized
    fun ending() {
        val r = current() ?: throw refuse("no_key", "This phone has no key.")
        if (r.state != KeyState.BOUND) throw refuse("not_bound", "This phone's key is not bound.")
        records.write(KeyRecord(r.key, KeyState.ENDING, r.id, null).text())
    }

    /** The key and its id gone: state none. */
    @Synchronized
    fun remove() = removeAll()

    /**
     * Signs what the policy builds for request (a JSON object of at most 1 KiB) and answers what the plan makes of the signature. Nothing
     * without a policy (`no_policy`), from none (`no_key`), past an unbound key's time (`key_expired`), with an ending key a plan not
     * marked for ending (`key_ending`); a policy's refusal is passed back as it is; when the plan asks for presence and the person does
     * not confirm, `presence_refused` and nothing signed. A plan that does not ask for presence still signs only inside the window the
     * person's last unlock opened: past it, the person is asked as for a plan that does. Never bytes handed in: only the plan's.
     */
    @Synchronized
    fun sign(request: JSONObject): String {
        if (utf8Size(request.toString()) > MAX_SIGN_REQUEST) throw refuse("too_large", "A request is at most $MAX_SIGN_REQUEST bytes.")
        val found = stored() ?: throw refuse("no_key", "This phone has no key.")
        if (expired(found)) {
            removeAll()
            throw refuse("key_expired", "This phone's key was not bound in time.")
        }
        val p = policy ?: throw refuse("no_policy", "This build signs nothing.")
        val plan = when (val r = p.plan(request, found.state.word, found.id, Base64Url.encode(found.key.publicKey))) {
            is PlanResult.Refused -> throw refuse(r.refusal.code, r.refusal.words)
            is PlanResult.Plan -> r.plan
        }
        if (found.state == KeyState.ENDING && !plan.whileEnding) throw refuse("key_ending", "This phone's key only ends what it was bound to.")
        var asked = false
        if (plan.presence) {
            if (!presence.confirm(PRESENCE_REASON)) throw refuse("presence_refused", "Not confirmed: nothing was signed.")
            asked = true
        }
        while (true) {
            // A wait for the person let the clock move: the key signs only if it is still the one the plan was built for, in the same
            // state, and an unbound one only before its time.
            val again = stored()
            if (again == null || !again.key.same(found.key) || again.state != found.state || again.id != found.id) {
                throw refuse("key_changed", "This phone's key changed while it was confirmed: nothing was signed.")
            }
            if (expired(again)) {
                removeAll()
                throw refuse("key_expired", "This phone's key was not bound in time.")
            }
            when (val s = device.sign(again.key, plan.message)) {
                is Signed.Signature -> return plan.finish(s.bytes)
                is Signed.Lost -> {
                    removeAll()
                    throw refuse("no_key", "This phone's key can no longer be used (its screen lock changed): nothing was signed.")
                }
                is Signed.NeedsUnlock -> {
                    if (asked || !presence.confirm(PRESENCE_REASON)) throw refuse("presence_refused", "Not confirmed: nothing was signed.")
                    asked = true
                }
            }
        }
    }

    companion object {
        /** The fixed name a packaging's signing policy is found by. */
        const val POLICY_CLASS = "dev.conductor.mobile.packaging.ConductorKeySigningPolicy"

        /** The packaging's policy, found by its fixed name with the loader given; none in the open-source build. */
        fun packagedPolicy(loader: ClassLoader? = ConductorKey::class.java.classLoader): KeySigningPolicy? = try {
            Class.forName(POLICY_CLASS, true, loader).getDeclaredConstructor().newInstance() as? KeySigningPolicy
        } catch (e: ClassNotFoundException) {
            null
        }
    }
}
