import Foundation

enum APIError: Error, LocalizedError {
    case unauthorized(String)
    case http(Int, String)
    case notEnrolled

    var errorDescription: String? {
        switch self {
        case .unauthorized(let msg): "Not signed in: \(msg)"
        case .http(let code, let msg): "Server error \(code): \(msg)"
        case .notEnrolled: "This device is not enrolled."
        }
    }
}

struct Me: Decodable, Sendable {
    let userID: String
    let tenantID: String
    let role: String

    enum CodingKeys: String, CodingKey {
        case userID = "user_id", tenantID = "tenant_id", role
    }
}

/// The single network seam for the WeHelp backend. Attaches the Bearer
/// token and, on a 401, refreshes once (with a device proof) and retries.
/// Tokens never touch UserDefaults.
struct APIClient: Sendable {
    let baseURL: URL
    let session: SessionStore

    struct Ping: Decodable { let message: String }

    func ping() async throws -> Ping {
        try await send("GET", "api/v1/ping")
    }

    // MARK: Auth

    func login(tenant: String, email: String, password: String) async throws -> TokenPair {
        try await send("POST", "api/v1/auth/login",
                       body: ["tenant": tenant, "email": email, "password": password])
    }

    /// Swaps an unbound login token for a device-bound pair.
    func enroll(loginRefreshToken: String, key: DeviceKey, name: String) async throws -> TokenPair {
        let proof = try key.proof(purpose: "enroll", refreshToken: loginRefreshToken)
        let body: [String: any Sendable & Encodable] = [
            "refresh_token": loginRefreshToken,
            "public_key": try key.publicKeyDER.base64EncodedString(),
            "name": name,
            "timestamp": proof.timestamp,
            "signature": proof.signature,
        ]
        return try await send("POST", "api/v1/auth/enroll", body: AnyEncodable(body))
    }

    /// Rotates the stored refresh token using the device key.
    @discardableResult
    func refresh() async throws -> TokenPair {
        let session = session
        let base = baseURL
        return try await session.refresh {
            guard let token = await session.refreshToken else { throw APIError.unauthorized("no session") }
            guard let key = DeviceKey.load() else { throw APIError.notEnrolled }
            let proof = try key.proof(purpose: "refresh", refreshToken: token)
            let body: [String: any Sendable & Encodable] = [
                "refresh_token": token, "timestamp": proof.timestamp, "signature": proof.signature,
            ]
            return try await APIClient(baseURL: base, session: session)
                .send("POST", "api/v1/auth/refresh", body: AnyEncodable(body))
        }
    }

    /// Revokes this device's session on the server. Best effort.
    func logout() async {
        guard let token = await session.refreshToken else { return }
        _ = try? await send("POST", "api/v1/auth/logout", body: ["refresh_token": token]) as Empty
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

    // MARK: Plumbing

    /// Sends with the access token; on 401 refreshes once and retries.
    func authorized<T: Decodable>(_ method: String, _ path: String, query: [URLQueryItem] = [], body: (any Encodable & Sendable)? = nil) async throws -> T {
        if await session.accessToken == nil { try await refresh() }
        do {
            return try await send(method, path, query: query, body: body, bearer: await session.accessToken)
        } catch APIError.unauthorized {
            try await refresh()
            return try await send(method, path, query: query, body: body, bearer: await session.accessToken)
        }
    }

    func send<T: Decodable>(_ method: String, _ path: String, query: [URLQueryItem] = [], body: (any Encodable & Sendable)? = nil, bearer: String? = nil) async throws -> T {
        var url = baseURL.appending(path: path)
        if !query.isEmpty { url.append(queryItems: query) }
        var req = URLRequest(url: url)
        req.httpMethod = method
        if let body {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try JSONEncoder().encode(body)
        }
        if let bearer { req.setValue("Bearer \(bearer)", forHTTPHeaderField: "Authorization") }

        let (data, response) = try await URLSession.shared.data(for: req)
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
