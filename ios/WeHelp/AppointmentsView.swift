import SwiftUI

/// Schedule tab state. In memory only; nothing is written to disk.
@MainActor
@Observable
final class AppointmentsModel {
    private(set) var appointments: [Appointment] = []
    /// Appointment requests (#68): a patient's own, or those waiting for a
    /// provider.
    private(set) var requests: [AppointmentRequest] = []
    private(set) var isLoading = false
    private(set) var hasLoaded = false
    private(set) var error: String?

    /// Not yet over, soonest first.
    func upcoming(now: Date = .now) -> [Appointment] {
        appointments.filter { $0.endsAt > now }.sorted { $0.startsAt < $1.startsAt }
    }

    /// Already over, most recent first.
    func past(now: Date = .now) -> [Appointment] {
        appointments.filter { $0.endsAt <= now }.sorted { $0.startsAt > $1.startsAt }
    }

    /// Requests worth showing: waiting ones, and those decided in the last
    /// two weeks (confirmed ones also appear as appointments).
    func recentRequests(now: Date = .now) -> [AppointmentRequest] {
        let cutoff = now.addingTimeInterval(-14 * 24 * 3600)
        return requests.filter { $0.status == .pending || ($0.decidedAt ?? $0.createdAt) > cutoff }
    }

    func cancel(_ r: AppointmentRequest, api: APIClient) async {
        do {
            let updated = try await api.cancelAppointmentRequest(r.id)
            if let i = requests.firstIndex(where: { $0.id == r.id }) { requests[i] = updated }
        } catch {
            self.error = error.localizedDescription
        }
    }

    func refresh(api: APIClient) async {
        guard !isLoading else { return }
        isLoading = true
        defer { isLoading = false }
        do {
            appointments = try await api.appointments()
            requests = (try? await api.appointmentRequests()) ?? requests
            error = nil
        } catch is CancellationError {
            return
        } catch let err as URLError where err.code == .cancelled {
            return
        } catch {
            self.error = error.localizedDescription
        }
        hasLoaded = true
    }
}

struct AppointmentsView: View {
    @Environment(AppState.self) private var appState
    @State private var model = AppointmentsModel()
    @State private var requesting = false

    /// Who the visit is with, from this user's point of view.
    private func counterpart(_ a: Appointment) -> String? {
        appState.isStaff ? appState.name(of: a.patientID) : appState.name(of: a.providerID)
    }

    var body: some View {
        NavigationStack {
            content
                .navigationTitle("Schedule")
                .toolbar {
                    if !appState.isStaff {
                        Button("Request", systemImage: "calendar.badge.plus") { requesting = true }
                    }
                }
                .sheet(isPresented: $requesting) {
                    AppointmentRequestView { Task { await model.refresh(api: appState.api) } }
                }
                .refreshable { await model.refresh(api: appState.api) }
                .task {
                    await appState.refreshPeople()
                    await model.refresh(api: appState.api)
                }
        }
    }

