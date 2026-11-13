import SwiftUI

/// Signed-out home: the practices/servers this phone knows. Tap one to sign
/// in; "+" adds a server; the ⓘ button, a long press, or a swipe edits or
/// deletes one.
struct ConnectionsView: View {
    @Environment(AppState.self) private var appState
    @State private var editing: EditTarget?
    @State private var confirmDelete: SavedConnection?

    var body: some View {
        NavigationStack {
            Group {
                if appState.savedConnections.isEmpty {
                    ContentUnavailableView {
                        Label("No servers yet", systemImage: "server.rack")
                    } description: {
                        Text("Add your practice's WeHelp server to sign in.")
                    } actions: {
                        Button("Add server") { editing = .new }
                            .buttonStyle(.borderedProminent)
                    }
                } else {
                    List {
                        Section {
                            ForEach(appState.savedConnections) { conn in
                                NavigationLink(value: conn) {
                                    ConnectionRow(conn: conn) { editing = .existing(conn) }
                                }
                                .contextMenu {
                                    Button { editing = .existing(conn) } label: { Label("Edit", systemImage: "pencil") }
                                    Button(role: .destructive) { confirmDelete = conn } label: { Label("Delete", systemImage: "trash") }
                                }
                                .swipeActions {
                                    Button("Delete", role: .destructive) { confirmDelete = conn }
                                    Button("Edit") { editing = .existing(conn) }.tint(.blue)
                                }
                            }
                        } footer: {
                            Text("Tap a practice to sign in. Tap ⓘ to edit or delete it.")
                        }
                    }
                }
            }
            .navigationTitle("Servers")
            .navigationDestination(for: SavedConnection.self) { SignInView(connection: $0) }
            .toolbar {
                ToolbarItem(placement: .primaryAction) {
                    Button { editing = .new } label: { Label("Add server", systemImage: "plus") }
                }
            }
            .sheet(item: $editing) { ConnectionEditorView(target: $0) }
            .confirmationDialog("Delete this server?", isPresented: Binding(
                get: { confirmDelete != nil }, set: { if !$0 { confirmDelete = nil } }),
                presenting: confirmDelete
            ) { conn in
                Button("Delete \(conn.tenant)", role: .destructive) { appState.forgetConnection(conn) }
            } message: { conn in
                Text("\(conn.subtitle) will be removed from this phone.")
            }
        }
    }
}

/// What the editor sheet is editing.
enum EditTarget: Identifiable {
    case new
    case existing(SavedConnection)

    var id: String {
        switch self {
        case .new: "new"
        case .existing(let c): c.id
        }
    }

    var original: SavedConnection? {
        if case .existing(let c) = self { c } else { nil }
    }
}

struct ConnectionRow: View {
    let conn: SavedConnection
    var onInfo: (() -> Void)? = nil

    var body: some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text(conn.title)
                Text(conn.subtitle).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            if conn.devPassword != nil {
                Text("Demo").font(.caption2).padding(.horizontal, 6).padding(.vertical, 2)
                    .background(.orange.opacity(0.2), in: Capsule())
            }
            if let onInfo {
                Button(action: onInfo) {
                    Image(systemName: "info.circle").imageScale(.large)
                }
                .buttonStyle(.borderless)
                .accessibilityLabel("Edit \(conn.tenant)")
            }
        }
    }
}
