import CryptoKit
import Foundation
import Network
import os

/// The only way the app talks to a Back Office (#36). Every request goes to
/// the office's Relay as an HTTP CONNECT proxy over TLS, pinned to the
/// Relay's key; inside it, TLS runs end to end with the office, pinned to the
/// office's key. Both checks fail closed. Proven in spike S1.
final class OfficeTransport: Sendable {
    let endpoint: OfficeEndpoint
    let session: URLSession
    private let failures: PinFailures

    private init(endpoint: OfficeEndpoint) {
        self.endpoint = endpoint
        let failures = PinFailures()
        self.failures = failures

        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 20
        config.waitsForConnectivity = false
        if let hp = endpoint.relayHostPort, let port = NWEndpoint.Port(rawValue: hp.port) {
            config.proxyConfigurations = [
                ProxyConfiguration(
                    httpCONNECTProxy: .hostPort(host: .init(hp.host), port: port),
                    tlsOptions: Pinning.relayTLSOptions(pin: endpoint.relayPin, failures: failures))
            ]
        }
        session = URLSession(configuration: config,
                             delegate: OfficePinningDelegate(pin: endpoint.officePin, failures: failures),
                             delegateQueue: nil)
    }

    private static let cache = OSAllocatedUnfairLock(initialState: [OfficeEndpoint: OfficeTransport]())

    /// One transport (and URLSession) per endpoint, reused across requests.
    static func shared(for endpoint: OfficeEndpoint) -> OfficeTransport {
        cache.withLock { cache in
            if let t = cache[endpoint] { return t }
            let t = OfficeTransport(endpoint: endpoint)
            cache[endpoint] = t
            return t
        }
    }

    /// Sends a request; a failed pin check becomes `APIError.pinMismatch`.
    func data(for request: URLRequest) async throws -> (Data, URLResponse) {
        if let problem = endpoint.problem { throw APIError.badEndpoint(problem) }
        failures.reset()
        do {
            return try await session.data(for: request)
        } catch {
            if let which = failures.last { throw APIError.pinMismatch(which) }
            throw error
        }
    }
}

/// Which pin failed, recorded by the verify callbacks so errors can say so.
enum PinnedParty: String, Sendable {
    case relay, office
}

final class PinFailures: Sendable {
    private let state = OSAllocatedUnfairLock<PinnedParty?>(initialState: nil)
    func record(_ p: PinnedParty) { state.withLock { $0 = p } }
    func reset() { state.withLock { $0 = nil } }
    var last: PinnedParty? { state.withLock { $0 } }
}

enum Pinning {
    /// TLS options for the hop to the Relay, accepting only the pinned Relay
    /// key. Built outside any actor: the verify block runs on a background
    /// queue and must not inherit main-actor isolation (Swift 6; spike S1).
    nonisolated static func relayTLSOptions(pin: String, failures: PinFailures) -> NWProtocolTLS.Options {
        let tls = NWProtocolTLS.Options()
        sec_protocol_options_set_min_tls_protocol_version(tls.securityProtocolOptions, .TLSv13)
        sec_protocol_options_set_verify_block(tls.securityProtocolOptions, { _, trust, complete in
            let ok = matches(trust: sec_trust_copy_ref(trust).takeRetainedValue(), pin: pin)
            if !ok { failures.record(.relay) }
            complete(ok)
        }, DispatchQueue(label: "wehelp.relay-pin"))
        return tls
    }

    /// Pin = base64 SHA-256 of the leaf key's external representation (X9.63
    /// for EC keys): the same value the office and Relay print.
    nonisolated static func matches(trust: SecTrust, pin: String) -> Bool {
        guard let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
              let leaf = chain.first,
              let key = SecCertificateCopyKey(leaf),
              let raw = SecKeyCopyExternalRepresentation(key, nil) as Data?
        else { return false }
        return Data(SHA256.hash(data: raw)).base64EncodedString() == pin.trimmingCharacters(in: .whitespaces)
    }
}

/// Inner TLS: only the pinned office key is accepted, never the system trust
/// store. ATS still evaluates the credential, so trust is anchored to the
/// pinned certificate alone; the scoped ATS exception for `wehelp.invalid`
/// lets that self-signed anchor through (spike S1).
final class OfficePinningDelegate: NSObject, URLSessionTaskDelegate, Sendable {
    let pin: String
    let failures: PinFailures

    init(pin: String, failures: PinFailures) {
        self.pin = pin
        self.failures = failures
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didReceive challenge: URLAuthenticationChallenge) async
        -> (URLSession.AuthChallengeDisposition, URLCredential?) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust
        else { return (.cancelAuthenticationChallenge, nil) }
        guard Pinning.matches(trust: trust, pin: pin),
              let leaf = (SecTrustCopyCertificateChain(trust) as? [SecCertificate])?.first
        else {
            failures.record(.office)
            return (.cancelAuthenticationChallenge, nil)
        }
        SecTrustSetAnchorCertificates(trust, [leaf] as CFArray)
        SecTrustSetAnchorCertificatesOnly(trust, true)
        return (.useCredential, URLCredential(trust: trust))
    }
}
