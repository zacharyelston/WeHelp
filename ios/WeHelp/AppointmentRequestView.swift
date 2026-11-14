import SwiftUI

/// Ask the practice for an appointment (#68): which provider, up to three
/// preferred days with a time of day, and an optional reason. The provider
/// confirms with an exact time or declines; the patient hears back in their
/// thread and by push.
struct AppointmentRequestView: View {
    @Environment(AppState.self) private var appState
    @Environment(\.dismiss) private var dismiss
    var onSent: () -> Void = {}

    struct Choice: Identifiable {
        let id = UUID()
        var date: Date
        var period: PreferredTime.Period = .any
    }

    @State private var providers: [CareLink] = []
    @State private var provider: UUID?
    @State private var choices = [Choice(date: Calendar.current.date(byAdding: .day, value: 1, to: .now) ?? .now)]
    @State private var reason = ""
    @State private var sending = false
    @State private var error: String?

    private var range: ClosedRange<Date> {
        let today = Calendar.current.startOfDay(for: .now)
        return today...(Calendar.current.date(byAdding: .day, value: 180, to: today) ?? today)
    }

    var body: some View {
        NavigationStack {
            Form {
                if providers.count > 1 {
                    Picker("With", selection: $provider) {
                        ForEach(providers, id: \.providerID) { l in
                            Text(l.providerName ?? "Your provider").tag(Optional(l.providerID))
                        }
                    }
                }
                Section {
                    ForEach($choices) { $c in
                        VStack(alignment: .leading) {
                            DatePicker("Day", selection: $c.date, in: range, displayedComponents: .date)
                            Picker("Time of day", selection: $c.period) {
                                ForEach(PreferredTime.Period.allCases) { Text($0.label).tag($0) }
                            }
                            .pickerStyle(.segmented)
                        }
                        .padding(.vertical, 4)
                    }
                    .onDelete { choices.remove(atOffsets: $0) }
                    .deleteDisabled(choices.count == 1)
                    if choices.count < 3 {
                        Button("Add another time", systemImage: "plus") {
                            let next = Calendar.current.date(byAdding: .day, value: 1, to: choices.last?.date ?? .now) ?? .now
                            choices.append(Choice(date: min(next, range.upperBound)))
                        }
                    }
                } header: {
                    Text("When would suit you?")
                } footer: {
                    Text("Up to three choices. The practice confirms an exact time.")
                }
                Section("Reason (optional)") {
                    TextField("What's the visit for?", text: $reason, axis: .vertical)
                        .lineLimit(2...5)
                }
                if let error {
                    Text(error).foregroundStyle(.red)
                }
            }
            .navigationTitle("Request appointment")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Send") { Task { await send() } }
                        .disabled(provider == nil || sending)
                }
            }
            .task { await loadProviders() }
        }
    }

    private func loadProviders() async {
        guard let links = try? await appState.api.links() else {
            error = "Couldn't reach your practice. Try again in a moment."
            return
        }
        providers = links.filter(\.isActive)
        if provider == nil { provider = providers.first?.providerID }
        if providers.isEmpty { error = "You're not linked with a provider yet." }
    }

    private func send() async {
        guard let provider else { return }
        sending = true
        defer { sending = false }
        let preferred = choices.map { PreferredTime(date: Self.day($0.date), period: $0.period) }
        do {
            try await appState.api.requestAppointment(provider: provider, preferred: preferred,
                                                      reason: reason.trimmingCharacters(in: .whitespacesAndNewlines))
            onSent()
            dismiss()
        } catch APIError.http(_, let message) {
            error = message
        } catch {
            self.error = error.localizedDescription
        }
    }

    /// YYYY-MM-DD in the phone's calendar.
    static func day(_ d: Date) -> String {
        let c = Calendar.current.dateComponents([.year, .month, .day], from: d)
        return String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
    }
}

/// One request in the Schedule tab.
struct AppointmentRequestRow: View {
    let request: AppointmentRequest
    var with: String? = nil

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline) {
                Text(with.map { "Request · \($0)" } ?? "Appointment request")
                    .font(.headline)
                Spacer()
                Text(statusLabel)
                    .font(.caption2.weight(.medium))
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(tint.opacity(0.15), in: Capsule())
                    .foregroundStyle(tint)
            }
            ForEach(request.preferred, id: \.self) { p in
                Text(p.label).font(.subheadline)
            }
            if !request.reason.isEmpty {
                Text(request.reason).font(.caption).foregroundStyle(.secondary)
            }
            if request.status == .declined, !request.note.isEmpty {
                Text(request.note).font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 2)
        .privacySensitive()
        .accessibilityElement(children: .combine)
    }

    private var statusLabel: String {
        switch request.status {
        case .pending: "Waiting"
        case .confirmed: "Confirmed"
        case .declined: "Declined"
        case .cancelled: "Cancelled"
        case .unknown: "Unknown"
        }
    }

    private var tint: Color {
        switch request.status {
        case .pending: .orange
        case .confirmed: .green
        case .declined: .red
        case .cancelled, .unknown: .gray
        }
    }
}
