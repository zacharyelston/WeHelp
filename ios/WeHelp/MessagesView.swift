import SwiftUI

/// Inbox state for the Messages tab. Everything lives in memory for the
/// life of the view; nothing is written to disk.
@MainActor
@Observable
final class InboxModel {
    private(set) var messages: [Message] = []
    private(set) var nextCursor: String?
    private(set) var isLoading = false
    private(set) var isLoadingMore = false
    private(set) var hasLoaded = false
    private(set) var error: String?
    /// Users the caller has an active link with (providers, for a patient).
    private(set) var contacts: [UUID] = []
    private var pagesLoaded = 0

    var unreadCount: Int { messages.filter(\.isUnread).count }

    /// Reloads the first page and the caller's links.
    func refresh(api: APIClient, me: Me?) async {
        guard !isLoading else { return }
        isLoading = true
        defer { isLoading = false }
        do {
            async let page = api.messages()
            async let links = api.links()
            let (p, l) = try await (page, links)
            messages = p.messages
            nextCursor = p.nextCursor
            pagesLoaded = 1
            contacts = Self.activeContacts(l, me: me)
            error = nil
        } catch is CancellationError {
            return
        } catch let err as URLError where err.code == .cancelled {
            return
        } catch {
            self.error = error.localizedDescription
        }
        hasLoaded = true
    }

    /// Background poll: only refresh while the user is on the first page, so
    /// it never throws away pages they scrolled to.
    func poll(api: APIClient, me: Me?) async {
        guard pagesLoaded <= 1, !isLoadingMore else { return }
        await refresh(api: api, me: me)
    }

    func loadMore(api: APIClient) async {
        guard let cursor = nextCursor, !isLoadingMore, !isLoading else { return }
        isLoadingMore = true
        defer { isLoadingMore = false }
        do {
            let page = try await api.messages(cursor: cursor)
            let seen = Set(messages.map(\.id))
            messages.append(contentsOf: page.messages.filter { !seen.contains($0.id) })
            nextCursor = page.nextCursor
            pagesLoaded += 1
        } catch {
            self.error = error.localizedDescription
        }
    }

    /// Marks read on the server, then locally. Failures are silent; the dot
    /// stays and the next open retries.
    func markRead(_ id: UUID, api: APIClient) async {
        guard let i = messages.firstIndex(where: { $0.id == id }), messages[i].isUnread else { return }
        do {
            try await api.markRead(messageID: id)
            if let j = messages.firstIndex(where: { $0.id == id }) { messages[j].readAt = .now }
        } catch {}
    }

    func message(_ id: UUID) -> Message? { messages.first { $0.id == id } }

    /// How a sender is shown. The API has no display names yet, so linked
    /// users are "Care team" (with a short ID only when there is more than
    /// one to tell apart).
    func senderName(_ id: UUID, role: Role?) -> String {
        let base = role == .provider ? "Patient" : "Care team"
        guard contacts.contains(id) else { return role == .provider ? "Patient" : "Clinic" }
        return contacts.count > 1 ? "\(base) · \(Self.shortID(id))" : base
    }

    static func shortID(_ id: UUID) -> String { String(id.uuidString.prefix(8)).lowercased() }

    private static func activeContacts(_ links: [CareLink], me: Me?) -> [UUID] {
        guard let mine = me.flatMap({ UUID(uuidString: $0.userID) }) else { return [] }
        var out: [UUID] = []
        for link in links where link.isActive {
            let other = link.providerID == mine ? link.patientID : link.providerID
            if other != mine, !out.contains(other) { out.append(other) }
        }
        return out
    }
}

struct MessagesView: View {
    @Environment(AppState.self) private var appState
    @State private var model = InboxModel()
    @State private var composing: ComposeTarget?
    @State private var sentNotice = false
    @State private var justSent = false

