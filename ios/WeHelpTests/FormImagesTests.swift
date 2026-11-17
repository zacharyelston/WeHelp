import UIKit
import XCTest
@testable import WeHelp

/// Pictures on display items (#198): what's accepted, how big they get, and
/// how the walker loads them.
final class FormImagesTests: XCTestCase {
    static func png(width: Int, height: Int) -> Data {
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        return UIGraphicsImageRenderer(size: CGSize(width: width, height: height), format: format)
            .image { ctx in UIColor.systemTeal.setFill(); ctx.fill(CGRect(x: 0, y: 0, width: width, height: height)) }
            .pngData()!
    }

    func testOnlyPNGAndJPEG() {
        XCTAssertTrue(FormImages.isPNGOrJPEG(Self.png(width: 4, height: 4)))
        XCTAssertTrue(FormImages.isPNGOrJPEG(Data([0xFF, 0xD8, 0xFF, 0xE0])))
        XCTAssertFalse(FormImages.isPNGOrJPEG(Data("<svg/>".utf8)))
        XCTAssertFalse(FormImages.isPNGOrJPEG(Data("GIF89a".utf8)))
        XCTAssertNil(FormImages.decode(Data("not an image".utf8)))
    }

    func testLargeImagesAreScaledDown() throws {
        let small = try XCTUnwrap(FormImages.decode(Self.png(width: 300, height: 200)))
        XCTAssertEqual(small.size.width * small.scale, 300)
        let big = try XCTUnwrap(FormImages.decode(Self.png(width: 4096, height: 1024), maxPixels: 2048))
        XCTAssertEqual(big.size.width * big.scale, 2048, accuracy: 1)
        XCTAssertEqual(big.size.height * big.scale, 512, accuracy: 1)
    }

    func testMediaIsReadFromADisplayItem() throws {
        let def = try FormDefinition(FormJSON.parse(#"""
        {"format":"wehelp.form.v1","title":"T","sections":[{"id":"s","pages":[{"id":"p","items":[
          {"id":"pic","type":"display","text":"Where it hurts","media":{"type":"image","asset":"body.png","alt":"Front of a body"}},
          {"id":"q","type":"boolean","text":"OK?"}]}]}]}
        """#))
        XCTAssertEqual(def.item("pic")?.media, .init(kind: "image", asset: "body.png", alt: "Front of a body"))
        XCTAssertNil(def.item("q")?.media)
    }
}

private struct FakeAssets: FormAssetSource {
    let data: [String: Data]
    func imageData(named name: String) async throws -> Data {
        guard let d = data[name] else { throw FormsAPIError.notFound }
        return d
    }
}

@MainActor
final class FormSessionImageTests: XCTestCase {
    private func session(_ assets: (any FormAssetSource)?) throws -> FormSession {
        let def = try XCTUnwrap(SampleForms.intake)
        return FormSession(definition: def, sink: InMemoryFormSink(definition: def), assets: assets)
    }

    func testLoadsOnceAndKeepsInMemory() async throws {
        let s = try session(FakeAssets(data: ["a.png": FormImagesTests.png(width: 10, height: 10)]))
        await s.loadImage("a.png")
        XCTAssertNotNil(s.images["a.png"])
        await s.loadImage("a.png") // already loaded: no second fetch needed
        XCTAssertNotNil(s.images["a.png"])
    }

    func testFailuresAreNotShownOrRetried() async throws {
        let s = try session(FakeAssets(data: ["bad.png": Data("nope".utf8)]))
        await s.loadImage("missing.png")
        await s.loadImage("bad.png")
        XCTAssertTrue(s.failedImages.contains("missing.png"))
        XCTAssertTrue(s.failedImages.contains("bad.png"))
        XCTAssertNil(s.images["bad.png"])
    }

    func testNoSourceMeansNoPicture() async throws {
        let s = try session(nil)
        await s.loadImage("a.png")
        XCTAssertTrue(s.failedImages.contains("a.png"))
    }
}
