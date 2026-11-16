package com.wehelp.spike

import java.math.BigInteger
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import java.security.interfaces.ECPublicKey
import java.util.Base64
import javax.net.ssl.X509TrustManager

/**
 * WeHelp pins are base64(SHA-256(uncompressed X9.63 point)): 04 || X || Y,
 * 65 bytes for P-256. Not the SubjectPublicKeyInfo hash that OkHttp's
 * CertificatePinner and network-security-config use (MOBILE_CLIENT.md §3).
 */
object Pins {
    fun x963(key: ECPublicKey): ByteArray {
        val size = (key.params.curve.field.fieldSize + 7) / 8
        return byteArrayOf(4) + fixed(key.w.affineX, size) + fixed(key.w.affineY, size)
    }

    /** Big-endian, left-padded (or stripped of BigInteger's sign byte) to exactly [size] bytes. */
    fun fixed(n: BigInteger, size: Int): ByteArray {
        val raw = n.toByteArray()
        val src = if (raw.size > size) raw.copyOfRange(raw.size - size, raw.size) else raw
        return ByteArray(size - src.size) + src
    }

    fun pin(cert: X509Certificate): String? {
        val key = cert.publicKey as? ECPublicKey ?: return null
        return Base64.getEncoder().encodeToString(MessageDigest.getInstance("SHA-256").digest(x963(key)))
    }
}

/** Which hop's pin failed. Never retried, never bypassed. */
class PinMismatch(val party: String) : Exception("$party pin mismatch")

/**
 * Accepts only a leaf whose key matches [pin]; ignores the system trust store,
 * names and dates. A plain X509TrustManager (not the Extended one), so
 * Conscrypt does no hostname check: the pin is the identity.
 */
class PinTrustManager(private val pin: String, private val party: String) : X509TrustManager {
    @Volatile var failed = false
        private set

    override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        val leaf = chain?.firstOrNull() ?: throw CertificateException("empty chain")
        if (Pins.pin(leaf) != pin.trim()) {
            failed = true
            throw CertificateException("$party pin mismatch")
        }
    }

    override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) =
        throw CertificateException("client certificates are not used")

    override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
}