    var body: some View {
        NavigationStack {
            content
                .navigationTitle("Messages")
                .navigationDestination(for: UUID.self) { id in
                    MessageDetailView(model: model, messageID: id) { sender in
                        composing = ComposeTarget(recipient: sender)
                    }
                }
                .toolbar {
                    ToolbarItem(placement: .primaryAction) {
                        Button {
                            composing = ComposeTarget(recipient: nil)
                        } label: {
                            Label("New message", systemImage: "square.and.pencil")
                        }
                        .disabled(!model.hasLoaded)
                    }
                }
                .refreshable { await model.refresh(api: appState.api, me: appState.me) }
                .task { await model.refresh(api: appState.api, me: appState.me) }
                .task {
                    // Polling refresh while the tab is on screen.
                    while !Task.isCancelled {
                        try? await Task.sleep(for: .seconds(60))
                        if Task.isCancelled { break }
                        await model.poll(api: appState.api, me: appState.me)
                    }
                }
                .sheet(item: $composing, onDismiss: {
                    if justSent { justSent = false; sentNotice = true }
                }) { target in
                    ComposeView(model: model, initialRecipient: target.recipient) {
                        justSent = true
                    }
                }
                .alert("Message sent", isPresented: $sentNotice) {
                    Button("OK", role: .cancel) {}
                } message: {
                    Text("Your care team will see it in their inbox.")
                }
        }
    }

    @ViewBuilder
    private var content: some View {
        if !model.hasLoaded && model.messages.isEmpty {
            ProgressView("Loading messages…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if let error = model.error, model.messages.isEmpty {
            ContentUnavailableView {
                Label("Couldn't load messages", systemImage: "wifi.exclamationmark")
            } description: {
                Text(error)
            } actions: {
                Button("Try again") {
                    Task { await model.refresh(api: appState.api, me: appState.me) }
                }
                .buttonStyle(.borderedProminent)
            }
        } else if model.messages.isEmpty {
            // A List keeps pull-to-refresh working on the empty state.
            List {
                ContentUnavailableView(
                    "No messages yet",
                    systemImage: "bubble.left.and.bubble.right",
                    description: Text("Messages and memos from your care team appear here.")
                )
                .listRowSeparator(.hidden)
            }
            .listStyle(.plain)
        } else {
            List {
                if let error = model.error {
                    Label(error, systemImage: "exclamationmark.triangle")
                        .font(.footnote)
                        .foregroundStyle(.orange)
                }
                ForEach(model.messages) { msg in
                    NavigationLink(value: msg.id) {
                        MessageRow(message: msg, sender: model.senderName(msg.senderID, role: appState.role))
                    }
                }
                if model.nextCursor != nil {
                    HStack {
                        Spacer()
                        ProgressView()
                        Spacer()
                    }
                    .task(id: model.nextCursor) { await model.loadMore(api: appState.api) }
                }
            }
            .listStyle(.plain)
        }
    }
}

struct ComposeTarget: Identifiable {
    let id = UUID()
    let recipient: UUID?
}

struct MessageRow: View {
    let message: Message
    let sender: String

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Circle()
                .fill(message.isUnread ? Color.accentColor : .clear)
                .frame(width: 9, height: 9)
                .padding(.top, 6)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 4) {
                HStack(alignment: .firstTextBaseline) {
                    Text(sender)
                        .font(.headline)
                        .fontWeight(message.isUnread ? .semibold : .regular)
                    Spacer()
                    Text(message.createdAt, format: Self.dateStyle(message.createdAt))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                KindBadge(kind: message.kind)
                Text(message.body)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
                    .privacySensitive()
            }
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .combine)
        .accessibilityHint(message.isUnread ? "Unread" : "")
    }

    static func dateStyle(_ date: Date) -> Date.FormatStyle {
        Calendar.current.isDateInToday(date)
            ? .dateTime.hour().minute()
            : .dateTime.month(.abbreviated).day()
    }
}

struct KindBadge: View {
    let kind: Message.Kind

