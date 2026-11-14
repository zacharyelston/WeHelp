import XCTest
@testable import WeHelp

/// The call's security check (#6): the certificate the other phone used in
/// DTLS must be the one its SDP announced.
final class DTLSFingerprintTests: XCTestCase {
    let sdp = """
    v=0\r
    o=- 1 2 IN IP4 127.0.0.1\r
    a=group:BUNDLE 0 1\r
    a=fingerprint:sha-256 F9:08:7A:85:00:0D:FF:01:E7:9D:86:F9:7D:D1:5A:02:72:A1:7E:6A:3C:74:F4:28:F3:63:98:EC:11:22:33:44\r
    a=setup:actpass\r
    """

    func testMatch() {
        XCTAssertEqual(DTLSFingerprint.check(remoteSDP: sdp, statsAlgorithm: "sha-256",
            statsFingerprint: "f9:08:7a:85:00:0d:ff:01:e7:9d:86:f9:7d:d1:5a:02:72:a1:7e:6a:3c:74:f4:28:f3:63:98:ec:11:22:33:44"), .match)
    }

    func testMismatch() {
        XCTAssertEqual(DTLSFingerprint.check(remoteSDP: sdp, statsAlgorithm: "sha-256",
            statsFingerprint: "00:08:7A:85:00:0D:FF:01:E7:9D:86:F9:7D:D1:5A:02:72:A1:7E:6A:3C:74:F4:28:F3:63:98:EC:11:22:33:44"), .mismatch)
        XCTAssertEqual(DTLSFingerprint.check(remoteSDP: sdp, statsAlgorithm: "sha-1",
            statsFingerprint: "F9:08:7A:85:00:0D:FF:01:E7:9D:86:F9:7D:D1:5A:02:72:A1:7E:6A:3C:74:F4:28:F3:63:98:EC:11:22:33:44"), .mismatch)
        XCTAssertEqual(DTLSFingerprint.check(remoteSDP: "v=0\r\n", statsAlgorithm: "sha-256", statsFingerprint: "AA"), .mismatch)
    }

    func testMissingStats() {
        XCTAssertEqual(DTLSFingerprint.check(remoteSDP: sdp, statsAlgorithm: nil, statsFingerprint: nil), .unknown)
        XCTAssertEqual(DTLSFingerprint.check(remoteSDP: sdp, statsAlgorithm: "sha-256", statsFingerprint: ""), .unknown)
    }

    func testJoinWindow() {
        let now = Date()
        let a = Appointment(id: UUID(), providerID: UUID(), patientID: UUID(), startsAt: now.addingTimeInterval(9 * 60),
                            endsAt: now.addingTimeInterval(39 * 60), status: .scheduled, createdAt: now)
        XCTAssertTrue(a.inVisitWindow(now: now))
        XCTAssertFalse(a.inVisitWindow(now: now.addingTimeInterval(-120)))
        XCTAssertTrue(a.inVisitWindow(now: now.addingTimeInterval(39 * 60 + 29 * 60)))
        XCTAssertFalse(a.inVisitWindow(now: now.addingTimeInterval(39 * 60 + 31 * 60)))
    }
}
