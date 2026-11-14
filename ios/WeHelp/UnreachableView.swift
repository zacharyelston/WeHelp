import SwiftUI

/// Shown at launch when this device is paired but its practice can't be
/// reached (the Relay or the office is down, or there's no network). The
/// pairing is kept: this is never the pair screen.
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
            }
        }
    }
}
