import SwiftUI

/// Messages (#67): one conversation per linked person (your providers, or
/// your patients), each a thread in both directions. Everything lives in
/// memory for the life of the view; nothing is written to disk.
struct MessagesView: View {
    @Environment(AppState.self) private var appState
    @State private var conversations: [Conversation] = []
    @State private var loaded = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            List {
                if let error {
                    Label(error, systemImage: "wifi.exclamationmark").foregroundStyle(.red)
                }
                ForEach(conversations) { c in
                    NavigationLink(value: c) { ConversationRow(conversation: c) }
                }
            }
            .overlay {
                if loaded && conversations.isEmpty && error == nil {
                    ContentUnavailableView("No conversations yet", systemImage: "bubble.left.and.bubble.right",
                                           description: Text(appState.isStaff ? "Your linked patients appear here." : "Your care team appears here once your practice links you."))
                } else if !loaded {
                    ProgressView()
                }
            }
            .navigationTitle("Messages")
            .navigationDestination(for: Conversation.self) { ThreadView(conversation: $0) }
            .refreshable { await load() }
            .task {
                // Load now, then keep the list fresh while it's on screen.
                while !Task.isCancelled {
                    await load()
                    try? await Task.sleep(for: .seconds(15))
                }
            }
        }
    }

    private func load() async {
        do {
            conversations = try await appState.api.conversations()
            error = nil
        } catch is CancellationError {
        } catch {
            self.error = error.localizedDescription
        }
        loaded = true
    }
}

private struct ConversationRow: View {
    let conversation: Conversation

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: conversation.role == "patient" ? "person.crop.circle" : "stethoscope")
                .font(.title2)
                .foregroundStyle(.tint)
            VStack(alignment: .leading, spacing: 2) {
                HStack {
                    Text(conversation.name.isEmpty ? "Unnamed" : conversation.name)
                        .font(conversation.unreadCount > 0 ? .headline : .body)
                    Spacer()
                    if let at = conversation.lastAt {
                        Text(at, format: .relative(presentation: .named))
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                Text(preview)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
            if conversation.unreadCount > 0 {
                Text("\(conversation.unreadCount)")
                    .font(.caption.bold())
                    .padding(.horizontal, 7).padding(.vertical, 2)
                    .background(.tint, in: Capsule())
                    .foregroundStyle(.white)
            }
        }
        .privacySensitive()
    }

    private var preview: String {
        guard let body = conversation.lastBody else { return "No messages yet" }
        return conversation.lastFromMe ? "You: \(body)" : body
    }
}

/// One conversation, oldest at the top like a chat, with a reply box.
struct ThreadView: View {
    @Environment(AppState.self) private var appState
    let conversation: Conversation

    @State private var messages: [Message] = [] // newest first, as the API returns them
    @State private var cursor: String?
    @State private var draft = ""
    @State private var sending = false
    @State private var error: String?

    private var me: UUID? { appState.me.flatMap { UUID(uuidString: $0.userID) } }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 8) {
                    if cursor != nil {
                        Button("Earlier messages") { Task { await loadOlder() } }
                            .font(.footnote)
                            .padding(.top, 8)
                    }
                    ForEach(messages.reversed()) { m in
                        Bubble(message: m, mine: m.senderID == me).id(m.id)
                    }
                }
                .padding(.horizontal)
            }
            .defaultScrollAnchor(.bottom)
            .onChange(of: messages.first?.id) { _, newest in
                if let newest { withAnimation { proxy.scrollTo(newest, anchor: .bottom) } }
            }
        }
        .safeAreaInset(edge: .bottom) { composer }
        .navigationTitle(conversation.name)
        .navigationBarTitleDisplayMode(.inline)
        .task {
            while !Task.isCancelled {
                await refresh()
                try? await Task.sleep(for: .seconds(10))
            }
        }
    }

    private var composer: some View {
        VStack(spacing: 4) {
            if let error {
                Text(error).font(.caption).foregroundStyle(.red)
            }
            HStack(alignment: .bottom) {
                TextField("Message", text: $draft, axis: .vertical)
                    .lineLimit(1...5)
                    .textFieldStyle(.roundedBorder)
                Button {
                    Task { await send() }
                } label: {
                    Image(systemName: "arrow.up.circle.fill").font(.title)
                }
                .disabled(draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || sending)
                .accessibilityLabel("Send")
            }
        }
        .padding(.horizontal)
        .padding(.vertical, 8)
        .background(.bar)
    }

    /// Reloads the newest page and marks what they sent as read.
    private func refresh() async {
        do {
            let page = try await appState.api.thread(with: conversation.userID)
            let older = messages.filter { m in !page.messages.contains { $0.id == m.id } }
                .filter { m in page.messages.last.map { m.createdAt < $0.createdAt } ?? false }
            messages = page.messages + older
            if cursor == nil || older.isEmpty { cursor = page.nextCursor }
            error = nil
            for m in page.messages where m.isUnread && m.senderID == conversation.userID {
                try? await appState.api.markRead(messageID: m.id)
            }
        } catch is CancellationError {
        } catch {
            self.error = error.localizedDescription
        }
    }

    private func loadOlder() async {
        guard let cursor else { return }
        do {
            let page = try await appState.api.thread(with: conversation.userID, cursor: cursor)
            let seen = Set(messages.map(\.id))
            messages.append(contentsOf: page.messages.filter { !seen.contains($0.id) })
            self.cursor = page.nextCursor
        } catch {
            self.error = error.localizedDescription
        }
    }

    private func send() async {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        sending = true
        defer { sending = false }
        do {
            try await appState.api.sendMessage(to: conversation.userID, body: text)
            draft = ""
            await refresh()
        } catch {
            self.error = error.localizedDescription
        }
    }
}

private struct Bubble: View {
    let message: Message
    let mine: Bool

    var body: some View {
        HStack {
            if mine { Spacer(minLength: 40) }
            VStack(alignment: mine ? .trailing : .leading, spacing: 2) {
                if message.kind != .message {
                    Label(message.kind.label, systemImage: message.kind.systemImage)
                        .font(.caption2)
                        .foregroundStyle(mine ? .white.opacity(0.8) : .secondary)
                }
                Text(message.body)
                Text(message.createdAt, format: .dateTime.hour().minute())
                    .font(.caption2)
                    .foregroundStyle(mine ? .white.opacity(0.8) : .secondary)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            .background(mine ? AnyShapeStyle(.tint) : AnyShapeStyle(.fill.tertiary),
                        in: RoundedRectangle(cornerRadius: 16))
            .foregroundStyle(mine ? .white : .primary)
            .privacySensitive()
            if !mine { Spacer(minLength: 40) }
        }
    }
}
