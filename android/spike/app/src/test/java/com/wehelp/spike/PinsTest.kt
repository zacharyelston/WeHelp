package com.wehelp.spike

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.math.BigInteger
import java.security.KeyPairGenerator
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec

class PinsTest {
    /** The X9.63 point must equal the last 65 bytes of the P-256 SubjectPublicKeyInfo. */
    @Test
    fun x963MatchesTheSpkiBitString() {
        repeat(50) {
            val kp = KeyPairGenerator.getInstance("EC").apply { initialize(ECGenParameterSpec("secp256r1")) }.generateKeyPair()
            val spki = kp.public.encoded
            val point = Pins.x963(kp.public as ECPublicKey)
            assertEquals(65, point.size)
            assertArrayEquals(spki.copyOfRange(spki.size - 65, spki.size), point)
        }
    }

    @Test
    fun fixedPadsAndStripsTheSignByte() {
        assertArrayEquals(ByteArray(31) + byteArrayOf(1), Pins.fixed(BigInteger.ONE, 32))
        val high = BigInteger(1, ByteArray(32) { 0xFF.toByte() }) // toByteArray() has a leading 0
        assertEquals(32, Pins.fixed(high, 32).size)
    }

    @Test
    fun endpointPinShape() {
        assertTrue(Endpoint.isPin("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="))
        assertFalse(Endpoint.isPin("abc"))
    }
}
