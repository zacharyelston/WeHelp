import AVFoundation
import Foundation
import Observation
import UIKit
import WebRTC

enum RTC {
    static let factory: RTCPeerConnectionFactory = {
        RTCInitializeSSL()
        return RTCPeerConnectionFactory(encoderFactory: RTCDefaultVideoEncoderFactory(),
                                        decoderFactory: RTCDefaultVideoDecoderFactory())
    }()
}

struct Settings {
    static var d: UserDefaults { .standard }
    /// Launch args override these, e.g. `-server http://192.168.1.20:8733 -rooms r1,r2 -autojoin YES`.
    static var server: URL { URL(string: d.string(forKey: "server") ?? "http://192.168.1.20:8733")! }
    static var rooms: [String] { (d.string(forKey: "rooms") ?? "room1").split(separator: ",").map(String.init) }
    static var autojoin: Bool { d.bool(forKey: "autojoin") }
    static var width: Int { d.integer(forKey: "width") == 0 ? 1280 : d.integer(forKey: "width") }
    static var height: Int { d.integer(forKey: "height") == 0 ? 720 : d.integer(forKey: "height") }
    static var fps: Int { d.integer(forKey: "fps") == 0 ? 30 : d.integer(forKey: "fps") }
}

/// One 1:1 call in one room, relay-only ICE.
@Observable
final class Call: NSObject, RTCPeerConnectionDelegate, Identifiable {
    let id = UUID()
    let room: String
    var status = "idle"
    var summary = ""
    var localTrack: RTCVideoTrack?
    var remoteTrack: RTCVideoTrack?

    @ObservationIgnored private var signaling: SignalingClient
    @ObservationIgnored private var pc: RTCPeerConnection?
    @ObservationIgnored private var capturer: RTCVideoCapturer?
    @ObservationIgnored private var pendingCandidates: [RTCIceCandidate] = []
    @ObservationIgnored private var haveRemote = false
    @ObservationIgnored private var running = false
    @ObservationIgnored private var lastBytes: (sent: Double, recv: Double, t: Double) = (0, 0, 0)

    init(room: String) {
        self.room = room
        signaling = SignalingClient(base: Settings.server, room: room)
    }

    private func log(_ s: String) {
        print("[S2][\(room)] \(s)")
    }

    @MainActor
    func start() async {
        guard !running else { return }
        running = true
        status = "joining"
        do {
            let j = try await signaling.join()
            log("joined peer=\(j.peer) initiator=\(j.initiator)")
            setupPeerConnection(j)
            Task { await pollLoop() }
            Task { await statsLoop() }
            if j.initiator { makeOffer() } else { status = "waiting for peer" }
        } catch {
            status = "join failed: \(error.localizedDescription)"
            running = false
        }
    }

    func stop() {
        running = false
        (capturer as? TestPatternCapturer)?.stop()
        (capturer as? RTCCameraVideoCapturer)?.stopCapture()
        pc?.close()
        pc = nil
        Task { @MainActor in status = "ended" }
    }

    private func setupPeerConnection(_ j: JoinResponse) {
        let cfg = RTCConfiguration()
        cfg.iceServers = j.iceServers.map { RTCIceServer(urlStrings: $0.urls, username: $0.username, credential: $0.credential) }
        cfg.iceTransportPolicy = .relay          // the point of the spike: media only via our TURN
        cfg.sdpSemantics = .unifiedPlan
        cfg.continualGatheringPolicy = .gatherContinually
        cfg.bundlePolicy = .maxBundle
        cfg.rtcpMuxPolicy = .require
        let constraints = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
        guard let pc = RTC.factory.peerConnection(with: cfg, constraints: constraints, delegate: self) else {
            log("peerConnection nil"); return
        }
        self.pc = pc

        let source = RTC.factory.videoSource()
        let label = "\(UIDevice.current.name.prefix(14)) \(room)"
        #if targetEnvironment(simulator)
        let cap = TestPatternCapturer(delegate: source, width: Settings.width, height: Settings.height, fps: Settings.fps, label: label)
        cap.start()
        capturer = cap
        #else
        let cam = RTCCameraVideoCapturer(delegate: source)
        if let dev = RTCCameraVideoCapturer.captureDevices().first(where: { $0.position == .front }),
           let fmt = RTCCameraVideoCapturer.supportedFormats(for: dev).first(where: {
               let d = CMVideoFormatDescriptionGetDimensions($0.formatDescription)
               return d.width == Int32(Settings.width) && d.height == Int32(Settings.height)
           }) {
            cam.startCapture(with: dev, format: fmt, fps: Settings.fps)
        }
        capturer = cam
        let audio = RTC.factory.audioTrack(with: RTC.factory.audioSource(with: constraints), trackId: "a0")
        pc.add(audio, streamIds: ["s0"])
        #endif
        let track = RTC.factory.videoTrack(with: source, trackId: "v0")
        let sender = pc.add(track, streamIds: ["s0"])
        // `-keepResolution YES` stops WebRTC's CPU-overuse adaptation from
        // scaling down (it drops frames instead). Used to measure 720p bitrate.
        if Settings.d.bool(forKey: "keepResolution"), let sender {
            let p = sender.parameters
            p.degradationPreference = NSNumber(value: RTCDegradationPreference.maintainResolution.rawValue)
            sender.parameters = p
        }
        Task { @MainActor in self.localTrack = track }
    }

