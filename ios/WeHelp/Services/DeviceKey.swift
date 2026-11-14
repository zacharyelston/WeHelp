import CryptoKit
import Foundation

/// The per-device, per-practice P-256 signing key that binds a session to
/// this device. On hardware it lives in the Secure Enclave: the Keychain
/// holds only an opaque blob that the Enclave can use, never the private key.
/// The simulator has no Enclave, so simulator builds use a software key.
/// Each practice account (#89) has its own key.
struct DeviceKey: Sendable {
    enum Kind: String { case secureEnclave, software }

    enum KeyError: Error, LocalizedError {
        case secureEnclaveUnavailable
        var errorDescription: String? { "This device has no Secure Enclave." }
    }

    let kind: Kind
    private let blob: Data

    /// DER SubjectPublicKeyInfo, as the server expects.
    var publicKeyDER: Data {
        get throws {
            switch kind {
            case .secureEnclave:
                try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: blob).publicKey.derRepresentation
            case .software:
                try P256.Signing.PrivateKey(rawRepresentation: blob).publicKey.derRepresentation
            }
        }
    }

    /// DER ECDSA signature over SHA-256(message).
    func sign(_ message: Data) throws -> Data {
        switch kind {
        case .secureEnclave:
            try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: blob).signature(for: message).derRepresentation
        case .software:
            try P256.Signing.PrivateKey(rawRepresentation: blob).signature(for: message).derRepresentation
        }
    }

    /// Signs the proof the server checks (see internal/auth/devicekey.go).
    func proof(purpose: String, refreshToken: String, at date: Date = .now) throws -> (timestamp: Int64, signature: String) {
        let ts = Int64(date.timeIntervalSince1970)
        let message = Data("wehelp-device-v1\n\(purpose)\n\(refreshToken)\n\(ts)".utf8)
        return (ts, try sign(message).base64EncodedString())
    }

    // MARK: Storage (per account)

    static func load(account: String) -> DeviceKey? {
        guard let blob = Keychain.data(Keychain.Item.deviceKey.scoped(account)),
              let kind = Keychain.string(Keychain.Item.deviceKeyKind.scoped(account)).flatMap(Kind.init(rawValue:))
        else { return nil }
        return DeviceKey(kind: kind, blob: blob)
    }

    static func create(account: String) throws -> DeviceKey {
        let key: DeviceKey
        #if targetEnvironment(simulator)
        key = DeviceKey(kind: .software, blob: P256.Signing.PrivateKey().rawRepresentation)
        #else
        guard SecureEnclave.isAvailable else { throw KeyError.secureEnclaveUnavailable }
        key = DeviceKey(kind: .secureEnclave, blob: try SecureEnclave.P256.Signing.PrivateKey().dataRepresentation)
        #endif
        try Keychain.set(key.blob, for: Keychain.Item.deviceKey.scoped(account))
        try Keychain.set(key.kind.rawValue, for: Keychain.Item.deviceKeyKind.scoped(account))
        return key
    }

    // MARK: Before #89: one unscoped key per device

    /// Moves a pre-#89 key to an account, returning whether one existed.
    static func migrateLegacy(to account: String) -> Bool {
        guard let blob = Keychain.data(Keychain.Item.deviceKey.rawValue),
              let kind = Keychain.string(Keychain.Item.deviceKeyKind.rawValue)
        else { return false }
        do {
            try Keychain.set(blob, for: Keychain.Item.deviceKey.scoped(account))
            try Keychain.set(kind, for: Keychain.Item.deviceKeyKind.scoped(account))
        } catch {
            return false
        }
        for item in [Keychain.Item.deviceKey, .deviceKeyKind, .deviceID] {
            Keychain.delete(item.rawValue)
        }
        return true
    }
}
