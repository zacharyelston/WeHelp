import SwiftUI
import VisionKit

/// Signed-out home (#37). Patients join by scanning the Pair phone QR code
/// the front desk shows them; there is no password. Debug builds also keep
/// the developer screens (saved practices with password sign-in, for staff
/// accounts and demos).
struct WelcomeView: View {
    @Environment(AppState.self) private var appState
    @State private var pairing = false
    #if DEBUG
    @State private var developer = false
    #endif

    var body: some View {
        NavigationStack {
            VStack(spacing: 24) {
                Spacer()
                Image(systemName: "heart.text.square.fill")
                    .font(.system(size: 72))
                    .foregroundStyle(.tint)
                Text("WeHelp")
                    .font(.largeTitle.weight(.bold))
                Text("To get started, ask the front desk at your practice to pair this phone. They'll show you a code to scan.")
                    .multilineTextAlignment(.center)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal)
                Button {
                    pairing = true
                } label: {
                    Label("Pair with your practice", systemImage: "qrcode.viewfinder")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .padding(.horizontal, 32)
                Spacer()
                #if DEBUG
                Button("Developer: saved practices") { developer = true }
                    .font(.footnote)
                #endif
            }
            .padding()
            .fullScreenCover(isPresented: $pairing) { PairingView() }
            #if DEBUG
            .sheet(isPresented: $developer) { ConnectionsView() }
            #endif
        }
    }
}

/// Scan the practice's QR code (or paste it), then pair through the Relay.
struct PairingView: View {
    @Environment(AppState.self) private var appState
    @Environment(\.dismiss) private var dismiss
    @State private var badCode = false

    private var scannerAvailable: Bool {
        DataScannerViewController.isSupported && DataScannerViewController.isAvailable
    }

    var body: some View {
        NavigationStack {
            Group {
                switch appState.pairing {
                case .connecting:
                    ProgressView("Connecting to your practice…")
                case .failed(let message):
                    ContentUnavailableView {
                        Label("Couldn't pair this phone", systemImage: "exclamationmark.triangle")
                    } description: {
                        Text(message)
                    } actions: {
                        Button("Try again") { appState.cancelPairing() }
                            .buttonStyle(.borderedProminent)
                    }
                case .idle:
                    scanner
                }
            }
            .navigationTitle("Pair this phone")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") {
                        appState.cancelPairing()
                        dismiss()
                    }
                }
            }
            .onChange(of: appState.phase) { _, phase in
                if phase == .signedIn { dismiss() }
            }
            .alert("That isn't a WeHelp pairing code", isPresented: $badCode) {
                Button("OK", role: .cancel) {}
            } message: {
                Text("Scan the code your practice's front desk shows you.")
            }
        }
    }

    @ViewBuilder private var scanner: some View {
        VStack(spacing: 16) {
            if scannerAvailable {
                QRScanner { text in handle(text) }
                    .clipShape(RoundedRectangle(cornerRadius: 16))
                    .frame(maxHeight: 420)
                    .accessibilityLabel("Camera viewfinder for the pairing code")
                Text("Point the camera at the code on the front desk's screen.")
                    .multilineTextAlignment(.center)
                    .foregroundStyle(.secondary)
            } else {
                ContentUnavailableView("Camera scanning isn't available",
                                       systemImage: "camera",
                                       description: Text("Allow camera access in Settings, or paste the code instead."))
            }
            Button("Paste code instead", systemImage: "doc.on.clipboard") {
                handle(UIPasteboard.general.string ?? "")
            }
        }
        .padding()
    }

    private func handle(_ text: String) {
        guard appState.pairing == .idle else { return }
        guard let payload = PairingPayload.parse(text) else {
            badCode = true
            return
        }
        appState.pair(with: payload)
    }
}

/// VisionKit's live QR scanner. Calls `onCode` with the first QR payload.
struct QRScanner: UIViewControllerRepresentable {
    let onCode: (String) -> Void

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let vc = DataScannerViewController(
            recognizedDataTypes: [.barcode(symbologies: [.qr])],
            qualityLevel: .balanced,
            isHighlightingEnabled: true)
        vc.delegate = context.coordinator
        return vc
    }

    func updateUIViewController(_ vc: DataScannerViewController, context: Context) {
        if !vc.isScanning { try? vc.startScanning() }
    }

    static func dismantleUIViewController(_ vc: DataScannerViewController, coordinator: Coordinator) {
        vc.stopScanning()
    }

    func makeCoordinator() -> Coordinator { Coordinator(onCode: onCode) }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        let onCode: (String) -> Void
        private var done = false
        init(onCode: @escaping (String) -> Void) { self.onCode = onCode }

        func dataScanner(_ scanner: DataScannerViewController, didAdd items: [RecognizedItem], allItems: [RecognizedItem]) {
            guard !done else { return }
            for item in items {
                if case .barcode(let code) = item, let text = code.payloadStringValue {
                    done = true
                    scanner.stopScanning()
                    onCode(text)
                    return
                }
            }
        }
    }
}
