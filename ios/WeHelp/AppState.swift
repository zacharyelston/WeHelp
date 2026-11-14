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

    // MARK: Saved connections

    static let connectionsKey = "savedConnections"
    static let hiddenPresetsKey = "hiddenPresetConnections"

    /// Most recent first. The Debug demo preset is listed after the user's
    /// own connections unless one of them already matches it, or the user
    /// deleted or edited it away.
    private(set) var savedConnections: [SavedConnection] = []

    private var hiddenPresets: Set<String> {
        get { Set(defaults.stringArray(forKey: Self.hiddenPresetsKey) ?? []) }
        set { defaults.set(Array(newValue), forKey: Self.hiddenPresetsKey) }
    }

    private func loadConnections(bundle: Bundle) {
        let stored = defaults.data(forKey: Self.connectionsKey)
            .flatMap { try? JSONDecoder().decode([SavedConnection].self, from: $0) } ?? []
        var list = stored
        let hidden = hiddenPresets
        for preset in SavedConnection.devPresets(bundle: bundle, defaultServerURL: Self.defaultServerURL(bundle: bundle))
        where !hidden.contains(preset.id) {
            if let i = list.firstIndex(where: { $0.id == preset.id }) {
                list[i].devPassword = preset.devPassword
                if list[i].label == nil { list[i].label = preset.label }
            } else {
                list.append(preset)
            }
        }
        savedConnections = list
    }

    /// Debug builds: whether any demo presets exist to restore.
    var hasDevPresets: Bool {
        !SavedConnection.devPresets(defaultServerURL: Self.defaultServerURL()).isEmpty
    }

    /// Brings back demo presets the user deleted or edited away.
    func restoreDevPresets() {
        hiddenPresets = []
        persistConnections()
        loadConnections(bundle: .main)
    }

    private func persistConnections() {
        if let data = try? JSONEncoder().encode(savedConnections) {
            defaults.set(data, forKey: Self.connectionsKey)
        }
    }

    /// Moves (or inserts) the connection to the top of the list.
    func rememberConnection(tenant: String, email: String) {
        var conn = SavedConnection(serverURL: api.baseURL, tenant: tenant, email: email)
        if let existing = savedConnections.first(where: { $0.id == conn.id }) {
            conn.devPassword = existing.devPassword
            conn.label = existing.label
        }
        savedConnections.removeAll { $0.id == conn.id }
        savedConnections.insert(conn, at: 0)
        persistConnections()
    }

    /// Adds a connection, or replaces `replacing` (the old ID) when editing.
    func saveConnection(_ conn: SavedConnection, replacing oldID: SavedConnection.ID? = nil) {
        if let oldID, oldID != conn.id,
           savedConnections.first(where: { $0.id == oldID })?.devPassword != nil {
            hiddenPresets.insert(oldID)   // an edited preset must not come back
        }
        let index = oldID.flatMap { id in savedConnections.firstIndex { $0.id == id } }
        savedConnections.removeAll { $0.id == conn.id || $0.id == oldID }
        savedConnections.insert(conn, at: min(index ?? 0, savedConnections.count))
        persistConnections()
    }

    func forgetConnection(_ conn: SavedConnection) {
        if conn.devPassword != nil { hiddenPresets.insert(conn.id) }
        savedConnections.removeAll { $0.id == conn.id }
        persistConnections()
    }

    /// Points the client at the connection's server.
    func use(_ conn: SavedConnection) {
        setServerURL(conn.serverURL.absoluteString)
    }

    init(defaults: UserDefaults = .standard, bundle: Bundle = .main) {
        self.defaults = defaults
        let stored = defaults.string(forKey: Self.serverURLKey)
        let url = stored.flatMap(Self.validURL) ?? Self.defaultServerURL(bundle: bundle)
        api = APIClient(baseURL: url, session: session)
        loadConnections(bundle: bundle)
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
        rememberConnection(tenant: tenant, email: email)
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
