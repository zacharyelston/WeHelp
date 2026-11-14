import Foundation

/// How to reach one Back Office: through its Relay, pinning both the Relay's
/// TLS key and the office's TLS key (#36). The JSON keys match the office's
/// pairing identity (`wehelpd init` prints it; the QR code in #37 carries it).
/// Nothing here is secret: pins are hashes of public keys.
struct OfficeEndpoint: Codable, Hashable, Sendable {
    /// The Relay's CONNECT listener, "host:port".
    var relay: String
    /// Base64 SHA-256 of the Relay's TLS public key (X9.63).
    var relayPin: String
    /// The office ID (a UUID); the office's host is "<id>.wehelp.invalid".
    var officeID: String
    /// Base64 SHA-256 of the office's TLS public key (X9.63).
    var officePin: String

    enum CodingKeys: String, CodingKey {
        case relay, relayPin = "relay_pin", officeID = "office", officePin = "key_pin"
    }

    /// The reserved name space; the app's only ATS exception covers it.
    static let hostSuffix = ".wehelp.invalid"

    var officeHost: String { officeID.lowercased() + Self.hostSuffix }
    var baseURL: URL { URL(string: "https://\(officeHost)/")! }

    var relayHostPort: (host: String, port: UInt16)? {
        guard let colon = relay.lastIndex(of: ":") else { return nil }
        let host = String(relay[..<colon])
        guard !host.isEmpty, let port = UInt16(relay[relay.index(after: colon)...]), port > 0 else { return nil }
        return (host, port)
    }

    enum Problem: Error, LocalizedError, Equatable {
        case relayAddress, relayPin, officeID, officePin

        var errorDescription: String? {
            switch self {
            case .relayAddress: "The relay address must look like host:port."
            case .relayPin: "The relay pin isn't valid."
            case .officeID: "The office ID isn't valid."
            case .officePin: "The office pin isn't valid."
            }
        }
    }

    /// Checks every field; the first problem found, or nil.
    var problem: Problem? {
        if relayHostPort == nil { return .relayAddress }
        if !Self.isPin(relayPin) { return .relayPin }
        if UUID(uuidString: officeID) == nil { return .officeID }
        if !Self.isPin(officePin) { return .officePin }
        return nil
    }

    static func isPin(_ s: String) -> Bool {
        Data(base64Encoded: s.trimmingCharacters(in: .whitespaces))?.count == 32
    }

    /// Parses the pairing JSON an office prints (`"pairing": {...}` from
    /// `wehelpd init`), accepting either the object itself or the whole
    /// summary that contains it.
    static func fromPairingJSON(_ text: String) -> OfficeEndpoint? {
        guard let data = text.data(using: .utf8) else { return nil }
        if let e = try? JSONDecoder().decode(OfficeEndpoint.self, from: data), e.problem == nil { return e }
        struct Summary: Decodable { let pairing: OfficeEndpoint }
        if let s = try? JSONDecoder().decode(Summary.self, from: data), s.pairing.problem == nil { return s.pairing }
        return nil
    }
}
