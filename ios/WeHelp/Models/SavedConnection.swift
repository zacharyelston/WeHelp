import Foundation

/// A practice the user has signed in to (or a dev preset): where it is and
/// who to sign in as. Stored in UserDefaults. It never contains a password
/// or token, so it is not a secret.
struct SavedConnection: Codable, Hashable, Identifiable, Sendable {
    var serverURL: URL
    /// The practice name as the server knows it; sent at sign-in.
    var tenant: String
    var email: String
    /// Optional display name ("Barnacle"). Not sent to the server.
    var label: String? = nil
    /// Debug builds only: prefilled from the seed data. Never persisted.
    var devPassword: String? = nil

    var id: String { "\(serverURL.absoluteString)|\(tenant)|\(email)".lowercased() }
    var title: String { label.flatMap { $0.isEmpty ? nil : $0 } ?? tenant }
    var subtitle: String {
        let host = serverURL.host() ?? serverURL.absoluteString
        let who = email.isEmpty ? host : "\(email) · \(host)"
        return title == tenant ? who : "\(tenant) · \(who)"
    }

    enum CodingKeys: String, CodingKey { case serverURL, tenant, email, label }

    /// Debug-build demo connections from Info.plist (ios/Config/*.xcconfig).
    /// `WeHelpDevServers` lists `Label|URL` pairs separated by commas; when it
    /// is empty, one preset at the default server URL is used. Release builds
    /// leave the demo keys empty, so this returns [].
    static func devPresets(bundle: Bundle = .main, defaultServerURL: URL) -> [SavedConnection] {
        func value(_ key: String) -> String? {
            (bundle.object(forInfoDictionaryKey: key) as? String)
                .map { $0.trimmingCharacters(in: .whitespaces) }
                .flatMap { $0.isEmpty ? nil : $0 }
        }
        guard let tenant = value("WeHelpDevTenant"), let email = value("WeHelpDevEmail") else { return [] }
        let password = value("WeHelpDevPassword")

        var servers: [(label: String?, url: URL)] = (value("WeHelpDevServers") ?? "")
            .split(separator: ",")
            .compactMap { pair in
                let parts = pair.split(separator: "|", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
                guard parts.count == 2, let url = AppState.validURL(parts[1]) else { return nil }
                return (parts[0].isEmpty ? nil : parts[0], url)
            }
        if servers.isEmpty { servers = [(nil, defaultServerURL)] }

        return servers.map {
            SavedConnection(serverURL: $0.url, tenant: tenant, email: email, label: $0.label, devPassword: password)
        }
    }
}
