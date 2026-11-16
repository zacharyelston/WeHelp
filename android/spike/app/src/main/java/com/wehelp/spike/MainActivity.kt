package com.wehelp.spike

import android.app.Activity
import android.os.Build
import android.os.Bundle
import android.util.Log
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import com.google.mlkit.vision.barcode.common.Barcode
import com.google.mlkit.vision.codescanner.GmsBarcodeScannerOptions
import com.google.mlkit.vision.codescanner.GmsBarcodeScanning
import org.json.JSONObject
import java.util.Base64

/**
 * Spike #127. Paste (or send with `--es payload_b64 <base64 of the QR JSON>`)
 * a real pairing payload; the app then:
 *  1. makes a Keystore P-256 key and reports its security level;
 *  2. redeems the invite through the double-pinned tunnel;
 *  3. calls GET /me, refreshes with a device proof, calls /me again;
 *  4. checks that a wrong Relay pin and a wrong office pin each fail closed;
 *  5. signs out (revokes the device) and deletes the key.
 * Results go to the screen and logcat (tag WeHelpSpike). Tokens and the invite are never logged.
 */
class MainActivity : Activity() {
    private lateinit var out: TextView
    private lateinit var input: EditText

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        input = EditText(this).apply { hint = "Pairing JSON"; minLines = 3 }
        val scan = Button(this).apply { text = "Scan pairing QR" }
        val run = Button(this).apply { text = "Run with pasted JSON" }
        out = TextView(this).apply { setTextIsSelectable(true); textSize = 12f }
        val col = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(32, 32, 32, 32)
            addView(scan)
            addView(input)
            addView(run)
            addView(out)
        }
        setContentView(ScrollView(this).apply { addView(col) })
        run.setOnClickListener { start(input.text.toString()) }
        scan.setOnClickListener { scanQR() }

        intent.getStringExtra("payload_b64")?.let {
            start(String(Base64.getDecoder().decode(it), Charsets.UTF_8))
        }
    }

    /** Google's code scanner: QR only, no camera permission in this app. */
    private fun scanQR() {
        val options = GmsBarcodeScannerOptions.Builder().setBarcodeFormats(Barcode.FORMAT_QR_CODE).build()
        GmsBarcodeScanning.getClient(this, options).startScan()
            .addOnSuccessListener { code -> start(code.rawValue ?: "") }
            .addOnCanceledListener { log("scan cancelled") }
            .addOnFailureListener { e -> log("scan failed: ${e.message}") }
    }

    private fun start(text: String) {
        out.text = ""
        Thread { runSpike(text) }.start()
    }

    private fun log(line: String) {
        Log.i(TAG, line)
        runOnUiThread { out.append(line + "\n") }
    }

    private fun now() = System.currentTimeMillis() / 1000

    private fun timed(label: String, block: () -> Http.Response): Http.Response {
        val t0 = System.nanoTime()
        val r = block()
        log("$label -> ${r.code} in ${(System.nanoTime() - t0) / 1_000_000} ms")
        return r
    }

    private fun runSpike(text: String) {
        var passed = true
        fun check(ok: Boolean, what: String) {
            log((if (ok) "PASS " else "FAIL ") + what)
            if (!ok) passed = false
        }
        try {
            log("device: ${Build.MANUFACTURER} ${Build.MODEL}, Android ${Build.VERSION.RELEASE} (API ${Build.VERSION.SDK_INT})")
            val p = PairingPayload.parse(text)
            val e = p.endpoint
            log("office ${e.officeId} via relay ${e.relay}")

            // 1. Device key.
            val made = DeviceKey.create()
            val priv = made.pair.private
            val level = DeviceKey.securityLevel(priv)
            log("key: security level $level" + (made.strongBoxFallback?.let { " (StrongBox unavailable: $it; made without StrongBox)" } ?: ""))
            check(!DeviceKey.isExportable(priv), "private key is not exportable")
            check(level == "StrongBox" || level == "TEE" || level == "secure hardware", "key is hardware-backed ($level)")

            // 2. Pair.
            val ts = now()
            val redeem = JSONObject()
                .put("invite", p.invite)
                .put("public_key", DeviceKey.publicKeyB64(made.pair))
                .put("name", "Spike ${Build.MODEL}")
                .put("timestamp", ts)
                .put("signature", DeviceKey.sign(priv, DeviceKey.proofMessage("pair", p.invite, ts)))
            val r1 = timed("POST /api/v1/pairing/redeem") { Http.request(e, "POST", "/api/v1/pairing/redeem", redeem.toString()) }
            check(r1.code == 200, "pairing redeemed (${r1.code})")
            if (r1.code != 200) {
                log("body: ${r1.body.take(200)}")
                return
            }
            val pair = JSONObject(r1.body)
            log("paired: practice '${pair.optString("practice")}', role ${pair.optString("role")}, device ${pair.optString("device_id")}")
            var access = pair.getString("access_token")
            var refresh = pair.getString("refresh_token")

            // 3. Authenticated calls and a device-bound refresh.
            val me = timed("GET /api/v1/me") { Http.request(e, "GET", "/api/v1/me", bearer = access) }
            check(me.code == 200, "authenticated GET /me")
            val ts2 = now()
            val rr = JSONObject()
                .put("refresh_token", refresh)
                .put("timestamp", ts2)
                .put("signature", DeviceKey.sign(priv, DeviceKey.proofMessage("refresh", refresh, ts2)))
            val r2 = timed("POST /api/v1/auth/refresh") { Http.request(e, "POST", "/api/v1/auth/refresh", rr.toString()) }
            check(r2.code == 200, "refresh with a Keystore device proof")
            if (r2.code == 200) {
                val t = JSONObject(r2.body)
                access = t.getString("access_token")
                refresh = t.getString("refresh_token")
                val me2 = timed("GET /api/v1/me (new token)") { Http.request(e, "GET", "/api/v1/me", bearer = access) }
                check(me2.code == 200, "GET /me with the refreshed token")
            }

            // 4. Both pins fail closed.
            val zeroPin = Base64.getEncoder().encodeToString(ByteArray(32))
            check(failsWith(e.withRelayPin(zeroPin), "relay"), "wrong Relay pin fails as a relay pin mismatch")
            check(failsWith(e.withOfficePin(zeroPin), "office"), "wrong office pin fails as an office pin mismatch")

            // 5. Clean up: revoke this device and drop the key.
            val lo = timed("POST /api/v1/auth/logout") {
                Http.request(e, "POST", "/api/v1/auth/logout", JSONObject().put("refresh_token", refresh).toString())
            }
            check(lo.code == 204, "signed out (device revoked)")
        } catch (t: Throwable) {
            passed = false
            log("ERROR ${t.javaClass.simpleName}: ${t.message}")
            Log.e(TAG, "spike failed", t)
        } finally {
            runCatching { DeviceKey.delete() }
            log("SPIKE RESULT: " + if (passed) "PASS" else "FAIL")
        }
    }

    /** True only if the request fails with a pin mismatch for [party], and nothing else. */
    private fun failsWith(e: Endpoint, party: String): Boolean =
        try {
            Http.request(e, "GET", "/healthz")
            log("$party pin: request succeeded (it should not)")
            false
        } catch (m: PinMismatch) {
            log("$party pin: ${m.message}")
            m.party == party
        } catch (t: Throwable) {
            log("$party pin: unexpected ${t.javaClass.simpleName}: ${t.message}")
            false
        }

    companion object {
        const val TAG = "WeHelpSpike"
    }
}