    @ViewBuilder
    private var content: some View {
        if !model.hasLoaded && model.appointments.isEmpty {
            ProgressView("Loading appointments…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if let error = model.error, model.appointments.isEmpty {
            ContentUnavailableView {
                Label("Couldn't load appointments", systemImage: "wifi.exclamationmark")
            } description: {
                Text(error)
            } actions: {
                Button("Try again") { Task { await model.refresh(api: appState.api) } }
                    .buttonStyle(.borderedProminent)
            }
        } else {
            let upcoming = model.upcoming()
            let past = model.past()
            List {
                if let error = model.error {
                    Label(error, systemImage: "exclamationmark.triangle")
                        .font(.footnote)
                        .foregroundStyle(.orange)
                }
                Section {
                    if upcoming.isEmpty {
                        Text("No upcoming appointments.")
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                    }
                    ForEach(upcoming) { AppointmentRow(appointment: $0, with: counterpart($0)) }
                } header: {
                    Text("Upcoming")
                } footer: {
                    if !appState.isStaff {
                        Text("Tap Request to ask for a new appointment.")
                    }
                }
                requestsSection
                if !past.isEmpty {
                    Section("Past") {
                        ForEach(past) { AppointmentRow(appointment: $0, with: counterpart($0)) }
                    }
                }
            }
        }
    }
}

extension AppointmentsView {
    /// Patients see their requests and can cancel waiting ones. Providers
    /// see what's waiting for them; they confirm or decline in the Back
    /// Office.
    @ViewBuilder
    var requestsSection: some View {
        let shown = appState.isStaff ? model.requests.filter { $0.status == .pending } : model.recentRequests()
        if !shown.isEmpty {
            Section {
                ForEach(shown) { r in
                    AppointmentRequestRow(request: r, with: appState.isStaff ? appState.name(of: r.patientID) : appState.name(of: r.providerID))
                        .swipeActions {
                            if !appState.isStaff && r.status == .pending {
                                Button("Cancel request", role: .destructive) {
                                    Task { await model.cancel(r, api: appState.api) }
                                }
                            }
                        }
                }
            } header: {
                Text(appState.isStaff ? "Waiting requests" : "Requests")
            } footer: {
                if appState.isStaff {
                    Text("Confirm or decline in the Back Office.")
                }
            }
        }
    }
}

struct AppointmentRow: View {
    let appointment: Appointment
    var with: String? = nil

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            DateTile(date: appointment.startsAt)
            VStack(alignment: .leading, spacing: 4) {
                HStack(alignment: .firstTextBaseline) {
                    Text(dayLabel)
                        .font(.headline)
                    Spacer()
                    StatusBadge(status: appointment.status)
                }
                Text(timeRange)
                    .font(.subheadline)
                    .strikethrough(appointment.status == .cancelled)
                Text(with.map { "Visit with \($0)" } ?? "Visit with your care team")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 2)
        .privacySensitive()
        .accessibilityElement(children: .combine)
    }

    private var dayLabel: String {
        let cal = Calendar.current
        if cal.isDateInToday(appointment.startsAt) { return "Today" }
        if cal.isDateInTomorrow(appointment.startsAt) { return "Tomorrow" }
        if cal.isDateInYesterday(appointment.startsAt) { return "Yesterday" }
        return appointment.startsAt.formatted(.dateTime.weekday(.wide))
    }

    private var timeRange: String {
        let start = appointment.startsAt.formatted(date: .omitted, time: .shortened)
        let end = appointment.endsAt.formatted(date: .omitted, time: .shortened)
        let minutes = Int(appointment.endsAt.timeIntervalSince(appointment.startsAt) / 60)
        return "\(start) – \(end) · \(minutes) min"
    }
}

struct DateTile: View {
    let date: Date

    var body: some View {
        VStack(spacing: 0) {
            Text(date, format: .dateTime.month(.abbreviated))
                .font(.caption2.weight(.semibold))
                .textCase(.uppercase)
                .foregroundStyle(.red)
            Text(date, format: .dateTime.day())
                .font(.title2.weight(.semibold))
        }
        .frame(width: 48, height: 48)
        .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 10))
    }
}

struct StatusBadge: View {
    let status: Appointment.Status

    var body: some View {
        Text(label)
            .font(.caption2.weight(.medium))
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(tint.opacity(0.15), in: Capsule())
            .foregroundStyle(tint)
    }

    private var label: String {
        switch status {
        case .scheduled: "Scheduled"
        case .cancelled: "Cancelled"
        case .completed: "Completed"
        case .noShow: "Missed"
        case .unknown: "Unknown"
        }
    }

    private var tint: Color {
        switch status {
        case .scheduled: .blue
        case .cancelled: .red
        case .completed: .green
        case .noShow: .orange
        case .unknown: .gray
        }
    }
}
