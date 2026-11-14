import SwiftUI
import UserNotifications

// Spike S3 (#34): register for remote notifications and show the device token
// and received pushes. Throwaway code; see docs/spikes/S3.md.

@MainActor
@Observable
final class PushState {
    static let shared = PushState()
    var token = "requesting…"
    var received: [String] = []
}

final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    func application(_ application: UIApplication,
                     didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        Task { @MainActor in
            let granted = (try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge])) ?? false
            if !granted { PushState.shared.token = "notifications not allowed" }
            application.registerForRemoteNotifications()
        }
        return true
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        let hex = deviceToken.map { String(format: "%02x", $0) }.joined()
        print("S3 device token: \(hex)")
        Task { @MainActor in PushState.shared.token = hex }
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
        Task { @MainActor in PushState.shared.token = "register failed: \(error.localizedDescription)" }
    }

    // Show pushes while the app is in the foreground too, and record them.
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async
        -> UNNotificationPresentationOptions {
        // Copy plain strings out before hopping to the main actor (Swift 6).
        let c = notification.request.content
        let line = "\(c.title): \(c.body) [category \(c.categoryIdentifier)]"
        await MainActor.run { PushState.shared.received.append(line) }
        return [.banner, .sound, .list]
    }
}

@main
struct S3SpikeApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) var delegate
    var body: some Scene { WindowGroup { SpikeView() } }
}

struct SpikeView: View {
    private var state = PushState.shared
    var body: some View {
        NavigationStack {
            Form {
                Section("Device token") {
                    Text(state.token).font(.caption.monospaced()).textSelection(.enabled)
                }
                Section("Received") {
                    if state.received.isEmpty { Text("none yet").foregroundStyle(.secondary) }
                    ForEach(Array(state.received.enumerated()), id: \.offset) { _, line in
                        Text(line).font(.caption)
                    }
                }
            }
            .navigationTitle("Spike S3")
        }
    }
}
