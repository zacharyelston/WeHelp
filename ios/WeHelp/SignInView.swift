import SwiftUI

/// Password sign-in. On success the app enrolls this phone's device key, so
/// later launches resume without the password, and the connection (server,
/// practice, email) is saved for next time.
struct SignInView: View {
    @Environment(AppState.self) private var appState
    @State private var tenant = ""
    @State private var email = ""
    @State private var password = ""
    @State private var selectedID: SavedConnection.ID?
    @State private var working = false
    @State private var error: String?

    private var canSubmit: Bool {
        !tenant.trimmingCharacters(in: .whitespaces).isEmpty && email.contains("@") && !password.isEmpty && !working
    }

    var body: some View {
        NavigationStack {
            Form {
                if !appState.savedConnections.isEmpty {
                    Section("Saved connections") {
                        ForEach(appState.savedConnections) { conn in
                            Button { select(conn) } label: { row(conn) }
                                .swipeActions {
                                    if conn.devPassword == nil {
                                        Button("Forget", role: .destructive) { appState.forgetConnection(conn) }
                                    }
                                }
                        }
                    }
                }
                Section("Practice") {
                    TextField("Practice name", text: $tenant)
                        .textContentType(.organizationName)
                        .autocorrectionDisabled()
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
                ServerSection()
            }
            .navigationTitle("WeHelp")
            .onAppear {
                if let first = appState.savedConnections.first {
                    select(first)
                } else if tenant.isEmpty {
                    tenant = appState.lastTenant
                }
            }
        }
    }

    private func row(_ conn: SavedConnection) -> some View {
        HStack {
            VStack(alignment: .leading) {
                Text(conn.title).foregroundStyle(.primary)
                Text(conn.subtitle).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            if conn.devPassword != nil {
                Text("Demo").font(.caption2).padding(.horizontal, 6).padding(.vertical, 2)
                    .background(.orange.opacity(0.2), in: Capsule())
            }
            if selectedID == conn.id {
                Image(systemName: "checkmark").foregroundStyle(.tint)
            }
        }
    }

    private func select(_ conn: SavedConnection) {
        selectedID = conn.id
        appState.use(conn)
        tenant = conn.tenant
        email = conn.email
        password = conn.devPassword ?? ""
        error = nil
    }

    private func submit() {
        guard canSubmit else { return }
        working = true
        error = nil
        Task {
            defer { working = false }
            do {
                try await appState.signIn(
                    tenant: tenant.trimmingCharacters(in: .whitespaces),
                    email: email.trimmingCharacters(in: .whitespaces),
                    password: password)
                password = ""
            } catch APIError.unauthorized {
                error = "Practice, email or password is incorrect."
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}
