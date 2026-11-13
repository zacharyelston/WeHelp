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

    var body: some View {
        switch appState.phase {
        case .launching:
            ProgressView().task { await appState.bootstrap() }
        case .signedOut:
            SignInView()
        case .signedIn:
            ContentView()
        }
    }
}
