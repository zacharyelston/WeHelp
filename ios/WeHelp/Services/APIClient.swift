import Foundation

/// Thin HTTP client for the WeHelp backend. Auth tokens will live in the
/// Keychain once login is implemented; do not store credentials in
/// UserDefaults.
struct APIClient: Sendable {
    let baseURL: URL

    struct Ping: Decodable {
        let message: String
    }

    func ping() async throws -> Ping {
        let url = baseURL.appendingPathComponent("api/v1/ping")
        let (data, response) = try await URLSession.shared.data(from: url)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
            throw URLError(.badServerResponse)
        }
        return try JSONDecoder().decode(Ping.self, from: data)
    }
}
