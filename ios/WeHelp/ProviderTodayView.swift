import SwiftUI

/// The provider's day (#69): today's visits with patient names, then what's
/// next, and unread messages. Everything is fetched through the Relay and
/// held in memory only.
struct ProviderTodayView: View {
    @Environment(AppState.self) private var appState
    @State private var appointments: [Appointment] = []
    @State private var unread = 0
    @State private var loaded = false
    @State private var error: String?

    private var today: [Appointment] {
        appointments.filter { Calendar.current.isDateInToday($0.startsAt) && $0.status != .cancelled }
            .sorted { $0.startsAt < $1.startsAt }
    }

    private var next: [Appointment] {
        let endOfToday = Calendar.current.startOfDay(for: .now).addingTimeInterval(86_400)
        return appointments.filter { $0.startsAt >= endOfToday && $0.status == .scheduled }
            .sorted { $0.startsAt < $1.startsAt }
            .prefix(5).map { $0 }
    }

    var body: some View {
        NavigationStack {
            List {
                if let error {
                    Section { Label(error, systemImage: "wifi.exclamationmark").foregroundStyle(.red) }
                }
                if unread > 0 {
                    Section {
                        Label("\(unread) unread message\(unread == 1 ? "" : "s")", systemImage: "envelope.badge")
                    }
                }
                Section("Today") {
                    if today.isEmpty {
                        Text(loaded ? "No visits today." : "Loading…").foregroundStyle(.secondary)
                    }
                    ForEach(today) { a in
                        AppointmentRow(appointment: a, with: appState.name(of: a.patientID))
                    }
                }
                if !next.isEmpty {
                    Section("Coming up") {
                        ForEach(next) { a in
                            AppointmentRow(appointment: a, with: appState.name(of: a.patientID))
                        }
                    }
                }
            }
            .navigationTitle(greeting)
            .refreshable { await load() }
            .task { await load() }
        }
    }

    private var greeting: String {
        guard let name = appState.me?.displayName, !name.isEmpty else { return "Today" }
        return name
    }

    private func load() async {
        do {
            async let appts = appState.api.appointments()
            async let page = appState.api.messages()
            await appState.refreshPeople()
            let (a, p) = try await (appts, page)
            appointments = a
            unread = p.messages.filter(\.isUnread).count
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
        loaded = true
    }
}
