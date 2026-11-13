import Foundation

enum Role: String, Codable {
    case provider
    case patient
}

/// Session-scoped state: who is signed in and how to reach the backend.
@Observable
final class AppState {
    /// UserDefaults key for the user-chosen server URL. Not a secret.
    static let serverURLKey = "serverURL"

    var role: Role?
    private(set) var api: APIClient

    init(defaults: UserDefaults = .standard, bundle: Bundle = .main) {
        self.defaults = defaults
        let stored = defaults.string(forKey: Self.serverURLKey)
        let url = stored.flatMap(Self.validURL) ?? Self.defaultServerURL(bundle: bundle)
        api = APIClient(baseURL: url)
    }

    private let defaults: UserDefaults

    /// Points the client at a new server and persists the choice.
    /// Returns false (and changes nothing) if the string is not an http(s) URL.
    @discardableResult
    func setServerURL(_ string: String) -> Bool {
        guard let url = Self.validURL(string) else { return false }
        api = APIClient(baseURL: url)
        defaults.set(url.absoluteString, forKey: Self.serverURLKey)
        return true
    }

    /// Forgets the user-chosen URL and returns to the build default.
    func resetServerURL(bundle: Bundle = .main) {
        defaults.removeObject(forKey: Self.serverURLKey)
        api = APIClient(baseURL: Self.defaultServerURL(bundle: bundle))
    }

    /// Build-time default from Info.plist (set per configuration in
    /// ios/Config/*.xcconfig), falling back to localhost.
    static func defaultServerURL(bundle: Bundle = .main) -> URL {
        let raw = bundle.object(forInfoDictionaryKey: "WeHelpDefaultServerURL") as? String
        return raw.flatMap(validURL) ?? URL(string: "http://localhost:8080")!
    }

    static func validURL(_ string: String) -> URL? {
        let trimmed = string.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let url = URL(string: trimmed),
              let scheme = url.scheme?.lowercased(), scheme == "http" || scheme == "https",
              url.host?.isEmpty == false
        else { return nil }
        return url
    }
}
