package dev.conductor.mobile

import android.app.KeyguardManager
import android.content.Context
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyInfo
import android.security.keystore.KeyPermanentlyInvalidatedException
import android.security.keystore.KeyProperties
import android.security.keystore.UserNotAuthenticatedException
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricPrompt
import androidx.fragment.app.FragmentActivity
import org.bouncycastle.crypto.params.Ed25519PrivateKeyParameters
import org.bouncycastle.crypto.signers.Ed25519Signer
import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.security.GeneralSecurityException
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.PrivateKey
import java.security.ProviderException
import java.security.SecureRandom
import java.security.Signature
import java.security.spec.ECGenParameterSpec
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executor
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.SecretKeyFactory
import javax.crypto.spec.GCMParameterSpec

// The key's seams as a phone gives them: the platform's keystore (its secure hardware, behind the person's unlock), the person's
// presence (BiometricPrompt, the device's PIN, pattern or password allowed), and the key's record in the no-backup folder.

/** Whether the keystore says a key is in its secure hardware and usable only after the person's authentication. */
fun protectedBy(info: KeyInfo): Boolean {
    val hardware = if (Build.VERSION.SDK_INT >= 31) {
        info.securityLevel == KeyProperties.SECURITY_LEVEL_TRUSTED_ENVIRONMENT ||
            info.securityLevel == KeyProperties.SECURITY_LEVEL_STRONGBOX ||
            info.securityLevel == KeyProperties.SECURITY_LEVEL_UNKNOWN_SECURE
    } else {
        @Suppress("DEPRECATION")
        info.isInsideSecureHardware
    }
    return hardware && info.isUserAuthenticationRequired
}

/**
 * The platform's keystore. From Android 13 the key is an Ed25519 key of the keystore's own; where the keystore cannot make one (before
 * Android 13, or a phone whose secure hardware has no Ed25519) it is an Ed25519 seed sealed by a keystore AES-256-GCM key, opened for
 * one signature and wiped. Either keystore key is made in the secure hardware, usable only while the phone is unlocked (from Android 9)
 * and only within [WINDOW] seconds of the person's unlock (the phone's own, or the shell's prompt); the keystore's own account of the
 * key is read back, and a key it does not keep in its secure hardware behind the unlock is removed and refused, never used.
 */
class AndroidKeyDevice(private val context: Context) : KeyDevice {
    companion object {
        const val STORE = "AndroidKeyStore"
        const val SIGNING = "conductor.shell.key"
        const val SEALING = "conductor.shell.seal"
        /** How long after the person's unlock the keystore lets the key be used, in seconds. */
        const val WINDOW = 300
    }

    private fun keyStore(): KeyStore = KeyStore.getInstance(STORE).apply { load(null) }

    private fun unprotected(): KeyRefusal =
        KeyRefusal("key_unprotected", "This phone cannot keep a key in its secure hardware behind its unlock: no key was made.")

    override fun make(): DeviceKey {
        val guard = context.getSystemService(Context.KEYGUARD_SERVICE) as? KeyguardManager
        if (guard == null || !guard.isDeviceSecure) {
            throw KeyRefusal("no_lock", "This phone has no screen lock: set one, then sign in again.")
        }
        removeAll()
        if (Build.VERSION.SDK_INT >= 33) {
            val made = try {
                makeKeystoreKey()
            } catch (e: KeyRefusal) {
                throw e
            } catch (e: GeneralSecurityException) {
                null
            } catch (e: ProviderException) {
                null
            }
            if (made != null) return made
            removeAll()
        }
        return makeSealedKey()
    }

