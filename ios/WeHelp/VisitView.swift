import SwiftUI
@preconcurrency import WebRTC

/// The video visit screen (#6): the other person full screen, you in a
/// corner, and mute, camera, flip and leave. Works in every iPad orientation;
/// with a keyboard, ⌘D mutes and ⌘E turns the camera off.
struct VisitView: View {
    @Environment(\.dismiss) private var dismiss
    @State private var call: VisitCall
    @State private var confirmEnd = false

    init(appointment: Appointment, peerName: String, api: APIClient, start: Bool) {
        _call = State(initialValue: VisitCall(appointment: appointment, peerName: peerName, api: api, start: start))
    }

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            if let remote = call.remoteTrack, call.phase == .connected || call.phase == .reconnecting {
                VideoTrackView(track: remote, mirrored: false, fill: true).ignoresSafeArea()
            } else {
                status
            }
            VStack {
                HStack {
                    header
                    Spacer()
                    if let local = call.localTrack, !call.cameraOff, !call.phase.isOver {
                        VideoTrackView(track: local, mirrored: true, fill: true)
                            .frame(width: 120, height: 160)
                            .clipShape(RoundedRectangle(cornerRadius: 12))
                            .overlay(RoundedRectangle(cornerRadius: 12).stroke(.white.opacity(0.4)))
                            .accessibilityLabel("Your camera")
                    }
                }
                .padding()
                Spacer()
                controls.padding(.bottom, 24)
            }
        }
        .preferredColorScheme(.dark)
        .statusBarHidden()
        .task { await call.begin() }
        .confirmationDialog("End the visit for both of you?", isPresented: $confirmEnd, titleVisibility: .visible) {
            Button("End visit", role: .destructive) { Task { await call.endForBoth() } }
            Button("Just leave (you can rejoin)") { Task { await call.leave() } }
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(call.peerName).font(.headline).foregroundStyle(.white)
            HStack(spacing: 6) {
                if call.verified { Image(systemName: "lock.fill").accessibilityLabel("Encrypted end to end") }
                Text(phaseLabel)
            }
            .font(.caption)
            .foregroundStyle(.white.opacity(0.8))
        }
        .privacySensitive()
    }

    private var phaseLabel: String {
        switch call.phase {
        case .starting: "Starting…"
        case .waiting: "Waiting"
        case .connecting: "Connecting…"
        case .connected: call.verified ? "Encrypted end to end" : "Connected"
        case .reconnecting: "Reconnecting…"
        case .ended: "Ended"
        case .failed: "Couldn't connect"
        }
    }

    @ViewBuilder
    private var status: some View {
        VStack(spacing: 16) {
            switch call.phase {
            case .waiting(let text):
                ProgressView().tint(.white)
                Text(text)
            case .starting, .connecting, .reconnecting:
                ProgressView().tint(.white)
                Text(call.phase == .reconnecting ? "Reconnecting…" : "Connecting to \(call.peerName)…")
            case .ended(let text), .failed(let text):
                Image(systemName: call.phase == .ended(text) ? "video.slash" : "exclamationmark.triangle")
                    .font(.largeTitle)
                Text(text).multilineTextAlignment(.center)
                Button("Close") { dismiss() }.buttonStyle(.borderedProminent)
            case .connected:
                ProgressView().tint(.white)
            }
        }
        .foregroundStyle(.white)
        .padding(32)
    }

    @ViewBuilder
    private var controls: some View {
        if !call.phase.isOver {
            HStack(spacing: 28) {
                RoundButton(system: call.muted ? "mic.slash.fill" : "mic.fill", label: call.muted ? "Unmute" : "Mute",
                            active: call.muted) { call.toggleMute() }
                    .keyboardShortcut("d", modifiers: .command)
                RoundButton(system: call.cameraOff ? "video.slash.fill" : "video.fill",
                            label: call.cameraOff ? "Turn camera on" : "Turn camera off", active: call.cameraOff) { call.toggleCamera() }
                    .keyboardShortcut("e", modifiers: .command)
                RoundButton(system: "arrow.triangle.2.circlepath.camera", label: "Switch camera", active: false) { call.flipCamera() }
                RoundButton(system: "phone.down.fill", label: call.isProvider ? "End visit" : "Leave", active: false, tint: .red) {
                    if call.isProvider { confirmEnd = true } else { Task { await call.leave() } }
                }
            }
        }
    }
}

private struct RoundButton: View {
    let system: String
    let label: String
    let active: Bool
    var tint: Color = .white.opacity(0.25)
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: system)
                .font(.title2)
                .frame(width: 60, height: 60)
                .background(active ? Color.white : tint, in: Circle())
                .foregroundStyle(active ? Color.black : Color.white)
        }
        .accessibilityLabel(label)
    }
}

/// Renders a WebRTC video track with Metal.
struct VideoTrackView: UIViewRepresentable {
    let track: RTCVideoTrack
    let mirrored: Bool
    let fill: Bool

    final class Coordinator { var track: RTCVideoTrack? }
    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> RTCMTLVideoView {
        let v = RTCMTLVideoView()
        v.videoContentMode = fill ? .scaleAspectFill : .scaleAspectFit
        v.clipsToBounds = true
        if mirrored { v.transform = CGAffineTransform(scaleX: -1, y: 1) }
        return v
    }

    func updateUIView(_ v: RTCMTLVideoView, context: Context) {
        guard context.coordinator.track !== track else { return }
        context.coordinator.track?.remove(v)
        track.add(v)
        context.coordinator.track = track
    }

    static func dismantleUIView(_ v: RTCMTLVideoView, coordinator: Coordinator) {
        coordinator.track?.remove(v)
    }
}

/// The Join or Start button on an appointment, or why there isn't one.
struct VisitButton: View {
    @Environment(AppState.self) private var appState
    let appointment: Appointment
    let peerName: String
    @State private var presenting: Bool? // true = start, false = join

    var body: some View {
        Group {
            if appState.isStaff {
                Button("Start video visit", systemImage: "video") { presenting = true }
                    .buttonStyle(.borderedProminent)
            } else if appointment.visitOpen {
                Button("Join video visit", systemImage: "video") { presenting = false }
                    .buttonStyle(.borderedProminent)
            } else {
                Label("Your provider will start the video visit", systemImage: "video")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .fullScreenCover(isPresented: Binding(get: { presenting != nil }, set: { if !$0 { presenting = nil } })) {
            VisitView(appointment: appointment, peerName: peerName, api: appState.api, start: presenting ?? false)
        }
    }
}
