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
    static let officeKey = "currentOffice"
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
        // Decoded one by one so a stale entry (e.g. a pre-Relay server URL)
        // is dropped without losing the rest.
        struct Lenient: Decodable {
            let value: SavedConnection?
            init(from decoder: Decoder) throws { value = try? SavedConnection(from: decoder) }
        }
        let stored = defaults.data(forKey: Self.connectionsKey)
            .flatMap { try? JSONDecoder().decode([Lenient].self, from: $0) }?
            .compactMap(\.value) ?? []
        var list = stored
        let hidden = hiddenPresets
        for preset in SavedConnection.devPresets(bundle: bundle)
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
        !SavedConnection.devPresets().isEmpty
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
        guard let office = api.office else { return }
        var conn = SavedConnection(office: office, tenant: tenant, email: email)
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

    /// Points the client at the connection's office and persists the choice.
    func use(_ conn: SavedConnection) {
        setOffice(conn.office)
    }

    init(defaults: UserDefaults = .standard, bundle: Bundle = .main) {
        self.defaults = defaults
        let stored = defaults.data(forKey: Self.officeKey)
            .flatMap { try? JSONDecoder().decode(OfficeEndpoint.self, from: $0) }
        api = APIClient(office: stored, session: session)
        defaults.removeObject(forKey: "serverURL") // pre-Relay setting (#36)
        loadConnections(bundle: bundle)
        // A revoked device (#10) is signed out as soon as the server says so.
        Task { [weak self] in
            for await _ in NotificationCenter.default.notifications(named: .weHelpSessionEnded) {
                guard let self, self.phase == .signedIn else { continue }
                await self.wipeLocalSession()
            }
        }
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

    // MARK: Pairing (#37)

    enum Pairing: Equatable {
        case idle
        case connecting
        case failed(String)
    }

    private(set) var pairing: Pairing = .idle
    private var pairingTask: Task<Void, Never>?

    /// Pairs this phone from a scanned QR code: a new device key, then redeem
    /// the invite through the Relay with both pins checked. Success signs in.
    func pair(with payload: PairingPayload) {
        pairingTask?.cancel()
        pairingTask = Task { await runPairing(payload) }
    }

    func cancelPairing() {
        pairingTask?.cancel()
        pairingTask = nil
        pairing = .idle
    }

    private func runPairing(_ p: PairingPayload) async {
        pairing = .connecting
        do {
            DeviceKey.delete()
            let key = try DeviceKey.create()
            let r = try await APIClient(office: p.office, session: session)
                .redeemInvite(p.invite, key: key, name: UIDevice.current.name)
            try await session.store(r.tokens)
            setOffice(p.office)
            lastTenant = r.practice
            rememberConnection(tenant: r.practice, email: "")
            me = try await api.me()
            pairing = .idle
            phase = .signedIn
        } catch is CancellationError {
            DeviceKey.delete()
        } catch {
            DeviceKey.delete()
            await session.clear()
            pairing = .failed(Self.pairingMessage(error))
        }
    }

    private static func pairingMessage(_ error: Error) -> String {
        switch error {
        case APIError.unauthorized:
            "This pairing code has expired or was already used. Ask the front desk for a new one."
        case APIError.http(409, _):
            "This phone is already paired. Ask the front desk to remove the old pairing first."
        default:
            error.localizedDescription
        }
    }

    // MARK: Office

    /// Points the client at an office (through its Relay) and persists it.
    func setOffice(_ office: OfficeEndpoint) {
        api = APIClient(office: office, session: session)
        if let data = try? JSONEncoder().encode(office) {
            defaults.set(data, forKey: Self.officeKey)
        }
    }
}
