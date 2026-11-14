import Foundation

enum APIError: Error, LocalizedError {
    case unauthorized(String)
    case http(Int, String)
    case notEnrolled
    case noOffice
    case badEndpoint(OfficeEndpoint.Problem)
    /// A pin check failed: the relay or office presented a key other than the
    /// one this phone was paired with. Never retried or bypassed.
    case pinMismatch(PinnedParty)

    var errorDescription: String? {
        switch self {
        case .unauthorized(let msg): "Not signed in: \(msg)"
        case .http(let code, let msg): "Server error \(code): \(msg)"
        case .notEnrolled: "This device is not enrolled."
        case .noOffice: "Choose a practice first."
        case .badEndpoint(let p): p.errorDescription
        case .pinMismatch(.relay):
            "The relay's identity didn't match the one this phone was set up with, so the app refused to connect. Check the relay pin, or ask your practice."
        case .pinMismatch(.office):
            "Your practice's identity didn't match the one this phone was set up with, so the app refused to connect. Check the office pin, or ask your practice to pair this phone again."
        }
    }
}

struct Me: Decodable, Sendable {
    let userID: String
    let tenantID: String
    let role: String
    let displayName: String?

    enum CodingKeys: String, CodingKey {
        case userID = "user_id", tenantID = "tenant_id", role, displayName = "display_name"
    }
}

/// The single network seam for the WeHelp backend. Every request goes
/// through the office's Relay with both pins checked (OfficeTransport).
/// Attaches the Bearer token and, on a 401, refreshes once (with a device
/// proof) and retries. Tokens never touch UserDefaults.
struct APIClient: Sendable {
    /// nil until the user picks a practice.
    let office: OfficeEndpoint?
    let session: SessionStore

    struct Ping: Decodable { let message: String }

    func ping() async throws -> Ping {
        try await send("GET", "api/v1/ping")
    }

    // MARK: Auth

    // MARK: Devices (#10)

    func devices() async throws -> [Device] {
        struct List: Decodable { let devices: [Device] }
        let list: List = try await authorized("GET", "api/v1/devices")
        return list.devices
    }

    func revokeDevice(_ id: String) async throws {
        let _: Empty = try await authorized("DELETE", "api/v1/devices/\(id)")
    }

    // MARK: Push (#7)

    func setPushToken(_ token: String, environment: String) async throws {
        let _: Empty = try await authorized("PUT", "api/v1/devices/current/push",
                                           body: ["token": token, "environment": environment])
    }

    // MARK: Pairing (#37)

    struct PairResult: Decodable, Sendable {
        let practice: String
        let patientName: String
        let role: String?
        let deviceID: String?
        let accessToken: String
        let refreshToken: String
        var tokens: TokenPair { TokenPair(accessToken: accessToken, refreshToken: refreshToken) }
        enum CodingKeys: String, CodingKey {
            case practice, role, patientName = "patient_name", deviceID = "device_id"
            case accessToken = "access_token", refreshToken = "refresh_token"
        }
    }

    /// Redeems a scanned invite with this phone's new device key. The office
    /// enrolls the key for the invite's patient and returns the session.
    func redeemInvite(_ invite: String, key: DeviceKey, name: String) async throws -> PairResult {
        let proof = try key.proof(purpose: "pair", refreshToken: invite)
        let body: [String: any Sendable & Encodable] = [
            "invite": invite,
            "public_key": try key.publicKeyDER.base64EncodedString(),
            "name": name,
            "timestamp": proof.timestamp,
            "signature": proof.signature,
        ]
        return try await send("POST", "api/v1/pairing/redeem", body: AnyEncodable(body))
    }

    /// Rotates the stored refresh token using the device key.
    @discardableResult
    func refresh() async throws -> TokenPair {
        let session = session
        let office = office
        return try await session.refresh {
            guard let token = await session.refreshToken else { throw APIError.unauthorized("no session") }
            guard let key = DeviceKey.load(account: session.accountID) else { throw APIError.notEnrolled }
            let proof = try key.proof(purpose: "refresh", refreshToken: token)
            let body: [String: any Sendable & Encodable] = [
                "refresh_token": token, "timestamp": proof.timestamp, "signature": proof.signature,
            ]
            return try await APIClient(office: office, session: session)
                .send("POST", "api/v1/auth/refresh", body: AnyEncodable(body))
        }
    }

    func me() async throws -> Me {
        try await authorized("GET", "api/v1/me")
    }

    // MARK: Links

    func links() async throws -> [CareLink] {
        try await authorized("GET", "api/v1/links")
    }

    // MARK: Messages

    /// The caller's inbox, newest first. Pass the previous page's
    /// `nextCursor` to continue.
    func messages(cursor: String? = nil, limit: Int = 50) async throws -> MessagePage {
        var query = [URLQueryItem(name: "limit", value: String(limit))]
        if let cursor { query.append(URLQueryItem(name: "cursor", value: cursor)) }
        return try await authorized("GET", "api/v1/messages", query: query)
    }

    /// Everyone the caller is linked with, newest conversation first (#67).
    func conversations() async throws -> [Conversation] {
        struct List: Decodable { let conversations: [Conversation] }
        let list: List = try await authorized("GET", "api/v1/conversations")
        return list.conversations
    }

    /// The whole conversation with one linked user, newest first (#67).
    func thread(with userID: UUID, cursor: String? = nil, limit: Int = 50) async throws -> MessagePage {
        var query = [URLQueryItem(name: "with", value: userID.uuidString.lowercased()),
                     URLQueryItem(name: "limit", value: String(limit))]
        if let cursor { query.append(URLQueryItem(name: "cursor", value: cursor)) }
        return try await authorized("GET", "api/v1/messages", query: query)
    }

