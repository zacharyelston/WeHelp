import Foundation

struct User: Identifiable, Codable {
    let id: UUID
    var displayName: String
    var role: Role
}

struct Message: Identifiable, Codable {
    let id: UUID
    var senderID: UUID
    var recipientID: UUID
    var kind: Kind
    var body: String
    var createdAt: Date

    enum Kind: String, Codable {
        case message
        case memo
        case checkin
        case reminder
    }
}
