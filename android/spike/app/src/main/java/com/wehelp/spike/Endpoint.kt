package com.wehelp.spike

import org.json.JSONObject
import java.util.Base64
import java.util.UUID

/** How to reach one office through its Relay (docs/MOBILE_CLIENT.md §2). Nothing here is secret. */
data class Endpoint(
    val relay: String,
    val relayPin: String,
    val officeId: String,
    val officePin: String,
) {
    val officeHost: String get() = officeId.lowercase() + ".wehelp.invalid"

    val relayHostPort: Pair<String, Int>
        get() {
            val i = relay.lastIndexOf(':')
            require(i > 0) { "relay must be host:port" }
            return relay.substring(0, i) to relay.substring(i + 1).toInt()
        }

    fun withRelayPin(pin: String) = copy(relayPin = pin)
    fun withOfficePin(pin: String) = copy(officePin = pin)

    companion object {
        fun isPin(s: String): Boolean =
            runCatching { Base64.getDecoder().decode(s.trim()).size == 32 }.getOrDefault(false)
    }
}

/** The pairing QR's JSON: the endpoint plus a single-use invite. The invite is secret: never log it. */
class PairingPayload(val endpoint: Endpoint, val invite: String) {
    companion object {
        fun parse(text: String): PairingPayload {
            val o = JSONObject(text.trim())
            require(o.getInt("v") == 1) { "unsupported pairing version" }
            val e = Endpoint(
                relay = o.getString("relay"),
                relayPin = o.getString("relay_pin"),
                officeId = o.getString("office"),
                officePin = o.getString("key_pin"),
            )
            e.relayHostPort
            require(Endpoint.isPin(e.relayPin)) { "bad relay pin" }
            require(Endpoint.isPin(e.officePin)) { "bad office pin" }
            UUID.fromString(e.officeId)
            val invite = o.getString("invite")
            require(invite.length in 16..64) { "bad invite" }
            return PairingPayload(e, invite)
        }
    }
}