    private func makeOffer() {
        guard let pc else { return }
        Task { @MainActor in status = "offering" }
        pc.offer(for: RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)) { [weak self] sdp, err in
            guard let self, let sdp else { self?.log("offer error \(String(describing: err))"); return }
            pc.setLocalDescription(sdp) { err in
                if let err { self.log("setLocal offer \(err)"); return }
                Task { await self.signaling.send(["type": "offer", "sdp": sdp.sdp]) }
            }
        }
    }

    private func handle(_ m: [String: Any]) {
        guard let pc, let type = m["type"] as? String else { return }
        switch type {
        case "offer", "answer":
            guard let s = m["sdp"] as? String else { return }
            let desc = RTCSessionDescription(type: type == "offer" ? .offer : .answer, sdp: s)
            pc.setRemoteDescription(desc) { [weak self] err in
                guard let self else { return }
                if let err { self.log("setRemote \(type) \(err)"); return }
                self.haveRemote = true
                for c in self.pendingCandidates { pc.add(c) { _ in } }
                self.pendingCandidates.removeAll()
                guard type == "offer" else { return }
                pc.answer(for: RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)) { sdp, err in
                    guard let sdp else { self.log("answer error \(String(describing: err))"); return }
                    pc.setLocalDescription(sdp) { err in
                        if let err { self.log("setLocal answer \(err)"); return }
                        Task { await self.signaling.send(["type": "answer", "sdp": sdp.sdp]) }
                    }
                }
            }
        case "candidate":
            guard let cand = m["candidate"] as? String else { return }
            let c = RTCIceCandidate(sdp: cand, sdpMLineIndex: Int32((m["sdpMLineIndex"] as? Int) ?? 0), sdpMid: m["sdpMid"] as? String)
            log("remote candidate: \(cand)")
            if haveRemote { pc.add(c) { _ in } } else { pendingCandidates.append(c) }
        default:
            break
        }
    }

    private func pollLoop() async {
        while running {
            do {
                for m in try await signaling.poll() { handle(m) }
            } catch {
                try? await Task.sleep(for: .seconds(1))
            }
        }
    }

    // MARK: stats

    private func statsLoop() async {
        while running {
            try? await Task.sleep(for: .seconds(5))
            guard let pc else { continue }
            let report: RTCStatisticsReport = await withCheckedContinuation { cont in
                pc.statistics { cont.resume(returning: $0) }
            }
            var rep = summarize(report)
            rep["event"] = "stats"
            rep["connectionState"] = name(pc.connectionState)
            let line = "local=\(rep["localCandidateType"] ?? "-")/\(rep["localRelayProtocol"] ?? "-") remote=\(rep["remoteCandidateType"] ?? "-") dtls=\(rep["dtlsState"] ?? "-") srtp=\(rep["srtpCipher"] ?? "-")\nsend \(rep["sendKbps"] ?? 0) kbps \(rep["sendWidth"] ?? 0)x\(rep["sendHeight"] ?? 0)@\(rep["sendFps"] ?? 0)  recv \(rep["recvKbps"] ?? 0) kbps \(rep["width"] ?? 0)x\(rep["height"] ?? 0)@\(rep["fps"] ?? 0) rtt \(rep["rttMs"] ?? "-") ms"
            log("stats " + line.replacingOccurrences(of: "\n", with: " | "))
            await MainActor.run { self.summary = line }
            await signaling.report(rep)
        }
    }

    private func summarize(_ report: RTCStatisticsReport) -> [String: Any] {
        let stats = report.statistics
        func v(_ s: RTCStatistics?, _ k: String) -> Any? { s?.values[k] }
        var out: [String: Any] = [:]
        let transport = stats.values.first { $0.type == "transport" }
        if let t = transport {
            for k in ["dtlsState", "dtlsCipher", "srtpCipher", "tlsVersion", "dtlsRole"] { out[k] = v(t, k) }
            if let rid = v(t, "remoteCertificateId") as? String, let cert = stats[rid] {
                out["remoteCertFingerprint"] = v(cert, "fingerprint")
                out["remoteCertAlgorithm"] = v(cert, "fingerprintAlgorithm")
            }
            if let lid = v(t, "localCertificateId") as? String, let cert = stats[lid] {
                out["localCertFingerprint"] = v(cert, "fingerprint")
            }
        }
        let pairId = v(transport, "selectedCandidatePairId") as? String
        let pair = pairId.flatMap { stats[$0] }
            ?? stats.values.first { $0.type == "candidate-pair" && ($0.values["nominated"] as? NSNumber)?.boolValue == true }
        if let p = pair {
            if let rtt = v(p, "currentRoundTripTime") as? NSNumber { out["rttMs"] = Int(rtt.doubleValue * 1000) }
            if let bw = v(p, "availableOutgoingBitrate") as? NSNumber { out["availableOutgoingBitrate"] = bw.intValue }
            if let l = (v(p, "localCandidateId") as? String).flatMap({ stats[$0] }) {
                out["localCandidateType"] = v(l, "candidateType")
                out["localRelayProtocol"] = v(l, "relayProtocol")
                out["localAddress"] = v(l, "address")
            }
            if let r = (v(p, "remoteCandidateId") as? String).flatMap({ stats[$0] }) {
                out["remoteCandidateType"] = v(r, "candidateType")
                out["remoteAddress"] = v(r, "address")
            }
        }
        var sent = 0.0, recv = 0.0
        for s in stats.values where (s.values["kind"] as? String) == "video" {
            if s.type == "outbound-rtp" {
                sent = (v(s, "bytesSent") as? NSNumber)?.doubleValue ?? 0
                out["bytesSent"] = Int(sent)
                out["framesSent"] = v(s, "framesSent")
                out["sendFps"] = v(s, "framesPerSecond")
                out["sendWidth"] = v(s, "frameWidth")
                out["sendHeight"] = v(s, "frameHeight")
                out["qualityLimitationReason"] = v(s, "qualityLimitationReason")
                out["encoder"] = v(s, "encoderImplementation")
            } else if s.type == "inbound-rtp" {
                recv = (v(s, "bytesReceived") as? NSNumber)?.doubleValue ?? 0
                out["bytesReceived"] = Int(recv)
                out["framesDecoded"] = v(s, "framesDecoded")
                out["fps"] = v(s, "framesPerSecond")
                out["width"] = v(s, "frameWidth")
                out["height"] = v(s, "frameHeight")
                out["packetsLost"] = v(s, "packetsLost")
                out["jitter"] = v(s, "jitter")
                out["decoder"] = v(s, "decoderImplementation")
            }
        }
        for s in stats.values where s.type == "codec" {
            if let mt = v(s, "mimeType") as? String, mt.hasPrefix("video/") { out["codec"] = mt }
        }
        let now = CACurrentMediaTime()
        if lastBytes.t > 0 {
            let dt = now - lastBytes.t
            out["sendKbps"] = Int((sent - lastBytes.sent) * 8 / 1000 / dt)
            out["recvKbps"] = Int((recv - lastBytes.recv) * 8 / 1000 / dt)
        }
        lastBytes = (sent, recv, now)
        // JSON-safe: stringify anything that is not a number or string.
        return out.mapValues { ($0 is NSNumber || $0 is String) ? $0 : "\($0)" }
    }

    // MARK: RTCPeerConnectionDelegate

    func peerConnection(_ pc: RTCPeerConnection, didChange stateChanged: RTCSignalingState) {}
    func peerConnection(_ pc: RTCPeerConnection, didAdd stream: RTCMediaStream) {}
    func peerConnection(_ pc: RTCPeerConnection, didRemove stream: RTCMediaStream) {}
    func peerConnectionShouldNegotiate(_ pc: RTCPeerConnection) {}
    func peerConnection(_ pc: RTCPeerConnection, didChange newState: RTCIceConnectionState) {
        log("ice \(name(newState))")
    }
    func peerConnection(_ pc: RTCPeerConnection, didChange newState: RTCIceGatheringState) {}
    func peerConnection(_ pc: RTCPeerConnection, didGenerate candidate: RTCIceCandidate) {
        log("local candidate: \(candidate.sdp)")
        Task {
            await signaling.send(["type": "candidate", "candidate": candidate.sdp,
                                  "sdpMLineIndex": Int(candidate.sdpMLineIndex), "sdpMid": candidate.sdpMid ?? ""])
        }
    }
    func peerConnection(_ pc: RTCPeerConnection, didRemove candidates: [RTCIceCandidate]) {}
    func peerConnection(_ pc: RTCPeerConnection, didOpen dataChannel: RTCDataChannel) {}
    func peerConnection(_ pc: RTCPeerConnection, didChange newState: RTCPeerConnectionState) {
        let s = name(newState)
        log("pc \(s)")
        Task { @MainActor in self.status = s }
    }
    func peerConnection(_ pc: RTCPeerConnection, didAdd rtpReceiver: RTCRtpReceiver, streams: [RTCMediaStream]) {
        if let t = rtpReceiver.track as? RTCVideoTrack {
            Task { @MainActor in self.remoteTrack = t }
        }
    }
}

private func name(_ s: RTCPeerConnectionState) -> String {
    switch s {
    case .new: "new"
    case .connecting: "connecting"
    case .connected: "connected"
    case .disconnected: "disconnected"
    case .failed: "failed"
    case .closed: "closed"
    @unknown default: "unknown"
    }
}

private func name(_ s: RTCIceConnectionState) -> String {
    switch s {
    case .new: "new"
    case .checking: "checking"
    case .connected: "connected"
    case .completed: "completed"
    case .failed: "failed"
    case .disconnected: "disconnected"
    case .closed: "closed"
    default: "count"
    }
}
