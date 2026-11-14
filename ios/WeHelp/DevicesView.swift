import SwiftUI

/// This account's devices (#10): every phone or iPad paired in the office.
/// Revoke a lost one, or sign out every device except this one.
struct DevicesView: View {
    @Environment(AppState.self) private var appState
    @State private var devices: [Device] = []
    @State private var loading = true
    @State private var error: String?
    @State private var confirmOthers = false

    private var others: [Device] { devices.filter { !$0.current && $0.status == "active" } }

    var body: some View {
        List {
            if let error {
                Section { Label(error, systemImage: "exclamationmark.triangle").foregroundStyle(.red) }
            }
            Section {
                ForEach(devices) { d in
                    DeviceRow(device: d)
                        .swipeActions {
                            if !d.current && d.status == "active" {
                                Button("Revoke", role: .destructive) { Task { await revoke([d]) } }
                            }
                        }
                }
            } footer: {
                Text("Each device was paired at your practice. A revoked device is signed out at once and must be paired again in the office.")
            }
            if !others.isEmpty {
                Section {
                    Button("Sign out other devices", role: .destructive) { confirmOthers = true }
                }
            }
        }
        .overlay { if loading && devices.isEmpty { ProgressView() } }
        .navigationTitle("Devices")
        .refreshable { await load() }
        .task { await load() }
        .confirmationDialog("Sign out \(others.count) other device\(others.count == 1 ? "" : "s")?",
                            isPresented: $confirmOthers, titleVisibility: .visible) {
            Button("Sign out other devices", role: .destructive) { Task { await revoke(others) } }
        } message: {
            Text("They will need to be paired again at your practice.")
        }
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

    private func revoke(_ list: [Device]) async {
        do {
            for d in list { try await appState.api.revokeDevice(d.id) }
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
        await load()
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
