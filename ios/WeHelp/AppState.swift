import Foundation
import UIKit

enum Role: String, Codable {
    case provider
    case patient
}

/// App state: the practices this device is paired with (#89), which one is
/// active, and that account's session.
@MainActor
@Observable
final class AppState {
    /// signedOut: no accounts yet (pair screen). unreachable: the active
    /// practice can't be reached right now; the account is kept and is never
    /// treated as unpaired.
    enum Phase: Equatable { case launching, signedOut, signedIn, unreachable(String) }

    /// UserDefaults keys. None is a secret.
    static let accountsKey = "accounts"
    static let activeKey = "activeAccount"
    static let pushTokenKey = "apnsToken" // an APNs device token; not a secret

    private(set) var phase: Phase = .launching
    private(set) var me: Me?
    var role: Role? { me.flatMap { Role(rawValue: $0.role) } }
    /// Provider and admin accounts get the practice view (#69).
    var isStaff: Bool { me?.role == "provider" || me?.role == "admin" }

    // MARK: Accounts (#89)

    /// Every practice this device is paired with, in Accounts-card order:
    /// admin first, then provider, then patient.
    private(set) var accounts: [Account] = []
    private(set) var active: Account?
    /// Bumped when a pairing adds an account, so the pair screen can close.
    private(set) var pairedCount = 0

    private var sessions: [String: SessionStore] = [:]
    private(set) var api: APIClient
    private let defaults: UserDefaults

    /// The active practice's name, for messages.
    var lastTenant: String { active?.practice ?? "" }

    private func session(for id: String) -> SessionStore {
        if let s = sessions[id] { return s }
        let s = SessionStore(accountID: id)
        sessions[id] = s
        return s
    }

    private func persist() {
        accounts = Account.sorted(accounts)
        if let data = try? JSONEncoder().encode(accounts) { defaults.set(data, forKey: Self.accountsKey) }
        defaults.set(active?.id, forKey: Self.activeKey)
    }

    // MARK: People

    /// Display names of the people the caller is linked with, from
    /// /api/v1/links. In memory only; never written to disk.
    private(set) var people: [UUID: String] = [:]

    func name(of id: UUID) -> String? { people[id].flatMap { $0.isEmpty ? nil : $0 } }

