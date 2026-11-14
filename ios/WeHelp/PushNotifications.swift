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
        [.banner, .sound, .list]
    }
}

extension Notification.Name {
    /// The hex APNs token, from the app delegate to AppState.
    static let weHelpPushToken = Notification.Name("WeHelpPushToken")
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
