import XCTest
@testable import WeHelp

/// The forms API's error mapping (docs/MOBILE_CLIENT.md, #197) and how the
/// walker shows each error.
final class FormsAPITests: XCTestCase {
    private func err(_ status: Int, _ json: String) -> FormsAPIError {
        FormsAPIError.from(status: status, data: Data(json.utf8))
    }

    func testErrorCodes() {
        XCTAssertEqual(err(403, #"{"error":"off","code":"feature_off"}"#), .featureOff)
        XCTAssertEqual(err(409, #"{"error":"closed","code":"window_closed"}"#), .windowClosed)
        XCTAssertEqual(err(409, #"{"error":"x","code":"already_submitted"}"#), .alreadySubmitted)
        XCTAssertEqual(err(409, #"{"error":"x","code":"not_open"}"#), .notOpen)
        XCTAssertEqual(err(409, #"{"error":"stale","code":"stale_revision","revision":7}"#), .staleRevision(7))
        XCTAssertEqual(err(404, #"{"error":"no"}"#), .notFound)
        XCTAssertEqual(err(500, #"{"error":"boom"}"#), .server(500, "boom"))
        XCTAssertEqual(err(502, "not json"), .server(502, "Server error 502"))
    }

    func testInvalidAnswers() {
        let e = err(422, #"{"error":"bad","code":"invalid_answers","errors":[{"item":"age","page_id":"p_about","code":"out_of_range","message":"above the maximum 120"}]}"#)
        XCTAssertEqual(e, .invalidAnswers([.init(item: "age", pageID: "p_about", code: "out_of_range", message: "above the maximum 120")]))
    }

    func testErrorFieldsKeepCodeWithOtherFields() {
        let (msg, code) = APIClient.errorFields(Data(#"{"error":"stale","code":"stale_revision","revision":3}"#.utf8), 409)
        XCTAssertEqual(msg, "stale")
        XCTAssertEqual(code, "stale_revision")
    }
}

/// A sink that fails the way the Back Office can.
private struct FailingSink: FormSink {
    let pageError: (any Error)?
    let submitError: (any Error)?
    func savePage(_ pageID: String, answers: [String: FormJSON]) async throws { if let pageError { throw pageError } }
    func submit(answers: [String: FormJSON]) async throws -> [String: FormResult.ScoreOut]? {
        if let submitError { throw submitError }
        return [:]
    }
}

@MainActor
final class FormSessionServerErrorTests: XCTestCase {
    private func session(_ sink: FormSink) throws -> FormSession {
        let def = try XCTUnwrap(SampleForms.intake)
        let s = FormSession(definition: def, sink: sink)
        s.set("consent", .bool(true))
        s.set("age", .number("40"))
        s.set("visit", .string("other"))
        return s
    }

    func testServerProblemsShowOnTheirItems() async throws {
        let problem = FormsAPIError.Problem(item: "age", pageID: nil, code: "out_of_range", message: "above the maximum 120")
        let s = try session(FailingSink(pageError: FormsAPIError.invalidAnswers([problem]), submitError: nil))
        let moved = await s.next()
        XCTAssertFalse(moved)
        XCTAssertEqual(s.pageID, "p_about")
        XCTAssertNotNil(s.problems["age"])
    }

    func testWindowClosedStopsTheWalker() async throws {
        let s = try session(FailingSink(pageError: FormsAPIError.windowClosed, submitError: nil))
        let moved = await s.next()
        XCTAssertFalse(moved)
        XCTAssertTrue(s.closed)
        XCTAssertEqual(s.saveError, "This form has closed. Your practice can send it again if needed.")
    }

    func testSubmitProblemsJumpToTheirPage() async throws {
        let problem = FormsAPIError.Problem(item: "consent", pageID: "p_about", code: "required", message: "an answer is required")
        let s = try session(FailingSink(pageError: nil, submitError: FormsAPIError.invalidAnswers([problem])))
        _ = await s.next() // p_general
        _ = await s.next() // p_notes, the last page
        XCTAssertTrue(s.isLast)
        await s.submit()
        XCTAssertFalse(s.submitted)
        XCTAssertEqual(s.pageID, "p_about")
        XCTAssertNotNil(s.problems["consent"])
    }
}
