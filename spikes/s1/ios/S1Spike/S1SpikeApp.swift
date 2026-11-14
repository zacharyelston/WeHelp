import CryptoKit
import Network
import Security
import SwiftUI

// Spike S1 (#32): reach a Back Office through a CONNECT relay with TLS 1.3 to
// a pinned, self-signed office key. Throwaway code; see docs/spikes/S1.md.

@main
struct S1SpikeApp: App {
    var body: some Scene { WindowGroup { SpikeView() } }
}

struct SpikeView: View {
    // Defaults match `s1 keys -relay-ip 192.168.1.20` output; edit in the UI if regenerated.
    @State private var relayHost = "192.168.1.20"
    @State private var relayPort = "8443"
    // Public-key hashes from this spike run's `s1 keys` (not secrets).
    @State private var relayPin = "idNPx46WeRz3IL8+VlrlbsLhxIoaKnnlufzZGj0IyB4="
    @State private var officeHost = "office-1.wehelp.invalid"
    @State private var officePin = "aiw2CxgF5OeCMRBaXsGZBjEhSaWgCT76gG/z/BDoc4c="
    @State private var log: [String] = []
    @State private var running = false

    var body: some View {
        NavigationStack {
            Form {
                Section("Relay (CONNECT over TLS)") {
                    TextField("host", text: $relayHost).autocorrectionDisabled().textInputAutocapitalization(.never)
                    TextField("port", text: $relayPort).keyboardType(.numberPad)
                    TextField("relay key pin (base64)", text: $relayPin).font(.caption.monospaced()).autocorrectionDisabled().textInputAutocapitalization(.never)
                }
                Section("Office (pinned, end to end)") {
                    TextField("office host", text: $officeHost).autocorrectionDisabled().textInputAutocapitalization(.never)
                    TextField("office key pin (base64)", text: $officePin).font(.caption.monospaced()).autocorrectionDisabled().textInputAutocapitalization(.never)
                }
                Section {
                    Button("Run checks") { Task { await run() } }.disabled(running)
                }
                Section("Results") {
                    ForEach(Array(log.enumerated()), id: \.offset) { _, line in
                        Text(line).font(.caption.monospaced()).accessibilityIdentifier("result")
                    }
                }
            }
            .navigationTitle("Spike S1")
        }
    }

    private func run() async {
        running = true
        defer { running = false }
        log = []
        // 1. Correct pins: should succeed.
        await check("good pins", officePin: officePin, relayPin: relayPin, marker: "PLAINTEXT-MARKER-S1-ios-good")
        // 2. Wrong office pin: must fail closed (a relay or attacker presenting another key).
        await check("wrong office pin", officePin: Data(repeating: 1, count: 32).base64EncodedString(), relayPin: relayPin, marker: "PLAINTEXT-MARKER-S1-ios-badoffice")
        // 3. Wrong relay pin: must refuse to talk to an unknown relay.
        await check("wrong relay pin", officePin: officePin, relayPin: Data(repeating: 2, count: 32).base64EncodedString(), marker: "PLAINTEXT-MARKER-S1-ios-badrelay")
    }

    private func check(_ name: String, officePin: String, relayPin: String, marker: String) async {
        guard let port = NWEndpoint.Port(relayPort) else { log.append("\(name): bad port"); return }
        let config = URLSessionConfiguration.ephemeral
        let tls = Pinning.relayTLSOptions(pin: relayPin)
        config.proxyConfigurations = [
            ProxyConfiguration(httpCONNECTProxy: .hostPort(host: .init(relayHost), port: port), tlsOptions: tls)
        ]
        let delegate = PinningDelegate(pin: officePin)
        let session = URLSession(configuration: config, delegate: delegate, delegateQueue: nil)
        defer { session.finishTasksAndInvalidate() }
        var req = URLRequest(url: URL(string: "https://\(officeHost)/api/v1/ping")!)
        req.setValue(marker, forHTTPHeaderField: "X-Spike-Marker")
        req.timeoutInterval = 10
        let start = Date()
        do {
            let (data, resp) = try await session.data(for: req)
            let code = (resp as? HTTPURLResponse)?.statusCode ?? 0
            let ms = Int(Date().timeIntervalSince(start) * 1000)
            log.append("\(name): HTTP \(code) \(String(decoding: data, as: UTF8.self)) (\(ms) ms)")
        } catch {
            log.append("\(name): FAILED \((error as NSError).code) \(error.localizedDescription)")
        }
    }
}

enum Pinning {
    /// TLS options for the hop to the relay, accepting only the pinned relay
    /// key. Built outside any actor: the verify block runs on a background
    /// queue, so it must not inherit the view's main-actor isolation.
    nonisolated static func relayTLSOptions(pin: String) -> NWProtocolTLS.Options {
        let tls = NWProtocolTLS.Options()
        sec_protocol_options_set_verify_block(tls.securityProtocolOptions, { _, trust, complete in
            complete(matches(trust: sec_trust_copy_ref(trust).takeRetainedValue(), pin: pin))
        }, DispatchQueue(label: "s1.relay-pin"))
        return tls
    }

    /// Pin = base64 SHA-256 of the leaf key's external representation
    /// (X9.63 for EC keys), matching `s1 keys`.
    static func matches(trust: SecTrust, pin: String) -> Bool {
        guard let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
              let leaf = chain.first,
              let key = SecCertificateCopyKey(leaf),
              let raw = SecKeyCopyExternalRepresentation(key, nil) as Data?
        else { return false }
        let digest = Data(SHA256.hash(data: raw)).base64EncodedString()
        return digest == pin
    }
}

final class PinningDelegate: NSObject, URLSessionDelegate, Sendable {
    let pin: String
    init(pin: String) { self.pin = pin }

    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge) async
        -> (URLSession.AuthChallengeDisposition, URLCredential?) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust
        else { return (.performDefaultHandling, nil) }
        // Fail closed: only the pinned office key is accepted, never the system trust store.
        guard Pinning.matches(trust: trust, pin: pin),
              let leaf = (SecTrustCopyCertificateChain(trust) as? [SecCertificate])?.first
        else { return (.cancelAuthenticationChallenge, nil) }
        // ATS still runs system trust on the credential, so anchor the trust in
        // the certificate we just pinned (and only that one). No ATS exception.
        SecTrustSetAnchorCertificates(trust, [leaf] as CFArray)
        SecTrustSetAnchorCertificatesOnly(trust, true)
        return (.useCredential, URLCredential(trust: trust))
    }
}
