import SwiftUI

/// The practices this device is paired with (#89): admin accounts first, then
/// provider, then patient. Tap one to switch the whole app to it, or pair
/// another practice. There is no remove here: only a practice removes a
/// device, from its staff page.
struct AccountsView: View {
    @Environment(AppState.self) private var appState
    @State private var pairing = false
    @State private var switching: String?

    private var groups: [(title: String, accounts: [Account])] {
        let all = appState.accounts
        return [("Admin", all.filter { $0.role == "admin" }),
                ("Provider", all.filter { $0.role == "provider" }),
                ("Patient", all.filter { $0.role != "admin" && $0.role != "provider" })]
            .filter { !$0.accounts.isEmpty }
    }

    var body: some View {
        List {
            ForEach(groups, id: \.title) { group in
                Section(group.title) {
                    ForEach(group.accounts) { a in
                        Button { open(a) } label: { AccountRow(account: a, active: a.id == appState.active?.id, busy: switching == a.id) }
                            .foregroundStyle(.primary)
                    }
                }
            }
            Section {
                Button("Pair another practice", systemImage: "qrcode.viewfinder") { pairing = true }
            } footer: {
                Text("Each practice gives you its own pairing code at the office. Accounts stay separate on this device.")
            }
        }
        .navigationTitle("Accounts")
        .fullScreenCover(isPresented: $pairing) { PairingView() }
    }

    private func open(_ a: Account) {
        guard a.id != appState.active?.id else { return }
        switching = a.id
        Task {
            await appState.switchTo(a)
            switching = nil
        }
    }
}

struct AccountRow: View {
    let account: Account
    var active = false
    var busy = false

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: icon)
                .font(.title2)
                .foregroundStyle(.tint)
                .frame(width: 32)
            VStack(alignment: .leading, spacing: 2) {
                Text(account.practice).font(.headline)
                Text(account.displayName.isEmpty ? account.roleLabel : "\(account.displayName) · \(account.roleLabel)")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            if busy {
                ProgressView()
            } else if active {
                Image(systemName: "checkmark").foregroundStyle(.tint).accessibilityLabel("Current")
            }
        }
        .contentShape(Rectangle())
    }

    private var icon: String {
        switch account.role {
        case "admin": "person.badge.key"
        case "provider": "stethoscope"
        default: "person.crop.circle"
        }
    }
}
