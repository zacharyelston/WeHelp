import Foundation

enum Role: String, Codable {
    case provider
    case patient
}

/// Session-scoped state: who is signed in and how to reach the backend.
@Observable
final class AppState {
    var role: Role?
    var api = APIClient(baseURL: URL(string: "http://localhost:8080")!)
}
