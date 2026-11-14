import AVFoundation
import Foundation
import Observation
import UIKit
@preconcurrency import WebRTC

/// One video visit on this phone (#6, docs/MOBILE_CLIENT.md §15).
///
/// Media goes phone to phone, DTLS-SRTP end to end, through the Relay's
/// TURN only (`iceTransportPolicy = relay`). Signaling goes through the Back
/// Office over the pinned connection. The provider always makes the offer
/// and the patient answers; the provider re-offers with an ICE restart when
/// the patient (re)joins or asks. Nothing about the call is stored.
@MainActor
@Observable
final class VisitCall: NSObject {
    enum Phase: Equatable {
        case starting
        case waiting(String)    // the other person isn't here yet, or left
        case connecting
        case connected
        case reconnecting
        case ended(String)
        case failed(String)

        var isOver: Bool {
            switch self {
            case .ended, .failed: true
            default: false
            }
        }
    }

    let appointment: Appointment
    let peerName: String
    private(set) var phase: Phase = .starting
    private(set) var localTrack: RTCVideoTrack?
    private(set) var remoteTrack: RTCVideoTrack?
    private(set) var muted = false
    private(set) var cameraOff = false
    private(set) var isProvider = false
    /// The DTLS fingerprint check (see DTLSFingerprint) has passed.
    private(set) var verified = false

    @ObservationIgnored private let api: APIClient
    @ObservationIgnored private let starting: Bool
    @ObservationIgnored private var visitID: UUID?
    @ObservationIgnored private var after: Int64 = 0
    @ObservationIgnored private var pc: RTCPeerConnection?
    @ObservationIgnored private var capturer: RTCCameraVideoCapturer?
    @ObservationIgnored private var useFront = true
    @ObservationIgnored private var audioTrack: RTCAudioTrack?
    @ObservationIgnored private var remoteSDP: String?
    @ObservationIgnored private var pending: [RTCIceCandidate] = []
    @ObservationIgnored private var pollTask: Task<Void, Never>?
    @ObservationIgnored private var checkTask: Task<Void, Never>?
    @ObservationIgnored private var offered = false

    private static let factory: RTCPeerConnectionFactory = {
        RTCInitializeSSL()
        return RTCPeerConnectionFactory(encoderFactory: RTCDefaultVideoEncoderFactory(),
                                        decoderFactory: RTCDefaultVideoDecoderFactory())
    }()

    /// `start` is true when the provider opens the visit; patients join.
    init(appointment: Appointment, peerName: String, api: APIClient, start: Bool) {
        self.appointment = appointment
        self.peerName = peerName
        self.api = api
        self.starting = start
    }

    // MARK: Lifecycle

    func begin() async {
        guard await permissions() else {
            phase = .failed("WeHelp needs the camera and microphone for a video visit. Turn them on in Settings > WeHelp.")
            return
        }
        let join: VisitJoin
        do {
            join = starting ? try await api.startVisit(appointment: appointment.id)
                : try await api.joinVisit(appointment: appointment.id)
        } catch {
            phase = .failed(Self.message(for: error))
            return
        }
        visitID = join.visitID
        after = join.after
        isProvider = join.isProvider
        UIApplication.shared.isIdleTimerDisabled = true
        configureAudio()
        setUpPeerConnection(join.iceServers)
        phase = join.peerPresent ? .connecting : .waiting("Waiting for \(peerName)…")
        pollTask = Task { [weak self] in await self?.pollLoop() }
        if isProvider && join.peerPresent { await offer(restart: false) }
        if !isProvider && join.peerPresent { send(OutgoingSignal(type: "restart")) } // ask for a fresh offer
    }

    /// Leave the call; the visit stays open to rejoin.
    func leave() async {
        if let visitID { try? await api.leaveVisit(visitID) }
        tearDown(.ended("You left the visit."))
    }

    /// The provider ends the visit for both.
    func endForBoth() async {
        if let visitID { try? await api.endVisit(visitID) }
        tearDown(.ended("The visit has ended."))
    }

    private func tearDown(_ final: Phase) {
        pollTask?.cancel()
        checkTask?.cancel()
        capturer?.stopCapture()
        capturer = nil
        pc?.close()
        pc = nil
        localTrack = nil
        remoteTrack = nil
        UIApplication.shared.isIdleTimerDisabled = false
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
        if !phase.isOver { phase = final }
    }

    // MARK: Controls

    func toggleMute() {
        muted.toggle()
        audioTrack?.isEnabled = !muted
    }

    func toggleCamera() {
        cameraOff.toggle()
        localTrack?.isEnabled = !cameraOff
        if cameraOff { capturer?.stopCapture() } else { startCapture() }
    }

    func flipCamera() {
        useFront.toggle()
        if !cameraOff { startCapture() }
    }

    // MARK: Setup

    private func permissions() async -> Bool {
        let video = await AVCaptureDevice.requestAccess(for: .video)
        let audio = await AVCaptureDevice.requestAccess(for: .audio)
        return video && audio
    }

