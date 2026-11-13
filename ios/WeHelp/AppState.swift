import Foundation
import UIKit

enum Role: String, Codable {
    case provider
    case patient
}

/// Session-scoped state: who is signed in and how to reach the backend.
@MainActor
@Observable
final class AppState {
    enum Phase: Equatable { case launching, signedOut, signedIn }

    /// UserDefaults keys. Neither is a secret.
    static let serverURLKey = "serverURL"
    static let tenantKey = "tenant"

    private(set) var phase: Phase = .launching
    private(set) var me: Me?
    var role: Role? { me.flatMap { Role(rawValue: $0.role) } }

    let session = SessionStore()
    private(set) var api: APIClient
    private let defaults: UserDefaults

    var lastTenant: String {
        get { defaults.string(forKey: Self.tenantKey) ?? "" }
        set { defaults.set(newValue, forKey: Self.tenantKey) }
    }

    init(defaults: UserDefaults = .standard, bundle: Bundle = .main) {
        self.defaults = defaults
        let stored = defaults.string(forKey: Self.serverURLKey)
        let url = stored.flatMap(Self.validURL) ?? Self.defaultServerURL(bundle: bundle)
        api = APIClient(baseURL: url, session: session)
    }

    // MARK: Session

    /// On launch: resume a device-bound session if this phone has one.
    func bootstrap() async {
        guard await session.refreshToken != nil, DeviceKey.load() != nil else {
            phase = .signedOut
            return
        }
        do {
            me = try await api.me()   // refreshes with the device key first
            phase = .signedIn
        } catch APIError.unauthorized, APIError.notEnrolled {
            await wipeLocalSession()
        } catch {
            // Server unreachable: keep the stored session so a later launch
            // can resume it; show sign-in for now.
            phase = .signedOut
        }
    }

    /// Password login, then enroll this device and keep only the
    /// device-bound session.
    func signIn(tenant: String, email: String, password: String) async throws {
        let login = try await api.login(tenant: tenant, email: email, password: password)
        DeviceKey.delete()
        let key = try DeviceKey.create()
        let bound = try await api.enroll(loginRefreshToken: login.refreshToken, key: key, name: UIDevice.current.name)
        try await session.store(bound)
        lastTenant = tenant
        me = try await api.me()
        phase = .signedIn
    }

    /// Revokes the device on the server, then wipes tokens and the device key.
    func signOut() async {
        await api.logout()
        await wipeLocalSession()
    }

    private func wipeLocalSession() async {
        await session.clear()
        DeviceKey.delete()
        me = nil
        phase = .signedOut
    }

    // MARK: Server URL

    /// Points the client at a new server and persists the choice.
    /// Returns false (and changes nothing) if the string is not an http(s) URL.
    @discardableResult
    func setServerURL(_ string: String) -> Bool {
        guard let url = Self.validURL(string) else { return false }
        api = APIClient(baseURL: url, session: session)
        defaults.set(url.absoluteString, forKey: Self.serverURLKey)
        return true
    }

    /// Forgets the user-chosen URL and returns to the build default.
    func resetServerURL(bundle: Bundle = .main) {
        defaults.removeObject(forKey: Self.serverURLKey)
        api = APIClient(baseURL: Self.defaultServerURL(bundle: bundle), session: session)
    }

    /// Build-time default from Info.plist (set per configuration in
    /// ios/Config/*.xcconfig), falling back to localhost.
    nonisolated static func defaultServerURL(bundle: Bundle = .main) -> URL {
        let raw = bundle.object(forInfoDictionaryKey: "WeHelpDefaultServerURL") as? String
        return raw.flatMap(validURL) ?? URL(string: "http://localhost:8080")!
    }

    nonisolated static func validURL(_ string: String) -> URL? {
        let trimmed = string.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let url = URL(string: trimmed),
              let scheme = url.scheme?.lowercased(), scheme == "http" || scheme == "https",
              url.host?.isEmpty == false
        else { return nil }
        return url
    }
}
