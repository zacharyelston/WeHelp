import SwiftUI

struct ContentView: View {
    @Environment(AppState.self) private var appState

    var body: some View {
        TabView {
            TodayView()
                .tabItem { Label("Today", systemImage: "heart.text.square") }
            MessagesView()
                .tabItem { Label("Messages", systemImage: "bubble.left.and.bubble.right") }
            AppointmentsView()
                .tabItem { Label("Schedule", systemImage: "calendar") }
            SettingsView()
                .tabItem { Label("Settings", systemImage: "gear") }
        }
    }
}

struct TodayView: View {
    var body: some View {
        NavigationStack {
            List {
                Label("Daily check-in", systemImage: "checkmark.circle")
                Label("Weight reminder", systemImage: "scalemass")
                Label("Sync health monitors", systemImage: "waveform.path.ecg")
            }
            .navigationTitle("Today")
        }
    }
}

struct MessagesView: View {
    var body: some View {
        NavigationStack {
            ContentUnavailableView(
                "No messages yet",
                systemImage: "bubble.left.and.bubble.right",
                description: Text("Messages and memos from your care team appear here.")
            )
            .navigationTitle("Messages")
        }
    }
}

struct AppointmentsView: View {
    var body: some View {
        NavigationStack {
            ContentUnavailableView(
                "No upcoming appointments",
                systemImage: "calendar",
                description: Text("Scheduled visits and reminders appear here.")
            )
            .navigationTitle("Schedule")
        }
    }
}

struct SettingsView: View {
    @Environment(AppState.self) private var appState
    @State private var signingOut = false

    var body: some View {
        NavigationStack {
            Form {
                Section("Account") {
                    LabeledContent("Role", value: appState.role?.rawValue ?? "unknown")
                    LabeledContent("Server", value: appState.api.baseURL.absoluteString)
                }
                Section {
                    Button("Sign out", role: .destructive) {
                        signingOut = true
                        Task { await appState.signOut() }
                    }
                    .disabled(signingOut)
                } footer: {
                    Text("Signing out removes this phone's key. You'll need your password to sign in again.")
                }
            }
            .navigationTitle("Settings")
        }
    }
}

/// Server address field with "Save and test connection".
struct ServerSection: View {
    @Environment(AppState.self) private var appState
    @State private var serverText = ""
    @State private var status: ConnectionStatus = .idle

    enum ConnectionStatus: Equatable {
        case idle, checking, ok(String), failed(String)
    }

    var body: some View {
                Section {
                    TextField("http://192.168.1.50:8080", text: $serverText)
                        .keyboardType(.URL)
                        .textContentType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .onSubmit(save)
                    Button("Save and test connection", action: save)
                    Button("Reset to default", role: .destructive) {
                        appState.resetServerURL()
                        serverText = appState.api.baseURL.absoluteString
                        Task { await check() }
                    }
                    statusRow
                } header: {
                    Text("Server")
                } footer: {
                    Text("The address of your clinic's WeHelp server.")
                }
                .onAppear { serverText = appState.api.baseURL.absoluteString }
                .onChange(of: appState.api.baseURL) { _, url in
                    serverText = url.absoluteString
                    status = .idle
                }
    }

    @ViewBuilder private var statusRow: some View {
        switch status {
        case .idle:
            EmptyView()
        case .checking:
            HStack { ProgressView(); Text("Checking…") }
        case .ok(let message):
            Label("Connected (\(message))", systemImage: "checkmark.circle.fill")
                .foregroundStyle(.green)
        case .failed(let message):
            Label(message, systemImage: "exclamationmark.triangle.fill")
                .foregroundStyle(.red)
        }
    }

    private func save() {
        guard appState.setServerURL(serverText) else {
            status = .failed("Enter an http:// or https:// address.")
            return
        }
        serverText = appState.api.baseURL.absoluteString
        Task { await check() }
    }

    private func check() async {
        status = .checking
        do {
            let pong = try await appState.api.ping()
            status = .ok(pong.message)
        } catch {
            status = .failed(error.localizedDescription)
        }
    }
}

#Preview {
    ContentView()
        .environment(AppState())
}
