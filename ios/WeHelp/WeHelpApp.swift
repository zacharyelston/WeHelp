import SwiftUI

@main
struct WeHelpApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var appState = AppState()

    var body: some Scene {
        WindowGroup {
            #if DEBUG
            // Debug builds only: `-sampleForm` opens the bundled sample form,
            // so the walker can be checked without pairing (#198).
            if ProcessInfo.processInfo.arguments.contains("-sampleForm"), let def = SampleForms.intake {
                NavigationStack {
                    FormWalkerView(session: FormSession(definition: def, sink: InMemoryFormSink(definition: def)))
                }
            } else {
                RootView().environment(appState)
            }
            #else
            RootView()
                .environment(appState)
            #endif
        }
    }
}

/// Unpaired phones land on the welcome screen to pair by QR; a stored
/// device-bound session resumes on launch behind the app lock.
struct RootView: View {
    @Environment(AppState.self) private var appState
    @Environment(\.scenePhase) private var scenePhase
    @State private var lock = AppLock()

    /// A demo office (its /me says demo) skips the lock, so testers can use
    /// borrowed phones. Switching to a real practice shows the lock again.
    private var lockRequired: Bool { appState.me?.demo != true }

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
                    if lock.locked && lockRequired { LockView(lock: lock) } else if scenePhase != .active { PrivacyCover() }
                }
                .task {
                    if lockRequired { await lock.unlock() }
                    await Push.enable() // permission once, then the token goes to every practice (#7)
                }
                .onChange(of: scenePhase) { _, phase in
                    if phase == .background { lock.lock() }
                    if phase == .active && lockRequired { Task { await lock.unlock() } }
                }
                .onChange(of: lockRequired) { _, required in
                    if required { Task { await lock.unlock() } }
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
