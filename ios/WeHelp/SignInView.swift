import SwiftUI

/// Password sign-in. On success the app enrolls this phone's device key, so
/// later launches resume without the password.
struct SignInView: View {
    @Environment(AppState.self) private var appState
    @State private var tenant = ""
    @State private var email = ""
    @State private var password = ""
    @State private var working = false
    @State private var error: String?

    private var canSubmit: Bool {
        !tenant.trimmingCharacters(in: .whitespaces).isEmpty && email.contains("@") && !password.isEmpty && !working
    }

    var body: some View {
        NavigationStack {
            Form {
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
            .onAppear { if tenant.isEmpty { tenant = appState.lastTenant } }
        }
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
