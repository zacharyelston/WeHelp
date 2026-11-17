import XCTest
@testable import WeHelp

/// The Swift walker's evaluator is held to the shared wehelp.form.v1 vectors
/// in testdata/forms/ (bundled into this test target), like the Go reference
/// and web/forms-eval.js. See testdata/forms/README.md for the format.
final class FormEvaluatorTests: XCTestCase {
    private var root: URL {
        get throws {
            let b = Bundle(for: FormEvaluatorTests.self)
            return try XCTUnwrap(b.url(forResource: "forms", withExtension: nil), "testdata/forms isn't bundled")
        }
    }

    private func json(_ url: URL) throws -> FormJSON { try FormJSON.parse(Data(contentsOf: url)) }

    private func object(_ v: FormJSON?) -> [String: FormJSON] {
        Dictionary(uniqueKeysWithValues: v?.objectPairs ?? [])
    }

    func testValidDefinitionsCompile() throws {
        let dir = try root.appendingPathComponent("valid")
        let files = try FileManager.default.contentsOfDirectory(atPath: dir.path).filter { $0.hasSuffix(".json") }
        XCTAssertFalse(files.isEmpty)
        for f in files {
            XCTAssertNoThrow(try FormDefinition(json: Data(contentsOf: dir.appendingPathComponent(f))), f)
        }
    }

    func testSharedInvalidCases() throws {
        let dir = try root.appendingPathComponent("invalid")
        let cases = try json(dir.appendingPathComponent("cases.json"))["cases"]?.array ?? []
        var shared = 0
        for c in cases where c["shared"]?.bool == true {
            shared += 1
            let file = c["file"]?.string ?? ""
            let want = (c["path"]?.string ?? "", c["kind"]?.string ?? "")
            do {
                _ = try FormDefinition(json: Data(contentsOf: dir.appendingPathComponent(file)))
                XCTFail("\(file): compiled, want \(want.1) at \(want.0)")
            } catch let e as FormError {
                XCTAssertEqual(e.kind, want.1, "\(file): \(e)")
                XCTAssertEqual(e.path, want.0, "\(file): \(e)")
            }
        }
        XCTAssertGreaterThan(shared, 0)
    }

    func testVectors() throws {
        let dir = try root.appendingPathComponent("vectors")
        let files = try FileManager.default.contentsOfDirectory(atPath: dir.path).filter { $0.hasSuffix(".json") }.sorted()
        XCTAssertFalse(files.isEmpty)
        var count = 0
        for f in files {
            let vec = try json(dir.appendingPathComponent(f))
            let defURL = try root.appendingPathComponent(vec["definition"]?.string ?? "")
            let def = try FormDefinition(json: Data(contentsOf: defURL))
            for c in vec["cases"]?.array ?? [] {
                count += 1
                let name = "\(f): \(c["name"]?.string ?? "?")"
                let answers = object(c["answers"])
                let res = def.evaluate(answers)

                for (id, want) in c["visible"]?.objectPairs ?? [] {
                    XCTAssertEqual(res.visible[id], want.bool, "\(name): visible \(id)")
                }
                for (id, want) in c["next"]?.objectPairs ?? [] {
                    XCTAssertEqual(res.next[id], want.string, "\(name): next \(id)")
                }
                if let path = c["path"]?.array {
                    XCTAssertEqual(res.path, path.compactMap(\.string), "\(name): path")
                }
                if let scores = c["scores"]?.objectPairs {
                    for (id, want) in scores {
                        let got = res.scores[id]
                        XCTAssertNotNil(got, "\(name): score \(id) missing")
                        XCTAssertEqual(got?.value, want["value"]?.string, "\(name): score \(id) value")
                        XCTAssertEqual(got?.band, want["band"]?.string, "\(name): score \(id) band")
                        XCTAssertEqual(got?.reason, want["reason"]?.string, "\(name): score \(id) reason")
                    }
                }
                XCTAssertEqual(res.error?.kind, c["error"]?.string, "\(name): error")
                if let flags = c["flags"]?.array {
                    XCTAssertEqual(res.flags, flags.compactMap(\.string), "\(name): flags")
                }

                if let check = c["check"] {
                    let today = c["today"]?.string ?? "2026-11-16"
                    let got = def.checkAnswers(answers, today: today)
                    let wantAnswers = object(check["answers"])
                    XCTAssertEqual(Set(got.answers.keys), Set(wantAnswers.keys), "\(name): check answers")
                    for (k, v) in wantAnswers { XCTAssertEqual(got.answers[k], v, "\(name): check answer \(k)") }
                    let wantErrors = (check["errors"]?.array ?? []).map { "\($0["item"]?.string ?? ""):\($0["code"]?.string ?? "")" }
                    XCTAssertEqual(got.errors.map { "\($0.item):\($0.code)" }, wantErrors, "\(name): check errors")
                }
            }
        }
        XCTAssertGreaterThan(count, 0)
    }

    func testDecimalText() {
        XCTAssertEqual(FormRational.parse("38.5")?.text, "38.5")
        XCTAssertEqual(FormRational.parse("-0.000001")?.text, "-0.000001")
        XCTAssertEqual(FormRational.parse("0")?.text, "0")
        XCTAssertEqual((FormRational(1) / FormRational(3)).text, "0.333333")
        XCTAssertEqual(FormRational.parse("2.5")?.roundedHalfEven(0).text, "2")
        XCTAssertEqual(FormRational.parse("3.5")?.roundedHalfEven(0).text, "4")
        XCTAssertEqual(FormRational.parse("-2.5")?.roundedHalfEven(0).text, "-2")
        XCTAssertNil(FormRational.parse("1e2"))
        XCTAssertNil(FormRational.parse("01"))
        XCTAssertNil(FormRational.parse("1234567890123"))
        XCTAssertNil(FormRational.parse("1.1234567"))
    }

    func testBigIntArithmetic() {
        let a = FormBigInt(decimal: "340282366920938463463374607431768211456")! // 2^128
        XCTAssertEqual(a.bitLength, 129)
        XCTAssertEqual((a - .one).bitLength, 128)
        XCTAssertEqual((a * a).quotientAndRemainder(dividingBy: a).0, a)
        XCTAssertEqual(FormBigInt(decimal: "-123456789012345678901234567890")!.description, "-123456789012345678901234567890")
        XCTAssertEqual(FormBigInt.gcd(FormBigInt(84), FormBigInt(-36)), FormBigInt(12))
        let (q, m) = FormBigInt(-7).floorDivMod(FormBigInt(2))
        XCTAssertEqual(q, FormBigInt(-4))
        XCTAssertEqual(m, FormBigInt(1))
    }

    func testJSONKeepsNumberText() throws {
        let v = try FormJSON.parse(#"{"a": 38.50, "b": [1, -0.25], "c": "xé"}"#)
        XCTAssertEqual(v["a"], .number("38.50"))
        XCTAssertEqual(v["b"]?.array?[1], .number("-0.25"))
        XCTAssertEqual(v["c"]?.string, "xé")
        XCTAssertThrowsError(try FormJSON.parse(#"{"a":1,"a":2}"#))
    }
}
