import Foundation

/// One paired device of the signed-in account (#10).
struct Device: Decodable, Identifiable, Sendable {
    let id: String
    let name: String
    let createdAt: Date
    let lastSeenAt: Date?
    let status: String // active, revoked
    let current: Bool

    enum CodingKeys: String, CodingKey {
        case id, name, status, current
        case createdAt = "created_at", lastSeenAt = "last_seen_at"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decode(String.self, forKey: .name)
        createdAt = try c.decode(Date.self, forKey: .createdAt)
        lastSeenAt = try c.decodeIfPresent(Date.self, forKey: .lastSeenAt)
        status = try c.decode(String.self, forKey: .status)
        current = try c.decodeIfPresent(Bool.self, forKey: .current) ?? false
    }
}

/// One inbox entry from `GET /api/v1/messages`. Held in memory only; PHI is
/// never written to disk.
struct Message: Identifiable, Decodable, Sendable, Hashable {
    let id: UUID
    let senderID: UUID
    let recipientID: UUID
    let kind: Kind
    let body: String
    var readAt: Date?
    let createdAt: Date

    var isUnread: Bool { readAt == nil }

    enum Kind: String, Decodable, Sendable {
        case message, memo, checkin, reminder, unknown

        init(from decoder: Decoder) throws {
            let raw = try decoder.singleValueContainer().decode(String.self)
            self = Kind(rawValue: raw) ?? .unknown
        }

        var label: String {
            switch self {
            case .message: "Message"
            case .memo: "Memo"
            case .checkin: "Check-in"
            case .reminder: "Reminder"
            case .unknown: "Note"
            }
        }

        var systemImage: String {
            switch self {
            case .message: "bubble.left"
            case .memo: "doc.text"
            case .checkin: "checkmark.circle"
            case .reminder: "bell"
            case .unknown: "envelope"
            }
        }
    }

    enum CodingKeys: String, CodingKey {
        case id, kind, body
        case senderID = "sender_id"
        case recipientID = "recipient_id"
        case readAt = "read_at"
        case createdAt = "created_at"
    }
}

/// A page of the inbox. `nextCursor` is nil on the last page.
/// One person the caller is linked with, and the latest message (#67).
struct Conversation: Identifiable, Decodable, Sendable, Hashable {
    let userID: UUID
    let name: String
    let role: String
    let lastBody: String?
    let lastAt: Date?
    let lastFromMe: Bool
    let unreadCount: Int

    var id: UUID { userID }

    enum CodingKeys: String, CodingKey {
        case name, role
        case userID = "user_id", lastBody = "last_body", lastAt = "last_at"
        case lastFromMe = "last_from_me", unreadCount = "unread_count"
    }
}

struct MessagePage: Decodable, Sendable {
    let messages: [Message]
    let nextCursor: String?

    enum CodingKeys: String, CodingKey {
        case messages
        case nextCursor = "next_cursor"
    }
}

/// A provider-patient link from `GET /api/v1/links`.
struct CareLink: Decodable, Sendable, Hashable {
    let providerID: UUID
    let patientID: UUID
    let status: String
    let providerName: String?
    let patientName: String?

    var isActive: Bool { status == "active" }

    enum CodingKeys: String, CodingKey {
        case status
        case providerID = "provider_id"
        case patientID = "patient_id"
        case providerName = "provider_name"
        case patientName = "patient_name"
    }
}

/// Response to `POST /api/v1/messages`.
struct CreatedMessage: Decodable, Sendable {
    let id: UUID
    let kind: String
}

/// One appointment from `GET /api/v1/appointments`. Held in memory only.
struct Appointment: Identifiable, Decodable, Sendable, Hashable {
    let id: UUID
    let providerID: UUID
    let patientID: UUID
    let startsAt: Date
    let endsAt: Date
    let status: Status
    let createdAt: Date

    /// Server statuses. A future status the app doesn't know decodes as
    /// `.unknown` rather than failing the whole list.
    enum Status: String, Decodable, Sendable {
        case scheduled, cancelled, completed, unknown
        case noShow = "no_show"

        init(from decoder: Decoder) throws {
            let raw = try decoder.singleValueContainer().decode(String.self)
            self = Status(rawValue: raw) ?? .unknown
        }
    }

    enum CodingKeys: String, CodingKey {
        case id, status
        case providerID = "provider_id"
        case patientID = "patient_id"
        case startsAt = "starts_at"
        case endsAt = "ends_at"
        case createdAt = "created_at"
    }
}

struct AppointmentList: Decodable, Sendable {
    let appointments: [Appointment]
}
