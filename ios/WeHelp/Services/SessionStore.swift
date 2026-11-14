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

/// Holds one practice account's session (#89): the access token in memory
/// only, the refresh token in that account's Keychain item. Refreshes are
/// single-flight so concurrent 401s don't spend the same single-use refresh
/// token twice.
actor SessionStore {
    nonisolated let accountID: String
    private(set) var accessToken: String?
    private var inFlight: Task<TokenPair, Error>?

    init(accountID: String) { self.accountID = accountID }

    private var key: String { Keychain.Item.refreshToken.scoped(accountID) }

    var refreshToken: String? { Keychain.string(key) }

    func store(_ pair: TokenPair) throws {
        try Keychain.set(pair.refreshToken, for: key)
        accessToken = pair.accessToken
    }

    func clear() {
        accessToken = nil
        inFlight?.cancel()
        inFlight = nil
        Keychain.delete(key)
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

    /// Moves a pre-#89 refresh token into this account.
    func adoptLegacyToken() -> Bool {
        guard let token = Keychain.string(Keychain.Item.refreshToken.rawValue) else { return false }
        guard (try? Keychain.set(token, for: key)) != nil else { return false }
        Keychain.delete(Keychain.Item.refreshToken.rawValue)
        return true
    }
}
