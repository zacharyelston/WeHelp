import SwiftUI
import WebRTC

struct VideoView: UIViewRepresentable {
    let track: RTCVideoTrack?

    final class Coordinator { var track: RTCVideoTrack? }
    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> RTCMTLVideoView {
        let v = RTCMTLVideoView()
        v.videoContentMode = .scaleAspectFit
        v.backgroundColor = .black
        return v
    }

    func updateUIView(_ v: RTCMTLVideoView, context: Context) {
        guard context.coordinator.track !== track else { return }
        context.coordinator.track?.remove(v)
        track?.add(v)
        context.coordinator.track = track
    }
}

struct CallView: View {
    let call: Call

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("\(call.room): \(call.status)").font(.headline)
            HStack(spacing: 4) {
                VStack {
                    VideoView(track: call.localTrack).aspectRatio(16 / 9, contentMode: .fit)
                    Text("local").font(.caption2)
                }
                VStack {
                    VideoView(track: call.remoteTrack).aspectRatio(16 / 9, contentMode: .fit)
                    Text("remote").font(.caption2)
                }
            }
            Text(call.summary).font(.system(size: 10, design: .monospaced))
        }
        .padding(8)
        .background(.quaternary, in: RoundedRectangle(cornerRadius: 8))
    }
}

struct ContentView: View {
    @State private var rooms = Settings.rooms.joined(separator: ",")
    @State private var calls: [Call] = []

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                Text("S2: WebRTC via TURN (relay only)").font(.title3.bold())
                HStack {
                    TextField("room (comma for several)", text: $rooms)
                        .textFieldStyle(.roundedBorder)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                    if calls.isEmpty {
                        Button("Join", action: join).buttonStyle(.borderedProminent)
                    } else {
                        Button("Leave", role: .destructive, action: leave).buttonStyle(.bordered)
                    }
                }
                ForEach(calls) { CallView(call: $0) }
            }
            .padding()
        }
        .task {
            if Settings.autojoin { join() }
        }
    }

    private func join() {
        calls = rooms.split(separator: ",").map { Call(room: String($0).trimmingCharacters(in: .whitespaces)) }
        for c in calls { Task { await c.start() } }
    }

    private func leave() {
        calls.forEach { $0.stop() }
        calls = []
    }
}
