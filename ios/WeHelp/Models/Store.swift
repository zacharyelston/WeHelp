import Foundation

/// Supply store and library (#97). Orders are placed through the pinned
/// tunnel to the Back Office; the public website only shows the catalogue.

struct Product: Identifiable, Decodable, Sendable, Hashable {
    let id: UUID
    let sku: String
    let name: String
    let description: String
    let category: String
    let unit: String
    let priceCents: Int

    enum CodingKeys: String, CodingKey {
        case id, sku, name, description, category, unit
        case priceCents = "price_cents"
    }
}

struct ProductList: Decodable, Sendable {
    let products: [Product]
    let payment: String
}

struct SupplyOrder: Identifiable, Decodable, Sendable, Hashable {
    let id: UUID
    let status: Status
    let note: String
    let staffNote: String
    let totalCents: Int
    let items: [Item]
    let createdAt: Date

    struct Item: Decodable, Sendable, Hashable {
        let productID: UUID
        let name: String
        let unit: String
        let unitPriceCents: Int
        let quantity: Int

        enum CodingKeys: String, CodingKey {
            case name, unit, quantity
            case productID = "product_id"
            case unitPriceCents = "unit_price_cents"
        }
    }

    enum Status: String, Decodable, Sendable {
        case placed, ready, fulfilled, cancelled, unknown

        init(from decoder: Decoder) throws {
            let raw = try decoder.singleValueContainer().decode(String.self)
            self = Status(rawValue: raw) ?? .unknown
        }

        var label: String {
            switch self {
            case .placed: "Placed"
            case .ready: "Ready for pickup"
            case .fulfilled: "Fulfilled"
            case .cancelled: "Cancelled"
            case .unknown: "Unknown"
            }
        }
    }

    enum CodingKeys: String, CodingKey {
        case id, status, note, items
        case staffNote = "staff_note"
        case totalCents = "total_cents"
        case createdAt = "created_at"
    }
}

struct Library: Decodable, Sendable {
    let podcasts: [Podcast]
    let files: [LibraryFile]
    let siteURL: String

    struct Podcast: Decodable, Sendable, Hashable {
        let title: String
        let url: String
        let description: String?
    }

    struct LibraryFile: Identifiable, Decodable, Sendable, Hashable {
        let id: UUID
        let title: String
        let filename: String
        let size: Int
    }

    enum CodingKeys: String, CodingKey {
        case podcasts, files
        case siteURL = "site_url"
    }
}

extension Int {
    /// Cents as local currency, e.g. "$28.00".
    var dollars: String {
        (Decimal(self) / 100).formatted(.currency(code: "USD"))
    }
}
