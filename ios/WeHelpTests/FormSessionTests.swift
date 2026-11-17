import XCTest
@testable import WeHelp

/// The walker's page logic on the bundled sample form: branching, required
/// checks before Next, and the score shown after submit.
@MainActor
final class FormSessionTests: XCTestCase {
    private func session() throws -> FormSession {
        let def = try XCTUnwrap(SampleForms.intake, "SampleIntake.json isn't bundled or doesn't compile")
        return FormSession(definition: def, sink: InMemoryFormSink(definition: def))
    }

    func testNextNeedsRequiredAnswers() async throws {
        let s = try session()
        XCTAssertEqual(s.pageID, "p_about")
        let moved = await s.next()
        XCTAssertFalse(moved)
        XCTAssertEqual(s.pageID, "p_about")
        XCTAssertNotNil(s.problems["consent"])
        XCTAssertNotNil(s.problems["age"])
    }

    func testOutOfRangeBlocksNext() async throws {
        let s = try session()
        s.set("consent", .bool(true))
        s.set("age", .number("200"))
        s.set("visit", .string("other"))
        let moved = await s.next()
        XCTAssertFalse(moved)
        XCTAssertNotNil(s.problems["age"])
    }

    func testSleepBranchAndScore() async throws {
        let s = try session()
        s.set("consent", .bool(true))
        s.set("age", .number("40"))
        s.set("visit", .string("sleep"))
        XCTAssertEqual(s.pages, ["p_about", "p_sleep", "p_notes"])
        let moved1 = await s.next()
        XCTAssertTrue(moved1)
        XCTAssertEqual(s.pageID, "p_sleep")
        s.set("sleep_hours", .number("6.5"))
        s.set("wake", .string("most"))
        s.set("rested", .string("somewhat"))
        let moved2 = await s.next()
        XCTAssertTrue(moved2)
        XCTAssertTrue(s.isLast)
        await s.submit()
        XCTAssertTrue(s.submitted)
        let shown = s.shownScores
        XCTAssertEqual(shown.first?.1.value, "3")
        XCTAssertEqual(shown.first?.1.band, "moderate")
    }

    func testOtherBranchSkipsSleep() throws {
        let s = try session()
        s.set("consent", .bool(true))
        s.set("visit", .string("digestion"))
        XCTAssertEqual(s.pages, ["p_about", "p_general", "p_notes"])
    }

    func testNoConsentEndsAfterFirstPage() throws {
        let s = try session()
        s.set("consent", .bool(false))
        XCTAssertEqual(s.pages, ["p_about"])
        XCTAssertTrue(s.isLast)
    }

    func testSliderText() {
        XCTAssertEqual(FormItemView.numberText(6.5, integer: false, step: 0.5), "6.5")
        XCTAssertEqual(FormItemView.numberText(7.0, integer: false, step: 0.5), "7")
        XCTAssertEqual(FormItemView.numberText(3.0000001, integer: true, step: 1), "3")
    }
}
