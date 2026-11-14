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

/// Unpaired phones land on the welcome screen to pair by QR; a stored
/// device-bound session resumes on launch behind the app lock.
struct RootView: View {
    @Environment(AppState.self) private var appState
    @Environment(\.scenePhase) private var scenePhase
    @State private var lock = AppLock()

    var body: some View {
        switch appState.phase {
        case .launching:
            ProgressView().task { await appState.bootstrap() }
        case .signedOut:
            WelcomeView()
        case .unreachable(let message):
            UnreachableView(message: message)
        case .signedIn:
            ContentView()
                .environment(lock)
                // Signed-in screens show PHI: cover them whenever the app is
                // not active so the app-switcher snapshot shows nothing, and
                // require Face ID or the passcode to see them (#37).
                .overlay {
                    if lock.locked { LockView(lock: lock) } else if scenePhase != .active { PrivacyCover() }
                }
                .task { await lock.unlock() }
                .onChange(of: scenePhase) { _, phase in
                    if phase == .background { lock.lock() }
                    if phase == .active { Task { await lock.unlock() } }
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