    private func configureAudio() {
        let s = AVAudioSession.sharedInstance()
        try? s.setCategory(.playAndRecord, mode: .videoChat, options: [.defaultToSpeaker, .allowBluetooth])
        try? s.setActive(true)
    }

    private func setUpPeerConnection(_ servers: [VisitJoin.ICEServer]) {
        let cfg = RTCConfiguration()
        cfg.iceServers = servers.map { RTCIceServer(urlStrings: $0.urls, username: $0.username, credential: $0.credential) }
        cfg.iceTransportPolicy = .relay // media only through the Relay's TURN
        cfg.sdpSemantics = .unifiedPlan
        cfg.bundlePolicy = .maxBundle
        cfg.rtcpMuxPolicy = .require
        cfg.continualGatheringPolicy = .gatherContinually
        // AES-GCM SRTP, preferred; the older suite only as a fallback.
        cfg.cryptoOptions = RTCCryptoOptions(srtpEnableGcmCryptoSuites: true, srtpPreferGcmCryptoSuites: true,
                                             srtpEnableAes128Sha1_32CryptoCipher: false, srtpEnableAes128Sha1_80CryptoCipher: true,
                                             srtpEnableEncryptedRtpHeaderExtensions: false, sframeRequireFrameEncryption: false)
        let constraints = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
        guard let pc = Self.factory.peerConnection(with: cfg, constraints: constraints, delegate: self) else {
            phase = .failed("The video call couldn't start on this device.")
            return
        }
        self.pc = pc
        let audio = Self.factory.audioTrack(with: Self.factory.audioSource(with: constraints), trackId: "a0")
        pc.add(audio, streamIds: ["s0"])
        audioTrack = audio
        let source = Self.factory.videoSource()
        capturer = RTCCameraVideoCapturer(delegate: source)
        startCapture()
        let video = Self.factory.videoTrack(with: source, trackId: "v0")
        if let sender = pc.add(video, streamIds: ["s0"]) {
            let p = sender.parameters
            p.degradationPreference = NSNumber(value: RTCDegradationPreference.balanced.rawValue)
            sender.parameters = p
        }
        localTrack = video
    }

    private func startCapture() {
        guard let capturer else { return }
        let position: AVCaptureDevice.Position = useFront ? .front : .back
        guard let dev = RTCCameraVideoCapturer.captureDevices().first(where: { $0.position == position }) else { return }
        let formats = RTCCameraVideoCapturer.supportedFormats(for: dev)
        let format = formats.first(where: {
            let d = CMVideoFormatDescriptionGetDimensions($0.formatDescription)
            return d.width == 1280 && d.height == 720
        }) ?? formats.last
        guard let format else { return }
        capturer.stopCapture()
        capturer.startCapture(with: dev, format: format, fps: 30)
    }

    // MARK: Signaling

    private func pollLoop() async {
        while !Task.isCancelled, let visitID {
            do {
                let page = try await api.visitSignals(visitID, after: after)
                if page.ended {
                    tearDown(.ended("The visit has ended."))
                    return
                }
                for s in page.signals {
                    after = max(after, s.seq)
                    await handle(s)
                    if phase.isOver { return }
                }
            } catch is CancellationError {
                return
            } catch APIError.http(409, _, let code) where code == "replaced" {
                tearDown(.ended("This visit moved to another of your devices."))
                return
            } catch APIError.http(409, _, let code) where code == "ended" {
                tearDown(.ended("The visit has ended."))
                return
            } catch {
                // Network blips (or the app was in the background): try again.
                try? await Task.sleep(for: .seconds(1))
            }
        }
    }

    private func handle(_ s: VisitSignal) async {
        switch s.type {
        case "joined":
            phase = .connecting
            if isProvider { await offer(restart: offered) }
        case "left":
            phase = .waiting("\(peerName) left the call. Waiting for them to come back…")
        case "restart":
            if isProvider { await offer(restart: true) }
        case "offer" where !isProvider:
            guard let sdp = s.sdp else { return }
            await accept(remote: RTCSessionDescription(type: .offer, sdp: sdp))
        case "answer" where isProvider:
            guard let sdp = s.sdp else { return }
            await accept(remote: RTCSessionDescription(type: .answer, sdp: sdp))
        case "candidate":
            guard let c = s.candidate else { return }
            let cand = RTCIceCandidate(sdp: c.candidate, sdpMLineIndex: c.sdpMLineIndex, sdpMid: c.sdpMid)
            if let pc, pc.remoteDescription != nil { try? await pc.add(cand) } else { pending.append(cand) }
        case "ended":
            tearDown(.ended("The visit has ended."))
        case "replaced":
            tearDown(.ended("This visit moved to another of your devices."))
        default:
            break
        }
    }

    /// The provider's offer; a restart gathers fresh candidates.
    private func offer(restart: Bool) async {
        guard let pc, let visitID else { return }
        let c = RTCMediaConstraints(mandatoryConstraints: restart ? ["IceRestart": "true"] : nil, optionalConstraints: nil)
        do {
            let sdp = try await pc.offer(for: c)
            try await pc.setLocalDescription(sdp)
            offered = true
            try await api.sendVisitSignal(visitID, OutgoingSignal(type: "offer", sdp: sdp.sdp))
        } catch {
            // The next joined, restart or failure brings another try.
        }
    }