    /// Idempotent on the server.
    func markRead(messageID: UUID) async throws {
        let _: Empty = try await authorized("POST", "api/v1/messages/\(messageID.uuidString.lowercased())/read")
    }

    @discardableResult
    func sendMessage(to recipientID: UUID, kind: String = "message", body: String) async throws -> CreatedMessage {
        let payload: [String: String] = [
            "recipient_id": recipientID.uuidString.lowercased(),
            "kind": kind,
            "body": body,
        ]
        return try await authorized("POST", "api/v1/messages", body: payload)
    }

    // MARK: Appointments

    /// Appointments where the caller is the patient (or provider), ordered
    /// by start time.
    func appointments() async throws -> [Appointment] {
        let list: AppointmentList = try await authorized("GET", "api/v1/appointments")
        return list.appointments
    }

    // MARK: Appointment requests (#68)

    /// The caller's requests, newest first: a patient's own, or those
    /// addressed to a provider.
    func appointmentRequests(status: String? = nil) async throws -> [AppointmentRequest] {
        let query = status.map { [URLQueryItem(name: "status", value: $0)] } ?? []
        let list: AppointmentRequestList = try await authorized("GET", "api/v1/appointment-requests", query: query)
        return list.requests
    }

    private struct NewRequest: Encodable, Sendable {
        let provider_id: String
        let preferred: [PreferredTime]
        let reason: String
    }

    @discardableResult
    func requestAppointment(provider: UUID, preferred: [PreferredTime], reason: String) async throws -> AppointmentRequest {
        try await authorized("POST", "api/v1/appointment-requests",
                             body: NewRequest(provider_id: provider.uuidString.lowercased(), preferred: preferred, reason: reason))
    }

    @discardableResult
    func cancelAppointmentRequest(_ id: UUID) async throws -> AppointmentRequest {
        try await authorized("POST", "api/v1/appointment-requests/\(id.uuidString.lowercased())/cancel")
    }

    // MARK: Plumbing

    /// Sends with the access token; on 401 refreshes once and retries.
    func authorized<T: Decodable>(_ method: String, _ path: String, query: [URLQueryItem] = [], body: (any Encodable & Sendable)? = nil) async throws -> T {
        if await session.accessToken == nil { try await refresh() }
        do {
            return try await send(method, path, query: query, body: body, bearer: await session.accessToken)
        } catch APIError.unauthorized {
            do {
                try await refresh()
            } catch APIError.unauthorized {
                // The session is gone for good (this device was revoked, #10,
                // or the refresh token expired). Let AppState sign out.
                NotificationCenter.default.post(name: .weHelpSessionEnded, object: session.accountID)
                throw APIError.unauthorized("This device was signed out.")
            }
            return try await send(method, path, query: query, body: body, bearer: await session.accessToken)
        }
    }

    func send<T: Decodable>(_ method: String, _ path: String, query: [URLQueryItem] = [], body: (any Encodable & Sendable)? = nil, bearer: String? = nil) async throws -> T {
        guard let office else { throw APIError.noOffice }
        var url = office.baseURL.appending(path: path)
        if !query.isEmpty { url.append(queryItems: query) }
        var req = URLRequest(url: url)
        req.httpMethod = method
        if let body {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try JSONEncoder().encode(body)
        }
        if let bearer { req.setValue("Bearer \(bearer)", forHTTPHeaderField: "Authorization") }

        let (data, response) = try await OfficeTransport.shared(for: office).data(for: req)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(status) else {
            let msg = (try? JSONDecoder().decode([String: String].self, from: data))?["error"] ?? "HTTP \(status)"
            throw status == 401 ? APIError.unauthorized(msg) : APIError.http(status, msg)
        }
        if T.self == Empty.self || data.isEmpty, let empty = Empty() as? T { return empty }
        return try Self.decoder.decode(T.self, from: data)
    }

    /// Go encodes `time.Time` as RFC 3339 with 0-9 fractional digits.
    static var decoder: JSONDecoder {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { decoder in
            let raw = try decoder.singleValueContainer().decode(String.self)
            if let date = parseRFC3339(raw) { return date }
            throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath,
                                                    debugDescription: "not an RFC 3339 date"))
        }
        return d
    }

    static func parseRFC3339(_ raw: String) -> Date? {
        if let d = try? Date.ISO8601FormatStyle(includingFractionalSeconds: true).parse(raw) { return d }
        return try? Date.ISO8601FormatStyle().parse(raw)
    }
}

struct Empty: Decodable, Sendable {}

extension Notification.Name {
    /// Posted when the server refuses a device's session for good. The
    /// object is the account ID (#89).
    static let weHelpSessionEnded = Notification.Name("WeHelpSessionEnded")
}

/// Encodes a heterogeneous [String: Encodable] dictionary as a JSON object.
struct AnyEncodable: Encodable, Sendable {
    let values: [String: any Sendable & Encodable]
    init(_ values: [String: any Sendable & Encodable]) { self.values = values }

    struct Key: CodingKey {
        var stringValue: String
        var intValue: Int? { nil }
        init(stringValue: String) { self.stringValue = stringValue }
        init?(intValue: Int) { nil }
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: Key.self)
        for (k, v) in values { try c.encode(v, forKey: Key(stringValue: k)) }
    }
}