    func refreshPeople() async {
        guard let links = try? await api.links() else { return }
        var names: [UUID: String] = [:]
        for l in links where l.status != "revoked" {
            if let n = l.providerName { names[l.providerID] = n }
            if let n = l.patientName { names[l.patientID] = n }
        }
        people = names
    }

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        api = APIClient(office: nil, session: SessionStore(accountID: "none"))
        accounts = Account.sorted(defaults.data(forKey: Self.accountsKey)
            .flatMap { try? JSONDecoder().decode([Account].self, from: $0) } ?? [])
        let activeID = defaults.string(forKey: Self.activeKey)
        active = accounts.first { $0.id == activeID } ?? accounts.first
        if let a = active { api = APIClient(office: a.office, session: session(for: a.id)) }
        for key in ["serverURL", "savedConnections", "hiddenPresetConnections"] { // pre-#89 settings
            defaults.removeObject(forKey: key)
        }
        // Push (#7): register the APNs token with every paired practice.
        NotificationCenter.default.addObserver(forName: .weHelpPushToken, object: nil, queue: .main) { [weak self] note in
            let token = note.object as? String
            MainActor.assumeIsolated {
                guard let self, let token else { return }
                self.defaults.set(token, forKey: Self.pushTokenKey)
                Task { await self.registerPushToken() }
            }
        }
        // A device the practice revoked (#10) loses just that account.
        NotificationCenter.default.addObserver(forName: .weHelpSessionEnded, object: nil, queue: .main) { [weak self] note in
            let accountID = note.object as? String
            MainActor.assumeIsolated {
                guard let self, let accountID else { return }
                Task { await self.removeRevoked(accountID) }
            }
        }
    }

    // MARK: Launch and switching

    /// On launch: move a pre-#89 pairing over, then open the active account.
    func bootstrap() async {
        await migrateLegacyPairing()
        guard let a = active else {
            phase = .signedOut
            return
        }
        await open(a)
    }

    /// Makes `a` the active account and reaches its practice.
    func switchTo(_ a: Account) async {
        guard a.id != active?.id || phase != .signedIn else { return }
        me = nil
        people = [:]
        active = a
        persist()
        phase = .launching
        await open(a)
    }

    private func open(_ a: Account) async {
        api = APIClient(office: a.office, session: session(for: a.id))
        do {
            let m = try await api.me() // refreshes with this account's device key first
            me = m
            if let i = accounts.firstIndex(where: { $0.id == a.id }) {
                accounts[i].role = m.role
                accounts[i].userID = m.userID
                if let n = m.displayName, !n.isEmpty { accounts[i].displayName = n }
                active = accounts[i]
                persist()
            }
            phase = .signedIn
        } catch APIError.unauthorized, APIError.notEnrolled {
            await removeRevoked(a.id)
        } catch {
            // Practice unreachable (Relay down, no network, office offline):
            // keep the account and offer to try again or switch, never the
            // pair screen.
            phase = .unreachable(error.localizedDescription)
        }
    }

    /// Tries to reach the active practice again after `.unreachable`.
    func retry() {
        phase = .launching
    }

    /// The practice revoked this device (or its session ended for good):
    /// remove only that account, then open another or show the pair screen.
    private func removeRevoked(_ id: String) async {
        guard accounts.contains(where: { $0.id == id }) else { return }
        await session(for: id).clear()
        Keychain.deleteAll(account: id)
        sessions[id] = nil
        accounts.removeAll { $0.id == id }
        if active?.id == id {
            me = nil
            people = [:]
            active = accounts.first
            persist()
            if let next = active {
                phase = .launching
                await open(next)
            } else {
                api = APIClient(office: nil, session: SessionStore(accountID: "none"))
                phase = .signedOut
            }
        } else {
            persist()
        }
    }

    /// A device paired before #89 kept one unscoped key and token; give them
    /// an account so the pairing survives the update.
    private func migrateLegacyPairing() async {
        guard accounts.isEmpty,
              let office = defaults.data(forKey: "currentOffice").flatMap({ try? JSONDecoder().decode(OfficeEndpoint.self, from: $0) })
        else { return }
        let id = UUID().uuidString
        let s = session(for: id)
        guard await s.adoptLegacyToken(), DeviceKey.migrateLegacy(to: id) else { return }
        let a = Account(id: id, office: office, practice: defaults.string(forKey: "tenant") ?? "Your practice",
                        displayName: "", role: "patient", userID: "", deviceID: "", pairedAt: .now)
        accounts = [a]
        active = a
        persist()
        defaults.removeObject(forKey: "currentOffice")
        defaults.removeObject(forKey: "tenant")
    }

    // MARK: Push (#7)

    /// Sends the device's APNs token to every paired practice. Best effort;
    /// it runs again whenever iOS hands over the token or a practice is added.
    func registerPushToken() async {
        guard let token = defaults.string(forKey: Self.pushTokenKey) else { return }
        for a in accounts {
            let client = APIClient(office: a.office, session: session(for: a.id))
            try? await client.setPushToken(token, environment: Push.environment)
        }
    }

    // MARK: Pairing (#37, #89)

    enum Pairing: Equatable {
        case idle
        case connecting
        case failed(String)
    }

    private(set) var pairing: Pairing = .idle
    private var pairingTask: Task<Void, Never>?

    /// Pairs this device with a practice from a scanned QR code, adding an
    /// account: its own device key, then redeem the invite through the Relay
    /// with both pins checked. Other accounts are untouched.
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
        let id = UUID().uuidString
        let s = session(for: id)
        do {
            let key = try DeviceKey.create(account: id)
            let client = APIClient(office: p.office, session: s)
            let r = try await client.redeemInvite(p.invite, key: key, name: UIDevice.current.name)
            try await s.store(r.tokens)
            let m = try await client.me()
            var a = Account(id: id, office: p.office, practice: r.practice,
                            displayName: m.displayName ?? r.patientName, role: m.role,
                            userID: m.userID, deviceID: r.deviceID ?? "", pairedAt: .now)
            if a.displayName.isEmpty { a.displayName = r.patientName }
            // Re-pairing the same person at the same practice replaces the old
            // entry (its device stays listed for the practice to revoke).
            for old in accounts where old.sameIdentity(as: a) {
                Keychain.deleteAll(account: old.id)
                sessions[old.id] = nil
            }
            accounts.removeAll { $0.sameIdentity(as: a) }
            accounts.append(a)
            active = a
            me = m
            people = [:]
            api = client
            persist()
            pairing = .idle
            pairedCount += 1
            phase = .signedIn
            Task { await registerPushToken() }
        } catch is CancellationError {
            Keychain.deleteAll(account: id)
            sessions[id] = nil
        } catch {
            await s.clear()
            Keychain.deleteAll(account: id)
            sessions[id] = nil
            pairing = .failed(Self.pairingMessage(error))
        }
    }

    private static func pairingMessage(_ error: Error) -> String {
        switch error {
        case APIError.unauthorized:
            "This pairing code has expired or was already used. Ask the front desk for a new one."
        case APIError.http(409, _):
            "This device is already paired for that account."
        default:
            error.localizedDescription
        }
    }
}