    private func accept(remote: RTCSessionDescription) async {
        guard let pc, let visitID else { return }
        do {
            try await pc.setRemoteDescription(remote)
            remoteSDP = remote.sdp
            for c in pending { try? await pc.add(c) }
            pending.removeAll()
            if remote.type == .offer {
                let answer = try await pc.answer(for: RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil))
                try await pc.setLocalDescription(answer)
                try await api.sendVisitSignal(visitID, OutgoingSignal(type: "answer", sdp: answer.sdp))
            }
        } catch {
            if !isProvider { send(OutgoingSignal(type: "restart")) }
        }
    }

    private func send(_ signal: OutgoingSignal) {
        guard let visitID else { return }
        let api = api
        Task { try? await api.sendVisitSignal(visitID, signal) }
    }

    // MARK: Security check

    /// Once connected, the certificate the other phone presented must be the
    /// one its SDP announced; otherwise someone swapped it in the path.
    private func verifyFingerprint() {
        checkTask?.cancel()
        checkTask = Task { [weak self] in
            for _ in 0..<10 {
                guard let self, let pc = self.pc, let sdp = self.remoteSDP else { return }
                let report = await pc.statistics()
                var alg: String?, fp: String?
                if let transport = report.statistics.values.first(where: { $0.type == "transport" }),
                   let id = transport.values["remoteCertificateId"] as? String,
                   let cert = report.statistics[id] {
                    alg = cert.values["fingerprintAlgorithm"] as? String
                    fp = cert.values["fingerprint"] as? String
                }
                switch DTLSFingerprint.check(remoteSDP: sdp, statsAlgorithm: alg, statsFingerprint: fp) {
                case .match:
                    self.verified = true
                    return
                case .mismatch:
                    self.tearDown(.failed("This call couldn't be secured, so it was ended."))
                    return
                case .unknown:
                    try? await Task.sleep(for: .milliseconds(500))
                }
            }
        }
    }

    // MARK: Errors

    static func message(for error: Error) -> String {
        guard case APIError.http(let status, let msg, let code) = error else { return error.localizedDescription }
        switch code {
        case "not_started": return "Your provider will start the visit. You'll get a notification when it begins."
        case "outside_window": return "This visit can be joined from 10 minutes before it starts."
        case "ended": return "This visit has ended."
        case "not_scheduled": return "This appointment is no longer scheduled."
        case "video_unavailable": return "The video service isn't reachable right now. Try again in a minute."
        default: return status == 409 ? msg : "The visit couldn't start: \(msg)"
        }
    }
}

// MARK: RTCPeerConnectionDelegate (called on WebRTC's thread)

extension VisitCall: RTCPeerConnectionDelegate {
    nonisolated func peerConnection(_ pc: RTCPeerConnection, didChange stateChanged: RTCSignalingState) {}
    nonisolated func peerConnection(_ pc: RTCPeerConnection, didAdd stream: RTCMediaStream) {}
    nonisolated func peerConnection(_ pc: RTCPeerConnection, didRemove stream: RTCMediaStream) {}
    nonisolated func peerConnectionShouldNegotiate(_ pc: RTCPeerConnection) {}
    nonisolated func peerConnection(_ pc: RTCPeerConnection, didChange newState: RTCIceConnectionState) {}
    nonisolated func peerConnection(_ pc: RTCPeerConnection, didChange newState: RTCIceGatheringState) {}
    nonisolated func peerConnection(_ pc: RTCPeerConnection, didRemove candidates: [RTCIceCandidate]) {}
    nonisolated func peerConnection(_ pc: RTCPeerConnection, didOpen dataChannel: RTCDataChannel) {}

    nonisolated func peerConnection(_ pc: RTCPeerConnection, didGenerate candidate: RTCIceCandidate) {
        let c = VisitCandidate(candidate: candidate.sdp, sdpMid: candidate.sdpMid, sdpMLineIndex: candidate.sdpMLineIndex)
        Task { @MainActor in self.send(OutgoingSignal(type: "candidate", candidate: c)) }
    }

    nonisolated func peerConnection(_ pc: RTCPeerConnection, didChange newState: RTCPeerConnectionState) {
        Task { @MainActor in self.connectionChanged(newState) }
    }

    nonisolated func peerConnection(_ pc: RTCPeerConnection, didAdd rtpReceiver: RTCRtpReceiver, streams: [RTCMediaStream]) {
        guard let track = rtpReceiver.track as? RTCVideoTrack else { return }
        Task { @MainActor in self.remoteTrack = track }
    }

    private func connectionChanged(_ state: RTCPeerConnectionState) {
        guard !phase.isOver else { return }
        switch state {
        case .connected:
            phase = .connected
            if !verified { verifyFingerprint() }
        case .disconnected:
            phase = .reconnecting
        case .failed:
            phase = .reconnecting
            if isProvider { Task { await offer(restart: true) } } else { send(OutgoingSignal(type: "restart")) }
        default:
            break
        }
    }
}
