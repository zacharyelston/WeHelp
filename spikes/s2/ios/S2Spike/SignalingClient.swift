import Foundation

/// Spike-only HTTP long-poll signaling against spikes/s2/server.
/// In V1 this goes through the Back Office over the pinned channel.
struct JoinResponse: Decodable {
    struct IceServer: Decodable {
        let urls: [String]
        let username: String
        let credential: String
    }
    let peer: String
    let initiator: Bool
    let iceServers: [IceServer]
}

actor SignalingClient {
    let base: URL
    let room: String
    private(set) var peer: String = ""
    private let session: URLSession

    init(base: URL, room: String) {
        self.base = base
        self.room = room
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = 30
        session = URLSession(configuration: cfg)
    }

    private func url(_ path: String) -> URL {
        var c = URLComponents(url: base.appendingPathComponent(path), resolvingAgainstBaseURL: false)!
        c.queryItems = [URLQueryItem(name: "room", value: room)]
        if !peer.isEmpty { c.queryItems?.append(URLQueryItem(name: "peer", value: peer)) }
        return c.url!
    }

    func join() async throws -> JoinResponse {
        var req = URLRequest(url: url("join"))
        req.httpMethod = "POST"
        let (data, _) = try await session.data(for: req)
        let r = try JSONDecoder().decode(JoinResponse.self, from: data)
        peer = r.peer
        return r
    }

    /// Sends are serialised by the actor, so offer/answer/candidates keep order.
    func send(_ msg: [String: Any]) async {
        await post("send", msg)
    }

    func report(_ msg: [String: Any]) async {
        await post("report", msg)
    }

    private func post(_ path: String, _ msg: [String: Any]) async {
        var req = URLRequest(url: url(path))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try? JSONSerialization.data(withJSONObject: msg)
        _ = try? await session.data(for: req)
    }

    func poll() async throws -> [[String: Any]] {
        let (data, _) = try await session.data(from: url("poll"))
        return (try JSONSerialization.jsonObject(with: data) as? [[String: Any]]) ?? []
    }
}
