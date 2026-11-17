import SwiftUI

/// The Forms entry on Today (#198). It lists the patient's open and upcoming
/// forms from /me/forms when the practice has the `forms` switch on, and
/// refreshes on push, on return to the app and on pull to refresh. Demo
/// builds also offer a bundled sample form.
///
/// The walker opens as a full-screen cover that owns its FormSession, so
/// answers live only while it's open. Locking the app or switching practice
/// account closes it and drops them; saved pages come back from the Back
/// Office on resume.
struct FormsSection: View {
    @Environment(AppState.self) private var appState
    @Environment(AppLock.self) private var lock
    @State private var session: FormSession?
    @State private var forms: [AssignedForm] = []
    @State private var enabled = false
    @State private var opening: String?
    @State private var message: String?

    private var api: FormsAPI { FormsAPI(api: appState.api) }
    private var demoSample: FormDefinition? { appState.me?.demo == true ? SampleForms.intake : nil }

    var body: some View {
        Group {
            if enabled || demoSample != nil {
                Section {
                    ForEach(forms) { f in row(f) }
                    if enabled && forms.isEmpty {
                        Text("No forms to fill in right now.").foregroundStyle(.secondary)
                    }
                    if let def = demoSample {
                        Button {
                            session = FormSession(definition: def, sink: InMemoryFormSink(definition: def))
                        } label: {
                            label(def.title, detail: "Sample, demo builds only", icon: "list.clipboard")
                        }
                        .buttonStyle(.plain)
                    }
                    if let message { Text(message).font(.footnote).foregroundStyle(.red) }
                } header: {
                    Text("Forms")
                } footer: {
                    Text("Forms your practice asks you to fill in appear here.")
                }
            }
        }
        .task(id: appState.active?.id) { await load() }
        .refreshOnPush { await load() }
        .onReceive(NotificationCenter.default.publisher(for: .weHelpRefreshToday)) { _ in Task { await load() } }
        .fullScreenCover(item: $session, onDismiss: { Task { await load() } }) { s in
            NavigationStack {
                FormWalkerView(session: s)
                    .toolbar {
                        ToolbarItem(placement: .cancellationAction) {
                            Button("Close") { session = nil }
                        }
                    }
            }
        }
        // PHI only while shown: a lock or another account closes the walker.
        .onChange(of: lock.locked) { _, locked in if locked { session = nil } }
        .onChange(of: appState.active?.id) { _, _ in session = nil; forms = [] }
    }

    @ViewBuilder private func row(_ f: AssignedForm) -> some View {
        if f.isOpen {
            Button { Task { await open(f) } } label: {
                HStack {
                    label(f.title, detail: f.dueAt.map { "Due " + $0.formatted(date: .abbreviated, time: .shortened) }
                          ?? (f.responseID != nil ? "In progress" : "Open"),
                          icon: f.responseID != nil ? "pencil.and.list.clipboard" : "list.clipboard")
                    if opening == f.occurrenceID { ProgressView() }
                }
            }
            .buttonStyle(.plain)
            .disabled(opening != nil)
        } else {
            label(f.title, detail: f.opensAt.map { "Opens " + $0.formatted(date: .abbreviated, time: .shortened) } ?? "Upcoming",
                  icon: "clock")
                .foregroundStyle(.secondary)
                .accessibilityLabel("\(f.title), not open yet")
        }
    }

    private func label(_ title: String, detail: String, icon: String) -> some View {
        Label {
            VStack(alignment: .leading) {
                Text(title)
                Text(detail).font(.caption).foregroundStyle(.secondary)
            }
        } icon: { Image(systemName: icon) }
        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
        .contentShape(Rectangle())
    }

    private func load() async {
        guard appState.active != nil, !appState.isStaff else { return }
        do {
            enabled = try await api.formsEnabled()
            forms = enabled ? try await api.myForms() : []
            message = nil
        } catch FormsAPIError.featureOff {
            enabled = false
            forms = []
        } catch {
            // Keep what's shown; Today has other content.
            if enabled { message = "Couldn't refresh your forms." }
        }
    }

    private func open(_ f: AssignedForm) async {
        opening = f.occurrenceID
        defer { opening = nil }
        do {
            let def = try await api.definition(versionID: f.versionID)
            let state = try await api.start(occurrenceID: f.occurrenceID)
            let sink = APIFormSink(forms: api, occurrenceID: f.occurrenceID, state: state)
            session = FormSession(definition: def, sink: sink, answers: state.answers, savedPages: state.savedPages,
                                  assets: APIFormAssets(forms: api, versionID: f.versionID))
            message = nil
        } catch let e as FormsAPIError {
            message = e.errorDescription
            await load()
        } catch {
            message = "Couldn't open this form. Check your connection and try again."
        }
    }
}

extension Notification.Name {
    /// Pull to refresh on Today.
    static let weHelpRefreshToday = Notification.Name("WeHelpRefreshToday")
}

enum SampleForms {
    /// The bundled sample definition, compiled once.
    static let intake: FormDefinition? = {
        guard let url = Bundle.main.url(forResource: "SampleIntake", withExtension: "json"),
              let data = try? Data(contentsOf: url) else { return nil }
        return try? FormDefinition(json: data)
    }()
}
