import Foundation
import Security

/// Minimal generic-password Keychain wrapper. Items are readable after the
/// first unlock (so background refresh works) and never leave this device:
/// they are excluded from backups and device migration, which matches the
/// device-bound session model.
///
/// Every practice this device is paired with (#89) has its own items,
/// scoped by a local account ID, so nothing is shared between practices.
enum Keychain {
    enum Item: String {
        case refreshToken = "session.refreshToken"
        case deviceKey = "device.key"
        case deviceKeyKind = "device.keyKind"
        case deviceID = "device.id"

        /// This item for one account.
        func scoped(_ accountID: String) -> String { "\(rawValue).\(accountID)" }
    }

    private static var service: String { Bundle.main.bundleIdentifier ?? "wehelp" }

    private static func query(_ key: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
        ]
    }

    static func set(_ data: Data, for key: String) throws {
        delete(key)
        var item = query(key)
        item[kSecValueData as String] = data
        item[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(item as CFDictionary, nil)
        guard status == errSecSuccess else {
            throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
        }
    }

    static func set(_ string: String, for key: String) throws {
        try set(Data(string.utf8), for: key)
    }

    static func data(_ key: String) -> Data? {
        var q = query(key)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: AnyObject?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess else { return nil }
        return out as? Data
    }

    static func string(_ key: String) -> String? {
        data(key).flatMap { String(data: $0, encoding: .utf8) }
    }

    static func delete(_ key: String) {
        SecItemDelete(query(key) as CFDictionary)
    }

    /// Removes every item of one account.
    static func deleteAll(account accountID: String) {
        for item in [Item.refreshToken, .deviceKey, .deviceKeyKind, .deviceID] {
            delete(item.scoped(accountID))
        }
    }
}