    /** An Ed25519 key of the keystore's own (Android 13 and later); null when the keystore makes it but cannot say how it keeps it. */
    private fun makeKeystoreKey(): DeviceKey? {
        if (Build.VERSION.SDK_INT < 33) return null
        val spec = KeyGenParameterSpec.Builder(SIGNING, KeyProperties.PURPOSE_SIGN)
            .setAlgorithmParameterSpec(ECGenParameterSpec("ed25519"))
            .setDigests(KeyProperties.DIGEST_NONE)
            .setUserAuthenticationRequired(true)
            .setUserAuthenticationParameters(WINDOW, KeyProperties.AUTH_BIOMETRIC_STRONG or KeyProperties.AUTH_DEVICE_CREDENTIAL)
            .setUnlockedDeviceRequired(true)
            .build()
        val generator = KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, STORE)
        generator.initialize(spec)
        val pair = generator.generateKeyPair()
        val raw = rawEd25519(pair.public.encoded) ?: return null
        val info = keyInfo(pair.private) ?: return null
        if (!protectedBy(info)) {
            removeAll()
            throw unprotected()
        }
        return DeviceKey(DeviceKey.KEYSTORE, raw, null)
    }

    /** The keystore's account of a private key it holds, or null when it gives none for that kind of key. */
    private fun keyInfo(key: PrivateKey): KeyInfo? {
        for (algorithm in listOf(key.algorithm, KeyProperties.KEY_ALGORITHM_EC)) {
            try {
                return KeyFactory.getInstance(algorithm, STORE).getKeySpec(key, KeyInfo::class.java)
            } catch (e: GeneralSecurityException) {
                continue
            }
        }
        return null
    }

    private fun makeSealedKey(): DeviceKey {
        val builder = KeyGenParameterSpec.Builder(SEALING, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256)
            .setUserAuthenticationRequired(true)
        if (Build.VERSION.SDK_INT >= 30) {
            builder.setUserAuthenticationParameters(WINDOW, KeyProperties.AUTH_BIOMETRIC_STRONG or KeyProperties.AUTH_DEVICE_CREDENTIAL)
        } else {
            @Suppress("DEPRECATION")
            builder.setUserAuthenticationValidityDurationSeconds(WINDOW)
        }
        if (Build.VERSION.SDK_INT >= 28) builder.setUnlockedDeviceRequired(true)
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, STORE)
        generator.init(builder.build())
        val key = generator.generateKey()
        val info = SecretKeyFactory.getInstance(key.algorithm, STORE).getKeySpec(key, KeyInfo::class.java) as KeyInfo
        if (!protectedBy(info)) {
            removeAll()
            throw unprotected()
        }
        val seed = ByteArray(32)
        SecureRandom().nextBytes(seed)
        try {
            val publicKey = Ed25519PrivateKeyParameters(seed, 0).generatePublicKey().encoded
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            try {
                cipher.init(Cipher.ENCRYPT_MODE, key)
            } catch (e: UserNotAuthenticatedException) {
                throw KeyNeedsUnlock()
            }
            val iv = cipher.iv
            if (iv == null || iv.size != 12) throw KeyRefusal("native", "The phone's keystore sealed the key in a form the shell does not read.")
            return DeviceKey(DeviceKey.SEALED, publicKey, iv + cipher.doFinal(seed))
        } finally {
            seed.fill(0)
        }
    }

    override fun holds(key: DeviceKey): Boolean = try {
        keyStore().containsAlias(if (key.form == DeviceKey.KEYSTORE) SIGNING else SEALING)
    } catch (e: Exception) {
        false
    }

    override fun sign(key: DeviceKey, message: ByteArray): Signed = if (key.form == DeviceKey.KEYSTORE) signKeystore(message) else signSealed(key, message)

    private fun signKeystore(message: ByteArray): Signed {
        val signingKey = keyStore().getKey(SIGNING, null) as? PrivateKey ?: return Signed.Lost
        val signature = Signature.getInstance("Ed25519")
        try {
            signature.initSign(signingKey)
        } catch (e: KeyPermanentlyInvalidatedException) {
            return Signed.Lost
        } catch (e: UserNotAuthenticatedException) {
            return Signed.NeedsUnlock
        }
        return try {
            signature.update(message)
            Signed.Signature(signature.sign())
        } catch (e: GeneralSecurityException) {
            if (e.cause is UserNotAuthenticatedException) Signed.NeedsUnlock else throw e
        }
    }

    private fun signSealed(key: DeviceKey, message: ByteArray): Signed {
        val sealing = keyStore().getKey(SEALING, null) as? SecretKey ?: return Signed.Lost
        val box = key.seed ?: return Signed.Lost
        if (box.size <= 12) return Signed.Lost
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        try {
            cipher.init(Cipher.DECRYPT_MODE, sealing, GCMParameterSpec(128, box, 0, 12))
        } catch (e: KeyPermanentlyInvalidatedException) {
            return Signed.Lost
        } catch (e: UserNotAuthenticatedException) {
            return Signed.NeedsUnlock
        }
        val seed = try {
            cipher.doFinal(box, 12, box.size - 12)
        } catch (e: javax.crypto.AEADBadTagException) {
            return Signed.Lost
        }
        try {
            if (seed.size != 32) return Signed.Lost
            val seedKey = Ed25519PrivateKeyParameters(seed, 0)
            if (!seedKey.generatePublicKey().encoded.contentEquals(key.publicKey)) return Signed.Lost
            val signer = Ed25519Signer()
            signer.init(true, seedKey)
            signer.update(message, 0, message.size)
            return Signed.Signature(signer.generateSignature())
        } finally {
            seed.fill(0)
        }
    }

    override fun removeAll() {
        val store = keyStore()
        for (alias in listOf(SIGNING, SEALING)) {
            if (store.containsAlias(alias)) store.deleteEntry(alias)
        }
    }
}

