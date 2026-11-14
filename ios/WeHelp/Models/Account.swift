import Foundation

/// One practice this device is paired with, as one person and role (#89).
/// A device can hold many: a provider at three practices, an admin at one,
/// a patient at another. Stored in UserDefaults; it holds no secrets. Each
/// account's device key and refresh token are separate Keychain items.
struct Account: Codable, Identifiable, Hashable, Sendable {
    /// Local ID; scopes the Keychain items.
    let id: String
    var office: OfficeEndpoint
    var practice: String
    var displayName: String
    var role: String // admin, provider, patient
    var userID: String
    var deviceID: String
    var pairedAt: Date

    /// Accounts card order: admin first, then provider, then patient.
    var rank: Int {
        switch role {
        case "admin": 0
        case "provider": 1
        default: 2
        }
    }

    var roleLabel: String {
        switch role {
        case "admin": "Admin"
        case "provider": "Provider"
        case "patient": "Patient"
        default: role.capitalized
        }
    }

    /// The same person at the same practice (re-pairing replaces it).
    func sameIdentity(as other: Account) -> Bool {
        office.officeID == other.office.officeID && userID == other.userID && !userID.isEmpty
    }

    static func sorted(_ list: [Account]) -> [Account] {
        list.sorted {
            ($0.rank, $0.practice.localizedLowercase, $0.displayName.localizedLowercase)
                < ($1.rank, $1.practice.localizedLowercase, $1.displayName.localizedLowercase)
        }
    }
}
