import SwiftUI

/// Add or edit a practice connection: how to reach its office through the
/// Relay (relay address and pin, office ID and pin), the practice name, and
/// the email to sign in as. "Test connection" pings the office through the
/// Relay with both pins checked. QR pairing (#37) will fill these in.
struct ConnectionEditorView: View {
    @Environment(AppState.self) private var appState
    @Environment(\.dismiss) private var dismiss

    let target: EditTarget
    private var original: SavedConnection? { target.original }

    @State private var relay = ""
    @State private var relayPin = ""
    @State private var officeID = ""
    @State private var officePin = ""
    @State private var confirmDelete = false
    @State private var tenant = ""
    @State private var email = ""
    @State private var label = ""
    @State private var status: Status = .idle

    enum Status: Equatable { case idle, checking, ok, failed(String) }

    private var office: OfficeEndpoint {
        OfficeEndpoint(relay: relay.trimmingCharacters(in: .whitespaces),
                       relayPin: relayPin.trimmingCharacters(in: .whitespaces),
                       officeID: officeID.trimmingCharacters(in: .whitespaces).lowercased(),
                       officePin: officePin.trimmingCharacters(in: .whitespaces))
    }
    private var canSave: Bool {
        office.problem == nil && !tenant.trimmingCharacters(in: .whitespaces).isEmpty
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("e.g. Barnacle, Main office", text: $label)
                        .autocorrectionDisabled()
                } header: {
                    Text("Label (optional)")
                } footer: {
                    Text("Only shown on this phone.")
                }
                Section {
                    Button("Paste pairing details", systemImage: "doc.on.clipboard", action: pastePairing)
                    field("Relay address (host:port)", $relay)
                    field("Relay pin", $relayPin)
                    field("Office ID", $officeID)
                    field("Office pin", $officePin)
                    Button("Test connection") { Task { await test() } }
                        .disabled(office.problem != nil || status == .checking)
                    statusRow
                } header: {
                    Text("Office")
                } footer: {
                    if let problem = office.problem, !relay.isEmpty || !officeID.isEmpty {
                        Text(problem.localizedDescription)
                    } else {
                        Text("The app reaches your practice only through its relay, and only if both pins match.")
                    }
                }
                Section {
                    TextField("Practice name", text: $tenant)
                        .textContentType(.organizationName)
                        .autocorrectionDisabled()
                } header: {
                    Text("Practice")
                } footer: {
                    Text("Must match your practice's name on the server exactly. Use Label for a nickname.")
                }
                Section {
                    TextField("Email (optional)", text: $email)
                        .keyboardType(.emailAddress)
                        .textContentType(.username)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                } header: {
                    Text("Account")
                } footer: {
                    Text("Your password is never saved.")
                }
                if let original {
                    Section {
                        Button("Delete practice", role: .destructive) { confirmDelete = true }
                    }
                    .confirmationDialog("Delete this practice?", isPresented: $confirmDelete) {
                        Button("Delete \(original.tenant)", role: .destructive) {
                            appState.forgetConnection(original)
                            dismiss()
                        }
                    } message: {
                        Text("\(original.subtitle) will be removed from this phone.")
                    }
                }
            }
            .navigationTitle(original == nil ? "New practice" : "Edit practice")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) { Button("Save", action: save).disabled(!canSave) }
            }
            .onAppear(perform: load)
        }
    }

    @ViewBuilder private var statusRow: some View {
        switch status {
        case .idle: EmptyView()
        case .checking: HStack { ProgressView(); Text("Checking…") }
        case .ok:
            Label("Office reachable through the relay", systemImage: "checkmark.circle.fill").foregroundStyle(.green)
        case .failed(let msg):
            Label(msg, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.red)
        }
    }

    private func load() {
        if let original {
            fill(original.office)
            tenant = original.tenant
            email = original.email
            label = original.label ?? ""
        }
    }

    private func field(_ title: String, _ text: Binding<String>) -> some View {
        TextField(title, text: text)
            .font(.callout.monospaced())
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled()
            .onChange(of: text.wrappedValue) { status = .idle }
    }

    private func fill(_ o: OfficeEndpoint) {
        relay = o.relay
        relayPin = o.relayPin
        officeID = o.officeID
        officePin = o.officePin
    }

    /// Accepts the "pairing" JSON that `wehelpd init` prints.
    private func pastePairing() {
        guard let text = UIPasteboard.general.string, let o = OfficeEndpoint.fromPairingJSON(text) else {
            status = .failed("The clipboard doesn't hold pairing details.")
            return
        }
        fill(o)
    }

    private func test() async {
        guard office.problem == nil else { return }
        status = .checking
        do {
            _ = try await APIClient(office: office, session: appState.session).ping()
            status = .ok
        } catch {
            status = .failed(error.localizedDescription)
        }
    }

    private func save() {
        guard office.problem == nil else { return }
        var conn = SavedConnection(
            office: office,
            tenant: tenant.trimmingCharacters(in: .whitespaces),
            email: email.trimmingCharacters(in: .whitespaces),
            label: label.trimmingCharacters(in: .whitespaces).isEmpty ? nil : label.trimmingCharacters(in: .whitespaces))
        // Keep the demo password only if the demo connection is unchanged.
        if let original, original.id == conn.id { conn.devPassword = original.devPassword }
        appState.saveConnection(conn, replacing: original?.id)
        dismiss()
    }
}
