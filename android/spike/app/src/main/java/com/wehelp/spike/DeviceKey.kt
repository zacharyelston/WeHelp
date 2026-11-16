package com.wehelp.spike

import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyInfo
import android.security.keystore.KeyProperties
import android.security.keystore.StrongBoxUnavailableException
import java.security.KeyFactory
import java.security.KeyPair
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.PrivateKey
import java.security.Signature
import java.security.spec.ECGenParameterSpec
import java.util.Base64

/** The device key (MOBILE_CLIENT.md §4): P-256 in the Android Keystore, sign-only, never exported. */
object DeviceKey {
    const val ALIAS = "wehelp-spike-device"

    class Made(val pair: KeyPair, val strongBoxRequested: Boolean, val strongBoxFallback: String?)

    /** Tries StrongBox first; on a phone without it, falls back to the TEE and says why. */
    fun create(): Made {
        delete()
        return try {
            Made(generate(strongBox = true), true, null)
        } catch (e: StrongBoxUnavailableException) {
            Made(generate(strongBox = false), true, "StrongBoxUnavailableException")
        }
    }

    private fun generate(strongBox: Boolean): KeyPair {
        val spec = KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_SIGN)
            .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
            .setDigests(KeyProperties.DIGEST_SHA256)
            .setIsStrongBoxBacked(strongBox)
            .build()
        return KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, "AndroidKeyStore")
            .apply { initialize(spec) }
            .generateKeyPair()
    }

    fun delete() {
        KeyStore.getInstance("AndroidKeyStore").apply { load(null) }.deleteEntry(ALIAS)
    }

    /** Where the key lives: StrongBox, TEE or software (which the product app refuses, #129). */
    fun securityLevel(key: PrivateKey): String {
        val info = KeyFactory.getInstance(key.algorithm, "AndroidKeyStore").getKeySpec(key, KeyInfo::class.java)
        return if (Build.VERSION.SDK_INT >= 31) {
            when (info.securityLevel) {
                KeyProperties.SECURITY_LEVEL_STRONGBOX -> "StrongBox"
                KeyProperties.SECURITY_LEVEL_TRUSTED_ENVIRONMENT -> "TEE"
                KeyProperties.SECURITY_LEVEL_SOFTWARE -> "software"
                else -> "unknown(${info.securityLevel})"
            }
        } else {
            @Suppress("DEPRECATION")
            if (info.isInsideSecureHardware) "secure hardware" else "software"
        }
    }

    /** A Keystore private key never yields its bytes. */
    fun isExportable(key: PrivateKey): Boolean = key.encoded != null

    /** Base64 DER SubjectPublicKeyInfo: what the Back Office parses. */
    fun publicKeyB64(pair: KeyPair): String = Base64.getEncoder().encodeToString(pair.public.encoded)

    fun proofMessage(purpose: String, token: String, unixSeconds: Long): ByteArray =
        "wehelp-device-v1\n$purpose\n$token\n$unixSeconds".toByteArray(Charsets.UTF_8)

    /** ASN.1 DER ECDSA over SHA-256, base64: what auth.VerifyDeviceProof checks. */
    fun sign(key: PrivateKey, message: ByteArray): String =
        Base64.getEncoder().encodeToString(
            Signature.getInstance("SHA256withECDSA").run {
                initSign(key)
                update(message)
                sign()
            },
        )
}
