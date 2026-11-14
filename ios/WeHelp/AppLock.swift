import LocalAuthentication
import SwiftUI

/// Face ID, Touch ID or the device passcode unlocks the app (#37). Patients
/// have no password; the device key plus this local check stand in for one.
/// The app locks whenever it goes to the background.
@MainActor
@Observable
final class AppLock {
    private(set) var locked = true
    private(set) var message: String?
    private var unlocking = false

    func lock() { locked = true }

    func unlock() async {
        guard locked, !unlocking else { return }
        unlocking = true
        defer { unlocking = false }
        let context = LAContext()
        var error: NSError?
        guard context.canEvaluatePolicy(.deviceOwnerAuthentication, error: &error) else {
            #if DEBUG
            // Simulators and development phones often have no passcode.
            locked = false
            #else
            message = "Set a passcode for this phone in Settings to use WeHelp. It protects your health information if the phone is lost."
            #endif
            return
        }
        do {
            try await context.evaluatePolicy(.deviceOwnerAuthentication,
                                             localizedReason: "Unlock your WeHelp health information")
            locked = false
            message = nil
        } catch {
            message = "Unlock to see your health information."
        }
    }
}

struct LockView: View {
    let lock: AppLock

    var body: some View {
        ZStack {
            Rectangle().fill(.background).ignoresSafeArea()
            VStack(spacing: 16) {
                Image(systemName: "lock.fill").font(.system(size: 48)).foregroundStyle(.secondary)
                Text("WeHelp is locked").font(.title2.weight(.semibold))
                if let message = lock.message {
                    Text(message).multilineTextAlignment(.center).foregroundStyle(.secondary).padding(.horizontal)
                }
                Button("Unlock") { Task { await lock.unlock() } }
                    .buttonStyle(.borderedProminent)
            }
        }
    }
}
