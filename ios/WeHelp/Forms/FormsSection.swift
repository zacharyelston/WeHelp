import SwiftUI

/// The Forms entry on Today (#198). Until the Back Office's assignment and
/// response routes exist (#196, #197), it lists only a bundled sample form,
/// and only in demo builds. The real list will load from /me/forms through
/// APIClient, refresh on push (`refreshOnPush`) and on pull to refresh.
struct FormsSection: View {
    @Environment(AppState.self) private var appState

    var body: some View {
        if appState.me?.demo == true, let def = SampleForms.intake {
            Section {
                NavigationLink {
                    FormWalkerView(session: FormSession(definition: def, sink: InMemoryFormSink(definition: def)))
                } label: {
                    Label {
                        VStack(alignment: .leading) {
                            Text(def.title)
                            Text("Sample, demo builds only").font(.caption).foregroundStyle(.secondary)
                        }
                    } icon: { Image(systemName: "list.clipboard") }
                }
                .frame(minHeight: 44)
            } header: {
                Text("Forms")
            } footer: {
                Text("Forms your practice asks you to fill in will appear here.")
            }
        }
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
