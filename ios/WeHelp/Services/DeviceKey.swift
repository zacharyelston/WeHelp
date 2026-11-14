import CryptoKit
import Foundation

/// The per-device P-256 signing key that binds a patient session to this
/// phone. On hardware it lives in the Secure Enclave: the Keychain holds
/// only an opaque blob that the Enclave can use, never the private key.
/// The simulator has no Enclave, so simulator builds use a software key.
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

    // MARK: Storage

    static func load() -> DeviceKey? {
        guard let blob = Keychain.data(.deviceKey),
              let kind = Keychain.string(.deviceKeyKind).flatMap(Kind.init(rawValue:))
        else { return nil }
        return DeviceKey(kind: kind, blob: blob)
    }

    static func create() throws -> DeviceKey {
        let key: DeviceKey
        #if targetEnvironment(simulator)
        key = DeviceKey(kind: .software, blob: P256.Signing.PrivateKey().rawRepresentation)
        #else
        guard SecureEnclave.isAvailable else { throw KeyError.secureEnclaveUnavailable }
        key = DeviceKey(kind: .secureEnclave, blob: try SecureEnclave.P256.Signing.PrivateKey().dataRepresentation)
        #endif
        try Keychain.set(key.blob, for: .deviceKey)
        try Keychain.set(key.kind.rawValue, for: .deviceKeyKind)
        return key
    }

    static func delete() {
        Keychain.delete(.deviceKey)
        Keychain.delete(.deviceKeyKind)
        Keychain.delete(.deviceID)
    }
}
