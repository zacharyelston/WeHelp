import SwiftUI

/// Add or edit a server connection: address, practice, and the email to sign
/// in as. "Test connection" pings the address before saving.
struct ConnectionEditorView: View {
    @Environment(AppState.self) private var appState
    @Environment(\.dismiss) private var dismiss

    let target: EditTarget
    private var original: SavedConnection? { target.original }

    @State private var server = ""
    @State private var confirmDelete = false
    @State private var tenant = ""
    @State private var email = ""
    @State private var label = ""
    @State private var status: Status = .idle

    enum Status: Equatable { case idle, checking, ok, failed(String) }

    private var url: URL? { AppState.validURL(server) }
    private var canSave: Bool {
        url != nil && !tenant.trimmingCharacters(in: .whitespaces).isEmpty
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
                    TextField("http://192.168.1.50:8080", text: $server)
                        .keyboardType(.URL)
                        .textContentType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .onChange(of: server) { status = .idle }
                    Button("Test connection") { Task { await test() } }
                        .disabled(url == nil || status == .checking)
                    statusRow
                } header: {
                    Text("Server address")
                } footer: {
                    Text("Ask your practice for this address.")
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
                        Button("Delete server", role: .destructive) { confirmDelete = true }
                    }
                    .confirmationDialog("Delete this server?", isPresented: $confirmDelete) {
                        Button("Delete \(original.tenant)", role: .destructive) {
                            appState.forgetConnection(original)
                            dismiss()
                        }
                    } message: {
                        Text("\(original.subtitle) will be removed from this phone.")
                    }
                }
            }
            .navigationTitle(original == nil ? "New server" : "Edit server")
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
            Label("Server reachable", systemImage: "checkmark.circle.fill").foregroundStyle(.green)
        case .failed(let msg):
            Label(msg, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.red)
        }
    }

    private func load() {
        if let original {
            server = original.serverURL.absoluteString
            tenant = original.tenant
            email = original.email
            label = original.label ?? ""
        } else if server.isEmpty {
            server = AppState.defaultServerURL().absoluteString
        }
    }

    private func test() async {
        guard let url else { return }
        status = .checking
        do {
            _ = try await APIClient(baseURL: url, session: appState.session).ping()
            status = .ok
        } catch {
            status = .failed(error.localizedDescription)
        }
    }

    private func save() {
        guard let url else { return }
        var conn = SavedConnection(
            serverURL: url,
            tenant: tenant.trimmingCharacters(in: .whitespaces),
            email: email.trimmingCharacters(in: .whitespaces),
            label: label.trimmingCharacters(in: .whitespaces).isEmpty ? nil : label.trimmingCharacters(in: .whitespaces))
        // Keep the demo password only if the demo connection is unchanged.
        if let original, original.id == conn.id { conn.devPassword = original.devPassword }
        appState.saveConnection(conn, replacing: original?.id)
        dismiss()
    }
}