/**
 * The person's presence as the phone asks it: BiometricPrompt with the fixed words, a fresh prompt each call, the person's strong
 * biometric or the device's PIN, pattern or password (from Android 11; before it a biometric or the device credential, which the
 * keystore then takes as the unlock only when it is a strong one). Asked from the plugin's runner, never the main thread, and waited
 * for with a generous bound: the system's sheet is the person's time.
 */
class AndroidPresence(private val activity: FragmentActivity) : Presence {
    private val main = Handler(Looper.getMainLooper())

    private fun authenticators(): Int =
        if (Build.VERSION.SDK_INT >= 30) {
            BiometricManager.Authenticators.BIOMETRIC_STRONG or BiometricManager.Authenticators.DEVICE_CREDENTIAL
        } else {
            BiometricManager.Authenticators.BIOMETRIC_WEAK or BiometricManager.Authenticators.DEVICE_CREDENTIAL
        }

    override fun confirm(reason: String): Boolean {
        if (Looper.myLooper() == Looper.getMainLooper()) return false
        val done = CountDownLatch(1)
        val confirmed = AtomicBoolean(false)
        main.post {
            try {
                if (activity.isFinishing || activity.isDestroyed) {
                    done.countDown()
                } else {
                    val prompt = BiometricPrompt(activity, Executor { r -> main.post(r) }, object : BiometricPrompt.AuthenticationCallback() {
                        override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) {
                            confirmed.set(true)
                            done.countDown()
                        }

                        override fun onAuthenticationError(errorCode: Int, errString: CharSequence) {
                            done.countDown()
                        }
                    })
                    val info = BiometricPrompt.PromptInfo.Builder().setTitle(reason).setAllowedAuthenticators(authenticators()).build()
                    prompt.authenticate(info)
                }
            } catch (e: Exception) {
                done.countDown()
            }
        }
        done.await(10, TimeUnit.MINUTES)
        return confirmed.get()
    }
}

/** The key's record: `conductor-key.json` in the no-backup folder, written to `.new`, synced and renamed over the old one. */
class FileKeyRecord(private val file: File) : KeyRecordFile {
    override fun read(): String? = if (!file.exists()) null else file.readText(Charsets.UTF_8)

    override fun write(text: String) {
        val next = File(file.parentFile, file.name + ".new")
        FileOutputStream(next).use { out ->
            out.write(text.toByteArray(Charsets.UTF_8))
            out.fd.sync()
        }
        if (!next.renameTo(file)) {
            next.delete()
            throw IOException("the key's record could not be kept")
        }
    }

    override fun delete() {
        File(file.parentFile, file.name + ".new").delete()
        if (file.exists() && !file.delete()) throw IOException("the key's record could not be removed")
    }
}

/** The key on this phone: the keystore, the person's unlock asked in the shell's activity, the record, the packaged policy. */
fun deviceKey(activity: FragmentActivity): ConductorKey =
    ConductorKey(
        AndroidKeyDevice(activity.applicationContext),
        AndroidPresence(activity),
        FileKeyRecord(File(activity.noBackupFilesDir, "conductor-key.json")),
        ConductorKey.packagedPolicy(),
    )
