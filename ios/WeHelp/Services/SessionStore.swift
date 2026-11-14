import Foundation

struct TokenPair: Decodable, Sendable {
    let accessToken: String
    let refreshToken: String

    enum CodingKeys: String, CodingKey {
        case accessToken = "access_token"
        case refreshToken = "refresh_token"
    }

    init(accessToken: String, refreshToken: String) {
        self.accessToken = accessToken
        self.refreshToken = refreshToken
    }
}

/// Holds the session: the access token in memory only, the refresh token in
/// the Keychain. Refreshes are single-flight so concurrent 401s don't spend
/// the same single-use refresh token twice.
actor SessionStore {
    private(set) var accessToken: String?
    private var inFlight: Task<TokenPair, Error>?

    var refreshToken: String? { Keychain.string(.refreshToken) }

    func store(_ pair: TokenPair) throws {
        try Keychain.set(pair.refreshToken, for: .refreshToken)
        accessToken = pair.accessToken
    }

    func clear() {
        accessToken = nil
        inFlight?.cancel()
        inFlight = nil
        Keychain.delete(.refreshToken)
    }

    /// Runs `perform` unless a refresh is already running, in which case it
    /// waits for that one. Stores the result.
    func refresh(_ perform: @escaping @Sendable () async throws -> TokenPair) async throws -> TokenPair {
        if let task = inFlight { return try await task.value }
        let task = Task { try await perform() }
        inFlight = task
        defer { inFlight = nil }
        let pair = try await task.value
        try store(pair)
        return pair
    }
}
