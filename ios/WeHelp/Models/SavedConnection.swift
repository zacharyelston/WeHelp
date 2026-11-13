import Foundation

/// A practice the user has signed in to (or a dev preset): where it is and
/// who to sign in as. Stored in UserDefaults. It never contains a password
/// or token, so it is not a secret.
struct SavedConnection: Codable, Hashable, Identifiable, Sendable {
    var serverURL: URL
    var tenant: String
    var email: String
    /// Debug builds only: prefilled from the seed data. Never persisted.
    var devPassword: String? = nil

    var id: String { "\(serverURL.absoluteString)|\(tenant)|\(email)".lowercased() }
    var title: String { tenant }
    var subtitle: String { "\(email) · \(serverURL.host() ?? serverURL.absoluteString)" }

    enum CodingKeys: String, CodingKey { case serverURL, tenant, email }

    /// The Debug-build demo connection from Info.plist (ios/Config/*.xcconfig),
    /// or nil when those keys are empty (Release).
    static func devPreset(bundle: Bundle = .main, serverURL: URL) -> SavedConnection? {
        func value(_ key: String) -> String? {
            (bundle.object(forInfoDictionaryKey: key) as? String)
                .flatMap { $0.trimmingCharacters(in: .whitespaces).isEmpty ? nil : $0 }
        }
        guard let tenant = value("WeHelpDevTenant"), let email = value("WeHelpDevEmail") else { return nil }
        return SavedConnection(serverURL: serverURL, tenant: tenant, email: email,
                               devPassword: value("WeHelpDevPassword"))
    }
}
