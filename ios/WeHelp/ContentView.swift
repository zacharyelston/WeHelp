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

    var body: some View {
        NavigationStack {
            Form {
                Section("Account") {
                    LabeledContent("Role", value: appState.role?.rawValue ?? "not signed in")
                }
                Section("Server") {
                    LabeledContent("API", value: appState.api.baseURL.absoluteString)
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
