import SwiftUI

struct ContentView: View {
    @Environment(AppState.self) private var appState

    var body: some View {
        TabView {
            Group {
                // One app, two views (#69): provider and admin devices open on
                // the day's schedule; patients on their own Today.
                if appState.isStaff { ProviderTodayView() } else { TodayView() }
            }
                .tabItem { Label("Today", systemImage: appState.isStaff ? "calendar.day.timeline.left" : "heart.text.square") }
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

struct SettingsView: View {
    @Environment(AppState.self) private var appState
    @State private var signingOut = false

    var body: some View {
        NavigationStack {
            Form {
                Section("Account") {
                    if let name = appState.me?.displayName, !name.isEmpty {
                        LabeledContent("Name", value: name)
                    }
                    LabeledContent("Role", value: appState.me?.role ?? "unknown")
                    LabeledContent("Office", value: appState.api.office.map { String($0.officeID.prefix(8)) + "…" } ?? "none")
                    LabeledContent("Relay", value: appState.api.office?.relay ?? "none")
                    NavigationLink("Devices") { DevicesView() }
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

#Preview {
    ContentView()
        .environment(AppState())
}