    var body: some View {
        HStack(spacing: 3) {
            Image(systemName: kind.systemImage)
            Text(kind.label)
        }
            .font(.caption2.weight(.medium))
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(tint.opacity(0.15), in: Capsule())
            .foregroundStyle(tint)
    }

    private var tint: Color {
        switch kind {
        case .message: .blue
        case .memo: .purple
        case .checkin: .green
        case .reminder: .orange
        case .unknown: .gray
        }
    }
}

struct MessageDetailView: View {
    @Environment(AppState.self) private var appState
    let model: InboxModel
    let messageID: UUID
    let onReply: (UUID) -> Void

    var body: some View {
        if let msg = model.message(messageID) {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    HStack {
                        KindBadge(kind: msg.kind)
                        Spacer()
                        Text(msg.createdAt, format: .dateTime.month().day().year().hour().minute())
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    Text(model.senderName(msg.senderID, role: appState.role))
                        .font(.title3.weight(.semibold))
                    Text(msg.body)
                        .font(.body)
                        .textSelection(.enabled)
                        .privacySensitive()
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding()
            }
            .navigationTitle(msg.kind.label)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                if model.contacts.contains(msg.senderID) {
                    ToolbarItem(placement: .primaryAction) {
                        Button("Reply") { onReply(msg.senderID) }
                    }
                }
            }
            .task { await model.markRead(msg.id, api: appState.api) }
        } else {
            ContentUnavailableView("Message unavailable", systemImage: "envelope")
        }
    }
}

struct ComposeView: View {
    @Environment(AppState.self) private var appState
    @Environment(\.dismiss) private var dismiss
    let model: InboxModel
    let initialRecipient: UUID?
    let onSent: () -> Void

    @State private var recipient: UUID?
    @State private var text = ""
    @State private var sending = false
    @State private var error: String?
    @FocusState private var editorFocused: Bool

    private var trimmed: String { text.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        NavigationStack {
            Form {
                if model.contacts.isEmpty {
                    Section {
                        Label("You're not linked to a care team yet. Ask your clinic to connect your account.",
                              systemImage: "person.crop.circle.badge.exclamationmark")
                            .foregroundStyle(.secondary)
                    }
                } else {
                    Section("To") {
                        if model.contacts.count == 1, let only = model.contacts.first {
                            Text(model.senderName(only, role: appState.role))
                        } else {
                            Picker("Recipient", selection: $recipient) {
                                ForEach(model.contacts, id: \.self) { id in
                                    Text(model.senderName(id, role: appState.role)).tag(Optional(id))
                                }
                            }
                            .labelsHidden()
                        }
                    }
                    Section {
                        TextField("Write a message", text: $text, axis: .vertical)
                            .lineLimit(5...12)
                            .focused($editorFocused)
                            .privacySensitive()
                    } footer: {
                        Text("Not for emergencies. If you need urgent help, call your local emergency number.")
                    }
                }
                if let error {
                    Section {
                        Label(error, systemImage: "exclamationmark.triangle")
                            .foregroundStyle(.red)
                    }
                }
            }
            .navigationTitle("New message")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if sending {
                        ProgressView()
                    } else {
                        Button("Send") { Task { await send() } }
                            .disabled(trimmed.isEmpty || recipient == nil)
                    }
                }
            }
            .interactiveDismissDisabled(sending || !trimmed.isEmpty)
            .onAppear {
                if recipient == nil {
                    recipient = initialRecipient.flatMap { model.contacts.contains($0) ? $0 : nil }
                        ?? model.contacts.first
                }
                editorFocused = true
            }
        }
    }

    private func send() async {
        guard let recipient, !trimmed.isEmpty else { return }
        sending = true
        defer { sending = false }
        do {
            try await appState.api.sendMessage(to: recipient, kind: "message", body: trimmed)
            onSent()
            dismiss()
        } catch {
            self.error = error.localizedDescription
        }
    }
}
