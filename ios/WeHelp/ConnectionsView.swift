import SwiftUI

/// Signed-out home: the practices/servers this phone knows. Tap one to sign
/// in; "+" adds a new server; swipe to edit or delete.
struct ConnectionsView: View {
    @Environment(AppState.self) private var appState
    @State private var editing: EditTarget?

    enum EditTarget: Identifiable {
        case new
        case existing(SavedConnection)
        var id: String {
            switch self {
            case .new: "new"
            case .existing(let c): c.id
            }
        }
    }

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
                                NavigationLink(value: conn) { ConnectionRow(conn: conn) }
                                    .swipeActions {
                                        if conn.devPassword == nil {
                                            Button("Delete", role: .destructive) { appState.forgetConnection(conn) }
                                        }
                                        Button("Edit") { editing = .existing(conn) }.tint(.blue)
                                    }
                            }
                        } footer: {
                            Text("Choose your practice to sign in.")
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
            .sheet(item: $editing) { target in
                switch target {
                case .new: ConnectionEditorView(original: nil)
                case .existing(let conn): ConnectionEditorView(original: conn)
                }
            }
        }
    }
}

struct ConnectionRow: View {
    let conn: SavedConnection

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
        }
    }
}
