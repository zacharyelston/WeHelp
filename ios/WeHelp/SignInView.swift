import SwiftUI

/// Sign in to one saved server. On success the app enrolls this phone's
/// device key, so later launches resume without the password.
struct SignInView: View {
    @Environment(AppState.self) private var appState
    @Environment(\.dismiss) private var dismiss
    let connection: SavedConnection
    @State private var editing: EditTarget?

    @State private var email = ""
    @State private var password = ""
    @State private var working = false
    @State private var error: String?

    private var canSubmit: Bool { email.contains("@") && !password.isEmpty && !working }

    var body: some View {
        Form {
            Section {
                LabeledContent("Practice", value: connection.tenant)
                LabeledContent("Server", value: connection.serverURL.host() ?? connection.serverURL.absoluteString)
            }
            Section("Account") {
                TextField("Email", text: $email)
                    .keyboardType(.emailAddress)
                    .textContentType(.username)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                SecureField("Password", text: $password)
                    .textContentType(.password)
                    .onSubmit(submit)
            }
            Section {
                Button(action: submit) {
                    HStack {
                        Text("Sign in")
                        if working { Spacer(); ProgressView() }
                    }
                }
                .disabled(!canSubmit)
                if let error {
                    Label(error, systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(.red)
                }
            } footer: {
                Text("Signing in registers this phone with your practice.")
            }
        }
        .navigationTitle(connection.title)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button("Edit") { editing = .existing(connection) }
            }
        }
        .sheet(item: $editing) { ConnectionEditorView(target: $0) }
        // Edited or deleted: this screen shows a stale server, so go back.
        .onChange(of: appState.savedConnections) { _, list in
            if !list.contains(where: { $0.id == connection.id }) { dismiss() }
        }
        .onAppear {
            appState.use(connection)
            if email.isEmpty { email = connection.email }
            if password.isEmpty { password = connection.devPassword ?? "" }
        }
    }

    private func submit() {
        guard canSubmit else { return }
        working = true
        error = nil
        Task {
            defer { working = false }
            do {
                appState.use(connection)
                try await appState.signIn(
                    tenant: connection.tenant,
                    email: email.trimmingCharacters(in: .whitespaces),
                    password: password)
                password = ""
            } catch APIError.unauthorized {
                error = "Email or password is incorrect."
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}
