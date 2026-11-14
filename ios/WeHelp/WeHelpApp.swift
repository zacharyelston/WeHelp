import SwiftUI

@main
struct WeHelpApp: App {
    @State private var appState = AppState()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(appState)
        }
    }
}

/// Unauthenticated users land on sign-in; a stored device-bound session
/// resumes on launch.
struct RootView: View {
    @Environment(AppState.self) private var appState
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        switch appState.phase {
        case .launching:
            ProgressView().task { await appState.bootstrap() }
        case .signedOut:
            ConnectionsView()
        case .signedIn:
            ContentView()
                // Signed-in screens show PHI: cover them whenever the app is
                // not active so the app-switcher snapshot shows nothing.
                .overlay {
                    if scenePhase != .active { PrivacyCover() }
                }
        }
    }
}

struct PrivacyCover: View {
    var body: some View {
        ZStack {
            Rectangle().fill(.background)
            Label("WeHelp", systemImage: "lock.fill")
                .font(.title2.weight(.semibold))
                .foregroundStyle(.secondary)
        }
        .ignoresSafeArea()
    }
}
