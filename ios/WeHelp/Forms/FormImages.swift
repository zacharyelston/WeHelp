import UIKit

/// Where a walker gets the pictures on display items (#198): the Back Office
/// for an assigned form; nothing for the bundled sample.
protocol FormAssetSource: Sendable {
    func imageData(named name: String) async throws -> Data
}

/// Images on one published version, fetched through the pinned connection.
struct APIFormAssets: FormAssetSource {
    let forms: FormsAPI
    let versionID: String
    func imageData(named name: String) async throws -> Data {
        try await forms.asset(versionID: versionID, name: name)
    }
}

enum FormImages {
    /// The Back Office only stores PNG and JPEG (#195); check the bytes, not
    /// a header, before handing them to the decoder.
    static func isPNGOrJPEG(_ data: Data) -> Bool {
        let png: [UInt8] = [0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]
        let jpeg: [UInt8] = [0xFF, 0xD8, 0xFF]
        return data.starts(with: png) || data.starts(with: jpeg)
    }

    /// Decodes to an image no bigger than `maxPixels` on its long side, so a
    /// large upload can't use a lot of memory on the phone.
    static func decode(_ data: Data, maxPixels: CGFloat = 2048) -> UIImage? {
        guard isPNGOrJPEG(data), let image = UIImage(data: data) else { return nil }
        let size = image.size
        let longSide = max(size.width, size.height) * image.scale
        guard longSide > maxPixels else { return image }
        let factor = maxPixels / longSide
        let target = CGSize(width: size.width * image.scale * factor, height: size.height * image.scale * factor)
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        return UIGraphicsImageRenderer(size: target, format: format).image { _ in
            image.draw(in: CGRect(origin: .zero, size: target))
        }
    }
}
