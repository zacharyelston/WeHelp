import SwiftUI

/// The Forms entry on Today (#198). Until the Back Office's assignment and
/// response routes exist (#196, #197), it lists only a bundled sample form,
/// and only in demo builds. The real list will load from /me/forms through
/// APIClient, refresh on push (`refreshOnPush`) and on pull to refresh.
///
/// The walker opens as a full-screen cover that owns its FormSession, so
/// answers live only while it's open. Locking the app or switching practice
/// account closes it and drops them; saved pages come back from the Back
/// Office on resume.
struct FormsSection: View {
    @Environment(AppState.self) private var appState
    @Environment(AppLock.self) private var lock
    @State private var session: FormSession?

    var body: some View {
        Group {
            if appState.me?.demo == true, let def = SampleForms.intake {
                Section {
                    Button {
                        session = FormSession(definition: def, sink: InMemoryFormSink(definition: def))
                    } label: {
                        Label {
                            VStack(alignment: .leading) {
                                Text(def.title).foregroundStyle(.primary)
                                Text("Sample, demo builds only").font(.caption).foregroundStyle(.secondary)
                            }
                        } icon: { Image(systemName: "list.clipboard") }
                        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                } header: {
                    Text("Forms")
                } footer: {
                    Text("Forms your practice asks you to fill in will appear here.")
                }
            }
        }
        .fullScreenCover(item: $session) { s in
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
        .onChange(of: appState.active?.id) { _, _ in session = nil }
    }
}

enum SampleForms {
    /// The bundled sample definition, compiled once.
    static let intake: FormDefinition? = {
        guard let url = Bundle.main.url(forResource: "SampleIntake", withExtension: "json"),
              let data = try? Data(contentsOf: url) else { return nil }
        return try? FormDefinition(json: data)
    }()
}
