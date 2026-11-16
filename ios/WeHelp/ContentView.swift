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
            if !appState.isStaff {
                // Supply store and library (#97); patients order here.
                StoreView()
                    .tabItem { Label("Store", systemImage: "bag") }
            }
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
                FormsSection()
                Section("From your practice") {
                    NavigationLink { LibraryView() } label: { Label("Library", systemImage: "books.vertical") }
                }
            }
            .navigationTitle("Today")
        }
    }
}

struct SettingsView: View {
    @Environment(AppState.self) private var appState
    @Environment(AppLock.self) private var lock

    var body: some View {
        NavigationStack {
            Form {
                // The practices this device is paired with (#89).
                Section("Practice") {
                    if let a = appState.active { AccountRow(account: a, active: true) }
                    NavigationLink {
                        AccountsView()
                    } label: {
                        Label(appState.accounts.count > 1 ? "Switch practice (\(appState.accounts.count) accounts)" : "Accounts",
                              systemImage: "person.2")
                    }
                }
                Section("This device") {
                    LabeledContent("Office", value: appState.api.office.map { String($0.officeID.prefix(8)) + "…" } ?? "none")
                    LabeledContent("Relay", value: appState.api.office?.relay ?? "none")
                    NavigationLink("Devices") { DevicesView() }
                }
                Section {
                    Button("Lock now", systemImage: "lock") { lock.lock() }
                } footer: {
                    Text(appState.me?.demo == true
                         ? "Demo practice: WeHelp doesn't lock, so testers can use borrowed devices. A real practice always asks for Face ID or your passcode."
                         : "WeHelp locks whenever you leave it; Face ID or your passcode opens it again. This device stays paired with your practice. Only the practice can remove it.")
                }
                Section {
                    NavigationLink("Third-party licences") { LicencesView() }
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
