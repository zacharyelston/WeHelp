package com.wehelp.spike

import java.io.ByteArrayOutputStream
import java.io.IOException
import java.io.InputStream
import java.net.InetSocketAddress
import java.net.Socket
import javax.net.ssl.SNIHostName
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLException
import javax.net.ssl.SSLSocket

class RelayStatus(val code: Int) : IOException("relay answered $code")

/**
 * The two pinned hops (MOBILE_CLIENT.md §3), with plain sockets because
 * OkHttp can't use a TLS connection to its proxy:
 *  1. TCP to the Relay, TLS 1.3 pinned to relay_pin;
 *  2. CONNECT <office>.wehelp.invalid:443 over it;
 *  3. a second TLS 1.3 layered on that socket, SNI = the office host, pinned to key_pin.
 */
object Tunnel {
    fun open(e: Endpoint, timeoutMs: Int = 20_000): SSLSocket {
        val (host, port) = e.relayHostPort
        val raw = Socket()
        try {
            raw.connect(InetSocketAddress(host, port), timeoutMs)
            raw.soTimeout = timeoutMs

            val relayTm = PinTrustManager(e.relayPin, "relay")
            val relay = tls13(relayTm).socketFactory.createSocket(raw, host, port, true) as SSLSocket
            relay.enabledProtocols = arrayOf("TLSv1.3")
            handshake(relay, relayTm, "relay")

            val target = e.officeHost
            relay.outputStream.apply {
                write("CONNECT $target:443 HTTP/1.1\r\nHost: $target:443\r\n\r\n".toByteArray(Charsets.US_ASCII))
                flush()
            }
            val (code, _) = Http.readHead(relay.inputStream)
            if (code != 200) throw RelayStatus(code)

            val officeTm = PinTrustManager(e.officePin, "office")
            val office = tls13(officeTm).socketFactory.createSocket(relay, target, 443, true) as SSLSocket
            office.enabledProtocols = arrayOf("TLSv1.3")
            office.sslParameters = office.sslParameters.apply { serverNames = listOf(SNIHostName(target)) }
            handshake(office, officeTm, "office")
            return office
        } catch (t: Throwable) {
            runCatching { raw.close() }
            throw t
        }
    }

    private fun tls13(tm: PinTrustManager): SSLContext =
        SSLContext.getInstance("TLSv1.3").apply { init(null, arrayOf(tm), null) }

    private fun handshake(s: SSLSocket, tm: PinTrustManager, party: String) {
        try {
            s.startHandshake()
        } catch (ex: SSLException) {
            if (tm.failed) throw PinMismatch(party)
            throw ex
        }
    }
}

/** A minimal HTTP/1.1 client over one tunnel per request (Connection: close). */
object Http {
    class Response(val code: Int, val body: String)

    fun request(e: Endpoint, method: String, path: String, json: String? = null, bearer: String? = null): Response {
        Tunnel.open(e).use { s ->
            val body = json?.toByteArray(Charsets.UTF_8) ?: ByteArray(0)
            val head = buildString {
                append("$method $path HTTP/1.1\r\n")
                append("Host: ${e.officeHost}\r\n")
                append("Connection: close\r\n")
                append("Accept: application/json\r\n")
                if (bearer != null) append("Authorization: Bearer $bearer\r\n")
                if (json != null) append("Content-Type: application/json\r\n")
                append("Content-Length: ${body.size}\r\n\r\n")
            }
            s.outputStream.apply {
                write(head.toByteArray(Charsets.US_ASCII))
                write(body)
                flush()
            }
            val input = s.inputStream.buffered()
            val (code, headers) = readHead(input)
            // Never read "until close": a close doesn't come back through the
            // Relay's tunnel, so a body without a length would hang (spike finding).
            val bytes = when {
                code in 100..199 || code == 204 || code == 304 || method == "HEAD" -> ByteArray(0)
                headers["transfer-encoding"]?.contains("chunked", ignoreCase = true) == true -> readChunked(input)
                headers["content-length"] != null -> readN(input, headers.getValue("content-length").trim().toInt())
                else -> input.readBytes()
            }
            return Response(code, bytes.toString(Charsets.UTF_8))
        }
    }

    /** Reads a status line and headers, byte by byte so nothing past them is consumed. */
    fun readHead(input: InputStream): Pair<Int, Map<String, String>> {
        val lines = mutableListOf<String>()
        while (true) {
            val line = readLine(input)
            if (line.isEmpty()) break
            lines += line
            if (lines.size > 100) throw IOException("too many header lines")
        }
        val status = lines.firstOrNull() ?: throw IOException("no status line")
        val code = status.split(' ').getOrNull(1)?.toIntOrNull() ?: throw IOException("bad status line")
        val headers = lines.drop(1).mapNotNull { l ->
            val i = l.indexOf(':')
            if (i <= 0) null else l.substring(0, i).trim().lowercase() to l.substring(i + 1).trim()
        }.toMap()
        return code to headers
    }

    private fun readLine(input: InputStream): String {
        val buf = ByteArrayOutputStream()
        while (true) {
            val b = input.read()
            if (b < 0) throw IOException("connection closed in headers")
            if (b == '\n'.code) break
            if (b != '\r'.code) buf.write(b)
            if (buf.size() > 16_384) throw IOException("header line too long")
        }
        return buf.toString(Charsets.US_ASCII.name())
    }

    private fun readN(input: InputStream, n: Int): ByteArray {
        val out = ByteArray(n)
        var off = 0
        while (off < n) {
            val r = input.read(out, off, n - off)
            if (r < 0) throw IOException("short body")
            off += r
        }
        return out
    }

    private fun readChunked(input: InputStream): ByteArray {
        val out = ByteArrayOutputStream()
        while (true) {
            val size = readLine(input).substringBefore(';').trim().toInt(16)
            if (size == 0) {
                while (readLine(input).isNotEmpty()) { /* trailers */ }
                return out.toByteArray()
            }
            out.write(readN(input, size))
            readLine(input)
        }
    }
}
