import Combine
import SwiftUI
import UserNotifications

/// Push notifications (#7). The Relay sends fixed, generic alerts ("You have
/// a new message from your practice"); the app fetches the details through
/// its pinned connection. The APNs token is registered with every practice
/// this device is paired with (#89).
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    func application(_ application: UIApplication,
                     didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        return true
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        let hex = deviceToken.map { String(format: "%02x", $0) }.joined()
        NotificationCenter.default.post(name: .weHelpPushToken, object: hex)
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {}

    // Show pushes while the app is open too (Swift 6: nonisolated; spike S3).
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async
        -> UNNotificationPresentationOptions {
        // The push carries no details: tell the open screens to fetch now,
        // so the banner and the screen agree.
        await MainActor.run { NotificationCenter.default.post(name: .weHelpPushArrived, object: nil) }
        return [.banner, .sound, .list]
    }
}

extension Notification.Name {
    /// A push arrived while the app is open: on-screen lists refresh now.
    static let weHelpPushArrived = Notification.Name("WeHelpPushArrived")
    /// The hex APNs token, from the app delegate to AppState.
    static let weHelpPushToken = Notification.Name("WeHelpPushToken")
}

/// Runs `action` when a push arrives while the app is open, and when the
/// app comes back to the foreground (for example from tapping the push).
private struct RefreshOnPush: ViewModifier {
    @Environment(\.scenePhase) private var scenePhase
    let action: () async -> Void

    func body(content: Content) -> some View {
        content
            .onReceive(NotificationCenter.default.publisher(for: .weHelpPushArrived)) { _ in
                Task { await action() }
            }
            .onChange(of: scenePhase) { old, new in
                if new == .active && old != .active { Task { await action() } }
            }
    }
}

extension View {
    func refreshOnPush(_ action: @escaping () async -> Void) -> some View {
        modifier(RefreshOnPush(action: action))
    }
}

enum Push {
    /// The APNs environment this build's entitlement uses.
    static var environment: String {
        #if DEBUG
        "sandbox"
        #else
        "production"
        #endif
    }

    /// Asks for permission once, then registers with APNs. Calling it again
    /// is cheap: iOS remembers the answer and hands back the same token.
    @MainActor
    static func enable() async {
        let granted = (try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge])) ?? false
        if granted { UIApplication.shared.registerForRemoteNotifications() }
    }
}
