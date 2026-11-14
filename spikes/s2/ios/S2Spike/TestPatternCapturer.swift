import CoreVideo
import UIKit
import WebRTC

/// Synthetic video source for simulators (no camera). Draws a moving test
/// pattern with a frame counter into BGRA pixel buffers and hands them to
/// WebRTC as if they came from a camera.
final class TestPatternCapturer: RTCVideoCapturer {
    private let width: Int
    private let height: Int
    private let fps: Int
    private let label: String
    private let queue = DispatchQueue(label: "s2.testpattern")
    private var timer: DispatchSourceTimer?
    private var pool: CVPixelBufferPool?
    private var frame: Int = 0

    init(delegate: RTCVideoCapturerDelegate, width: Int, height: Int, fps: Int, label: String) {
        self.width = width
        self.height = height
        self.fps = fps
        self.label = label
        super.init(delegate: delegate)
        let attrs: [String: Any] = [
            kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32BGRA,
            kCVPixelBufferWidthKey as String: width,
            kCVPixelBufferHeightKey as String: height,
            kCVPixelBufferIOSurfacePropertiesKey as String: [:],
        ]
        CVPixelBufferPoolCreate(nil, nil, attrs as CFDictionary, &pool)
    }

    func start() {
        let t = DispatchSource.makeTimerSource(queue: queue)
        t.schedule(deadline: .now(), repeating: .nanoseconds(1_000_000_000 / fps))
        t.setEventHandler { [weak self] in self?.emit() }
        t.resume()
        timer = t
    }

    func stop() {
        timer?.cancel()
        timer = nil
    }

    private func emit() {
        guard let pool else { return }
        var pb: CVPixelBuffer?
        CVPixelBufferPoolCreatePixelBuffer(nil, pool, &pb)
        guard let pb else { return }
        CVPixelBufferLockBaseAddress(pb, [])
        if let base = CVPixelBufferGetBaseAddress(pb),
           let ctx = CGContext(data: base, width: width, height: height, bitsPerComponent: 8,
                               bytesPerRow: CVPixelBufferGetBytesPerRow(pb),
                               space: CGColorSpaceCreateDeviceRGB(),
                               bitmapInfo: CGImageAlphaInfo.premultipliedFirst.rawValue | CGBitmapInfo.byteOrder32Little.rawValue) {
            draw(ctx)
        }
        CVPixelBufferUnlockBaseAddress(pb, [])
        frame += 1
        let ns = Int64(CACurrentMediaTime() * 1_000_000_000)
        let buf = RTCCVPixelBuffer(pixelBuffer: pb)
        let vf = RTCVideoFrame(buffer: buf, rotation: ._0, timeStampNs: ns)
        delegate?.capturer(self, didCapture: vf)
    }

    private func draw(_ ctx: CGContext) {
        let w = CGFloat(width), h = CGFloat(height)
        // Colour bars that slowly scroll, so the encoder always has motion.
        let bars: [UIColor] = [.white, .yellow, .cyan, .green, .magenta, .red, .blue, .black]
        let bw = w / CGFloat(bars.count)
        let shift = CGFloat(frame * 2).truncatingRemainder(dividingBy: w)
        for (i, c) in bars.enumerated() {
            ctx.setFillColor(c.cgColor)
            var x = CGFloat(i) * bw + shift
            if x >= w { x -= w }
            ctx.fill(CGRect(x: x, y: 0, width: bw + 1, height: h))
            if x + bw > w { ctx.fill(CGRect(x: x - w, y: 0, width: bw + 1, height: h)) }
        }
        // A bouncing box.
        let t = CGFloat(frame) / CGFloat(fps)
        let bx = (sin(t * 1.3) * 0.5 + 0.5) * (w - 120)
        let by = (cos(t * 0.9) * 0.5 + 0.5) * (h - 120)
        ctx.setFillColor(UIColor.orange.cgColor)
        ctx.fill(CGRect(x: bx, y: by, width: 120, height: 120))
        // Label: CGContext is bottom-up here, so flip for UIKit text drawing.
        ctx.saveGState()
        ctx.translateBy(x: 0, y: h)
        ctx.scaleBy(x: 1, y: -1)
        UIGraphicsPushContext(ctx)
        let text = "\(label)  #\(frame)  \(Date().formatted(date: .omitted, time: .standard))" as NSString
        let attrs: [NSAttributedString.Key: Any] = [
            .font: UIFont.monospacedSystemFont(ofSize: h / 16, weight: .bold),
            .foregroundColor: UIColor.white,
            .backgroundColor: UIColor.black.withAlphaComponent(0.7),
        ]
        text.draw(at: CGPoint(x: 16, y: h - h / 16 - 24), withAttributes: attrs)
        UIGraphicsPopContext()
        ctx.restoreGState()
    }
}
