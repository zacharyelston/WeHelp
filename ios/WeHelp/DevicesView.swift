import SwiftUI

/// This account's devices (#10): every phone or iPad paired in the office.
/// Read-only: devices are never unpaired from the app; the practice revokes
/// a lost or replaced one from its staff page.
struct DevicesView: View {
    @Environment(AppState.self) private var appState
    @State private var devices: [Device] = []
    @State private var loading = true
    @State private var error: String?

    var body: some View {
        List {
            if let error {
                Section { Label(error, systemImage: "exclamationmark.triangle").foregroundStyle(.red) }
            }
            Section {
                ForEach(devices) { d in
                    DeviceRow(device: d)
                }
            } footer: {
                Text("Each device was paired at your practice. Lost or replaced one? Ask the practice to remove it; it's signed out at once.")
            }
        }
        .overlay { if loading && devices.isEmpty { ProgressView() } }
        .navigationTitle("Devices")
        .refreshable { await load() }
        .task { await load() }
    }

    private func load() async {
        loading = true
        defer { loading = false }
        do {
            devices = try await appState.api.devices()
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }

}

private struct DeviceRow: View {
    let device: Device

    var body: some View {
        HStack {
            Image(systemName: device.name.localizedCaseInsensitiveContains("ipad") ? "ipad" : "iphone")
                .foregroundStyle(device.status == "active" ? Color.accentColor : .secondary)
            VStack(alignment: .leading) {
                Text(device.name.isEmpty ? "Unnamed device" : device.name)
                Group {
                    if device.status == "revoked" {
                        Text("Revoked")
                    } else if let seen = device.lastSeenAt {
                        Text("Last used \(seen, format: .relative(presentation: .named))")
                    } else {
                        Text("Paired \(device.createdAt, format: .dateTime.day().month().year())")
                    }
                }
                .font(.caption)
                .foregroundStyle(.secondary)
            }
            Spacer()
            if device.current {
                Text("This device").font(.caption.weight(.semibold)).foregroundStyle(.tint)
            }
        }
    }
}
