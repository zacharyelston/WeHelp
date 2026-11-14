import Foundation

/// A practice the user has signed in to (or a dev preset): how to reach its
/// Back Office through the Relay, and who to sign in as. Stored in
/// UserDefaults. It never contains a password or token, and pins are hashes
/// of public keys, so it is not a secret.
struct SavedConnection: Codable, Hashable, Identifiable, Sendable {
    var office: OfficeEndpoint
    /// The practice name as the server knows it; sent at sign-in.
    var tenant: String
    var email: String
    /// Optional display name ("Barnacle"). Not sent to the server.
    var label: String? = nil
    /// Debug builds only: prefilled from the seed data. Never persisted.
    var devPassword: String? = nil

    var id: String { "\(office.officeID)|\(tenant)|\(email)".lowercased() }
    var title: String { label.flatMap { $0.isEmpty ? nil : $0 } ?? tenant }
    var subtitle: String {
        let via = "via \(office.relay)"
        let who = email.isEmpty ? via : "\(email) · \(via)"
        return title == tenant ? who : "\(tenant) · \(who)"
    }

    enum CodingKeys: String, CodingKey { case office, tenant, email, label }

    /// Debug-build demo connections from Info.plist (ios/Config/*.xcconfig).
    /// `WeHelpDevServers` lists `Label|relay host:port|relay pin|office ID|office pin`
    /// entries separated by commas. Release builds compile this out.
    static func devPresets(bundle: Bundle = .main) -> [SavedConnection] {
        #if DEBUG
        func value(_ key: String) -> String? {
            (bundle.object(forInfoDictionaryKey: key) as? String)
                .map { $0.trimmingCharacters(in: .whitespaces) }
                .flatMap { $0.isEmpty ? nil : $0 }
        }
        guard let tenant = value("WeHelpDevTenant"), let email = value("WeHelpDevEmail") else { return [] }
        let password = value("WeHelpDevPassword")
        return (value("WeHelpDevServers") ?? "")
            .split(separator: ",")
            .compactMap { entry in
                let p = entry.split(separator: "|", omittingEmptySubsequences: false)
                    .map { $0.trimmingCharacters(in: .whitespaces) }
                guard p.count == 5 else { return nil }
                let office = OfficeEndpoint(relay: p[1], relayPin: p[2], officeID: p[3], officePin: p[4])
                guard office.problem == nil else { return nil }
                return SavedConnection(office: office, tenant: tenant, email: email,
                                       label: p[0].isEmpty ? nil : p[0], devPassword: password)
            }
        #else
        return []
        #endif
    }
}
