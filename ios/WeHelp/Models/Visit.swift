import Foundation

// Video visits (#6). The contract is docs/MOBILE_CLIENT.md §15.

/// The join window: 10 minutes before the start until 30 after the end.
extension Appointment {
    static let visitEarly: TimeInterval = 10 * 60
    static let visitLate: TimeInterval = 30 * 60

    func inVisitWindow(now: Date = .now) -> Bool {
        status == .scheduled && now >= startsAt.addingTimeInterval(-Self.visitEarly)
            && now <= endsAt.addingTimeInterval(Self.visitLate)
    }

    var visitOpen: Bool { visitStatus == "open" }
}

struct VisitJoin: Decodable, Sendable {
    struct ICEServer: Decodable, Sendable {
        let urls: [String]
        let username: String
        let credential: String
    }

    let visitID: UUID
    let role: String
    let peerPresent: Bool
    let after: Int64
    let iceServers: [ICEServer]
    let expiresAt: Date

    var isProvider: Bool { role == "provider" }

    enum CodingKeys: String, CodingKey {
        case role
        case visitID = "visit_id"
        case peerPresent = "peer_present"
        case after
        case iceServers = "ice_servers"
        case expiresAt = "expires_at"
    }
}

/// An ICE candidate as it travels in a signal.
struct VisitCandidate: Codable, Sendable {
    let candidate: String
    let sdpMid: String?
    let sdpMLineIndex: Int32
}

struct VisitSignal: Decodable, Sendable {
    let seq: Int64
    let from: String
    let type: String
    let sdp: String?
    let candidate: VisitCandidate?
}

struct VisitSignalPage: Decodable, Sendable {
    let signals: [VisitSignal]
    let ended: Bool
}

/// What a phone sends: an offer or answer, a candidate, or a restart request.
struct OutgoingSignal: Encodable, Sendable {
    let type: String
    var sdp: String? = nil
    var candidate: VisitCandidate? = nil
}

/// The security check from MOBILE_CLIENT.md §15: the certificate the other
/// phone presented in DTLS must be the one its SDP (relayed by the Back Office
/// over the pinned connection) announced. Anything else means someone in the
/// path swapped it, and the call is refused.
enum DTLSFingerprint {
    /// The `a=fingerprint:<algorithm> <hex>` values in an SDP, normalised.
    static func inSDP(_ sdp: String) -> [String] {
        sdp.split(whereSeparator: \.isNewline).compactMap { line in
            let l = line.trimmingCharacters(in: .whitespaces)
            guard l.lowercased().hasPrefix("a=fingerprint:") else { return nil }
            let rest = l.dropFirst("a=fingerprint:".count)
            let parts = rest.split(separator: " ", maxSplits: 1)
            guard parts.count == 2 else { return nil }
            return normalise(algorithm: String(parts[0]), hex: String(parts[1]))
        }
    }

    static func normalise(algorithm: String, hex: String) -> String {
        algorithm.lowercased() + " " + hex.uppercased().filter { !$0.isWhitespace }
    }

    enum Result: Equatable { case match, mismatch, unknown }

    /// Compares the remote certificate from getStats with the remote SDP.
    /// `unknown` until the stats carry a certificate.
    static func check(remoteSDP: String, statsAlgorithm: String?, statsFingerprint: String?) -> Result {
        guard let alg = statsAlgorithm, let fp = statsFingerprint, !fp.isEmpty else { return .unknown }
        let announced = inSDP(remoteSDP)
        guard !announced.isEmpty else { return .mismatch }
        return announced.contains(normalise(algorithm: alg, hex: fp)) ? .match : .mismatch
    }
}
