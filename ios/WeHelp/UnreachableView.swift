import SwiftUI

/// Shown when the active practice can't be reached (the Relay or the office
/// is down, or there's no network). The account is kept: this is never the
/// pair screen. You can switch to another practice (#89) or pair one, even
/// with only one practice (for example after the practice moved its Relay).
struct UnreachableView: View {
    @Environment(AppState.self) private var appState
    let message: String

    private var practice: String { appState.lastTenant.isEmpty ? "your practice" : appState.lastTenant }

    var body: some View {
        NavigationStack {
            ContentUnavailableView {
                Label("Can't reach \(practice)", systemImage: "wifi.exclamationmark")
            } description: {
                Text("This device is still paired. Check your connection and try again.\n\n\(message)")
            } actions: {
                Button("Try again") { appState.retry() }
                    .buttonStyle(.borderedProminent)
                // Always offered, even with one practice: a phone whose
                // practice moved its Relay can only get going again by
                // pairing anew, and without this it was stuck here.
                NavigationLink(appState.accounts.count > 1 ? "Switch or pair a practice" : "Pair again or another practice") {
                    AccountsView()
                }
            }
        }
    }
}
