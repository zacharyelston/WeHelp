# Spike S2: 1:1 WebRTC through pion/turn (throwaway)

Write-up and results: [docs/spikes/S2.md](../../docs/spikes/S2.md).
This code is not production code and is not built by CI.

- `server/`: its own Go module. One binary runs a pion/turn server (UDP and
  TCP on 3478, relay ports 49160-49999, TURN REST long-term credentials) and
  an HTTP long-poll signaling endpoint. It logs allocations, per-allocation
  relayed bytes (classified as STUN, DTLS or SRTP by first byte, RFC 7983),
  process CPU and memory, and the stats each client reports.
- `ios/`: SwiftUI app (xcodegen), WebRTC from `stasel/WebRTC` 157.0.0,
  `iceTransportPolicy = .relay`. On a simulator it sends a generated test
  pattern; on a device it uses the front camera and the microphone.
- `run.sh`, `sample.sh`: launch a call on two simulators and sample CPU/RSS.

## Rerun

```sh
LAN=$(ipconfig getifaddr en0)

# 1. Server
cd spikes/s2/server && go build -o /tmp/s2server . && /tmp/s2server -ip $LAN -http $LAN:8733

# 2. App (another shell)
cd spikes/s2/ios && xcodegen
xcodebuild build -project S2Spike.xcodeproj -scheme S2Spike \
  -destination 'platform=iOS Simulator,name=iPhone 18 Pro' -derivedDataPath /tmp/s2dd
APP=/tmp/s2dd/Build/Products/Debug-iphonesimulator/S2Spike.app
xcrun simctl install <ipad-udid> $APP; xcrun simctl install <iphone-udid> $APP

# 3. One call for 10 minutes, then two at once (each simulator runs both)
IPAD=<ipad-udid> IPHONE=<iphone-udid> spikes/s2/run.sh c1 600 one-call.csv
IPAD=<ipad-udid> IPHONE=<iphone-udid> spikes/s2/run.sh c2a,c2b 600 two-calls.csv
```

If the LAN address is not 192.168.1.20, pass it to the app too:
`xcrun simctl launch <udid> com.zacelston.wehelp.spike2 -server http://$LAN:8733 -rooms c1 -autojoin YES`.
Other launch arguments: `-width`, `-height`, `-fps`.

On a device, open `S2Spike.xcodeproj`, pick team WZYFJ8TSWD, run on two
phones on the same Wi-Fi as the laptop, type the same room on both, tap Join.
