import Foundation
import Security

/// Minimal generic-password Keychain wrapper. Items are readable after the
/// first unlock (so background refresh works) and never leave this device:
/// they are excluded from backups and device migration, which matches the
/// device-bound session model.
enum Keychain {
    enum Account: String {
        case refreshToken = "session.refreshToken"
        case deviceKey = "device.key"
        case deviceKeyKind = "device.keyKind"
        case deviceID = "device.id"
    }

    private static var service: String { Bundle.main.bundleIdentifier ?? "wehelp" }

    private static func query(_ account: Account) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account.rawValue,
        ]
    }

    static func set(_ data: Data, for account: Account) throws {
        delete(account)
        var item = query(account)
        item[kSecValueData as String] = data
        item[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(item as CFDictionary, nil)
        guard status == errSecSuccess else {
            throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
        }
    }

    static func set(_ string: String, for account: Account) throws {
        try set(Data(string.utf8), for: account)
    }

    static func data(_ account: Account) -> Data? {
        var q = query(account)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: AnyObject?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess else { return nil }
        return out as? Data
    }

    static func string(_ account: Account) -> String? {
        data(account).flatMap { String(data: $0, encoding: .utf8) }
    }

    static func delete(_ account: Account) {
        SecItemDelete(query(account) as CFDictionary)
    }
}
