import Foundation

/// What the practice's Pair phone QR code holds (#37): the office's pairing
/// identity (how to reach it through the Relay, and both pins) plus a
/// single-use invite. Field names match the server's QRPayload.
struct PairingPayload: Decodable, Sendable {
    let office: OfficeEndpoint
    let invite: String

    private enum Keys: String, CodingKey { case v, invite }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: Keys.self)
        guard try c.decode(Int.self, forKey: .v) == 1 else {
            throw DecodingError.dataCorruptedError(forKey: .v, in: c, debugDescription: "unsupported pairing version")
        }
        invite = try c.decode(String.self, forKey: .invite)
        office = try OfficeEndpoint(from: decoder) // same object: relay, relay_pin, office, key_pin
    }

    /// Parses scanned or pasted text; nil unless every field is valid.
    static func parse(_ text: String) -> PairingPayload? {
        guard let data = text.trimmingCharacters(in: .whitespacesAndNewlines).data(using: .utf8),
              let p = try? JSONDecoder().decode(PairingPayload.self, from: data),
              p.office.problem == nil, p.invite.count >= 16, p.invite.count <= 64
        else { return nil }
        return p
    }
}
