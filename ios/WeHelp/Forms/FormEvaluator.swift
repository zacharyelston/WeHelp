import Foundation

// The wehelp.form.v1 evaluator for the patient walker (#198). It mirrors
// internal/forms (the Go reference) and web/forms-eval.js, and is held to the
// same shared vectors in testdata/forms/ (WeHelpTests/FormEvaluatorTests).
// The Back Office checks every save and submit; this drives what the patient
// sees: visibility, branching and live scores.
//
// Rules and scores are a closed JSON expression tree: nothing is evaluated as
// code. Numbers are exact rationals (FormRational), read from the JSON text
// as written, never through a Double.

/// A definition problem: where (a JSON path like "sections[0].pages[1].id"),
/// what kind (json, schema, duplicate, reference, order, type, operator,
/// depth, nodes, items, range, overflow) and a message.
struct FormError: Error, Equatable, Sendable, CustomStringConvertible {
    let path: String
    let kind: String
    let message: String
    var description: String { path.isEmpty ? message : "\(path): \(message)" }
}

enum FormLimits {
    static let maxBytes = 200_000
    static let maxItems = 500
    static let maxDepth = 32
    static let maxNodes = 4096
    static let defaultMaxLength = 4000
}

/// A compiled, validated form definition. Immutable and safe to share.
final class FormDefinition: @unchecked Sendable {
    static let format = "wehelp.form.v1"
    static let end = "end"

    struct Option: Sendable { let value: String; let score: FormRational?; let raw: FormJSON
        var text: String { raw["text"]?.string ?? value }
    }

    struct Item: Sendable {
        let id: String
        let type: String
        let page: Int      // index into pages
        let index: Int     // position on its page
        let seq: Int       // document order across the form
        let required: Bool
        let multiple: Bool
        let options: [Option]
        let min: FormRational?
        let max: FormRational?
        let minDate: String
        let maxDate: String
        let maxLength: Int
        var enableWhen: FormExpr?
        let raw: FormJSON

        var text: String { raw["text"]?.string ?? "" }
        var control: String? { raw["control"]?.string }
        var unit: String? { raw["unit"]?.string }
        var minLabel: String? { raw["minLabel"]?.string }
        var maxLabel: String? { raw["maxLabel"]?.string }
        var step: FormRational? { if case let .number(t)? = raw["step"] { return FormRational.parse(t) }; return nil }
        /// A display item's picture or video: an asset of this form version.
        var media: Media? {
            guard let m = raw["media"], let asset = m["asset"]?.string else { return nil }
            return Media(kind: m["type"]?.string ?? "image", asset: asset, alt: m["alt"]?.string ?? "")
        }
        struct Media: Sendable, Equatable { let kind: String; let asset: String; let alt: String }
        var position: (Int, Int) { (page, index) }
    }

    struct Page: Sendable {
        let id: String
        let index: Int     // across the form
        let section: Int
        var items: [Int]   // indexes into items
        var enableWhen: FormExpr?
        let raw: FormJSON
        var title: String? { raw["title"]?.string }
    }

    struct Branch: Sendable { var when: FormExpr?; let section: String; let rawWhen: FormJSON?; let hasWhen: Bool }

    struct Section: Sendable {
        let id: String
        let index: Int
        var pages: [Int]   // indexes into pages
        var next: [Branch]
        let raw: FormJSON
        var title: String? { raw["title"]?.string }
    }

    /// A staff-side flag (#199), raised when its rule holds over the whole
    /// submitted form. Never shown to the patient (docs/MOBILE_CLIENT.md §16).
    struct Flag: Sendable { let id: String; let text: String?; let raw: FormJSON?; var when: FormExpr? }

    struct Missing: Sendable { let policy: String; let minAnswered: Int; let places: Int }
    struct Band: Sendable { let min: FormRational; let id: String; let text: String? }

    struct Score: Sendable {
        let id: String
        let index: Int
        var expr: FormExpr = .bool(false)
        var maxDep: (Int, Int) = noDeps
        let missing: Missing
        let bands: [Band]
        let raw: FormJSON
        var text: String? { raw["text"]?.string }
        var showScore: Bool { raw["showScore"]?.bool ?? false }
    }

    let raw: FormJSON
    private(set) var sections: [Section] = []
    private(set) var pages: [Page] = []
    private(set) var items: [Item] = []
    private(set) var scores: [Score] = []
    private(set) var flags: [Flag] = []
    private(set) var sectionIndex: [String: Int] = [:]
    private(set) var pageIndex: [String: Int] = [:]
    private(set) var itemIndex: [String: Int] = [:]
    private(set) var scoreIndex: [String: Int] = [:]
    private var ids: [String: String] = [:]
    private var nodes = 0

    var title: String { raw["title"]?.string ?? "" }
    var description: String? { raw["description"]?.string }

    func item(_ id: String) -> Item? { itemIndex[id].map { items[$0] } }

    /// Parses and validates JSON text. Errors carry the shared vectors' path
    /// and kind for ids, references, ordering, types, operators, number
    /// literals and caps. Field-level checks are the Back Office's.
    convenience init(json data: Data) throws {
        guard data.count <= FormLimits.maxBytes else {
            throw FormError(path: "", kind: "json", message: "definition is larger than \(FormLimits.maxBytes) bytes")
        }
        let doc: FormJSON
        do { doc = try FormJSON.parse(data) } catch {
            throw FormError(path: "", kind: "json", message: "not valid JSON")
        }
        try self.init(doc)
    }

    init(_ def: FormJSON) throws {
        raw = def
        try compile()
    }

    // MARK: compile

    private static func fail(_ path: String, _ kind: String, _ msg: String) -> FormError { FormError(path: path, kind: kind, message: msg) }
    private static func join(_ path: String, _ key: String) -> String { path.isEmpty ? key : path + "." + key }
    private static func idx(_ path: String, _ i: Int) -> String { "\(path)[\(i)]" }

    private static func validID(_ s: String) -> Bool {
        let u = Array(s.utf8)
        guard (1...64).contains(u.count), let f = u.first,
              (0x41...0x5A).contains(f) || (0x61...0x7A).contains(f) else { return false }
        return u.allSatisfy { c in
            (0x41...0x5A).contains(c) || (0x61...0x7A).contains(c) || (0x30...0x39).contains(c)
                || c == UInt8(ascii: "_") || c == UInt8(ascii: ".") || c == UInt8(ascii: "-")
        }
    }

    private func declare(_ path: String, _ v: FormJSON?) throws -> String {
        guard let id = v?.string, Self.validID(id) else { throw Self.fail(path, "schema", "bad id") }
        if id == Self.end { throw Self.fail(path, "schema", "id \"end\" is reserved") }
        if let first = ids[id] { throw Self.fail(path, "duplicate", "duplicate id \"\(id)\" (first at \(first))") }
        ids[id] = path
        return id
    }

    private func decimal(_ path: String, _ v: FormJSON?) throws -> FormRational {
        guard case let .number(t)? = v else { throw Self.fail(path, "schema", "bad number") }
        guard let r = FormRational.parse(t) else { throw Self.fail(path, "range", "bad number") }
        return r
    }

    private func nonEmptyArray(_ path: String, _ v: FormJSON?) throws -> [FormJSON] {
        guard let a = v?.array, !a.isEmpty else { throw Self.fail(path, "schema", "must be a non-empty array") }
        return a
    }

    private func compile() throws {
        guard raw.isObject else { throw Self.fail("", "schema", "definition must be a JSON object") }
        guard raw["format"]?.string == Self.format else { throw Self.fail("format", "schema", "must be \"\(Self.format)\"") }
        let secs = try nonEmptyArray("sections", raw["sections"])

        for (i, sd) in secs.enumerated() {
            let sp = Self.idx("sections", i)
            guard sd.isObject else { throw Self.fail(sp, "schema", "must be an object") }
            let sid = try declare(Self.join(sp, "id"), sd["id"])
            var s = Section(id: sid, index: i, pages: [], next: [], raw: sd)
            sectionIndex[sid] = i
            for (j, pd) in try nonEmptyArray(Self.join(sp, "pages"), sd["pages"]).enumerated() {
                let pp = Self.idx(Self.join(sp, "pages"), j)
                guard pd.isObject else { throw Self.fail(pp, "schema", "must be an object") }
                let pid = try declare(Self.join(pp, "id"), pd["id"])
                var pg = Page(id: pid, index: pages.count, section: i, items: [], enableWhen: nil, raw: pd)
                pageIndex[pid] = pg.index
                for (k, idf) in try nonEmptyArray(Self.join(pp, "items"), pd["items"]).enumerated() {
                    let ip = Self.idx(Self.join(pp, "items"), k)
                    if items.count >= FormLimits.maxItems { throw Self.fail(ip, "items", "more than \(FormLimits.maxItems) items") }
                    guard idf.isObject else { throw Self.fail(ip, "schema", "must be an object") }
                    let iid = try declare(Self.join(ip, "id"), idf["id"])
                    let type = idf["type"]?.string ?? ""
                    var options: [Option] = []
                    var minR: FormRational?, maxR: FormRational?
                    if type == "choice" {
                        var seen = Set<String>()
                        for (oi, od) in try nonEmptyArray(Self.join(ip, "options"), idf["options"]).enumerated() {
                            let op = Self.idx(Self.join(ip, "options"), oi)
                            guard od.isObject, let value = od["value"]?.string, !value.isEmpty else {
                                throw Self.fail(Self.join(op, "value"), "schema", "required")
                            }
                            guard seen.insert(value).inserted else {
                                throw Self.fail(Self.join(ip, "options"), "duplicate", "duplicate value \"\(value)\"")
                            }
                            let score = od["score"] != nil ? try decimal(Self.join(op, "score"), od["score"]) : nil
                            options.append(Option(value: value, score: score, raw: od))
                        }
                    }
                    if type == "integer" || type == "decimal" {
                        if idf["min"] != nil { minR = try decimal(Self.join(ip, "min"), idf["min"]) }
                        if idf["max"] != nil { maxR = try decimal(Self.join(ip, "max"), idf["max"]) }
                    }
                    let isDate = type == "date"
                    let it = Item(
                        id: iid, type: type, page: pg.index, index: k, seq: items.count,
                        required: idf["required"]?.bool == true, multiple: idf["multiple"]?.bool == true,
                        options: options, min: minR, max: maxR,
                        minDate: isDate ? (idf["min"]?.string ?? "") : "",
                        maxDate: isDate ? (idf["max"]?.string ?? "") : "",
                        maxLength: idf["maxLength"]?.intValue ?? FormLimits.defaultMaxLength,
                        enableWhen: nil, raw: idf)
                    itemIndex[iid] = items.count
                    pg.items.append(items.count)
                    items.append(it)
                }
                s.pages.append(pg.index)
                pages.append(pg)
            }
            for bd in sd["next"]?.array ?? [] {
                s.next.append(Branch(when: nil, section: bd["section"]?.string ?? "", rawWhen: bd["when"], hasWhen: bd["when"] != nil))
            }
            sections.append(s)
        }

        for (i, scd) in (raw["scores"]?.array ?? []).enumerated() {
            let sp = Self.idx("scores", i)
            guard scd.isObject else { throw Self.fail(sp, "schema", "must be an object") }
            let sid = try declare(Self.join(sp, "id"), scd["id"])
            let m = scd["missing"]
            var bands: [Band] = []
            for (j, b) in (scd["bands"]?.array ?? []).enumerated() {
                bands.append(Band(min: try decimal(Self.join(Self.idx(Self.join(sp, "bands"), j), "min"), b["min"]),
                                  id: b["id"]?.string ?? "", text: b["text"]?.string))
            }
            scoreIndex[sid] = i
            scores.append(Score(id: sid, index: i,
                                missing: Missing(policy: m?["policy"]?.string ?? "null",
                                                 minAnswered: m?["minAnswered"]?.intValue ?? 0,
                                                 places: m?["places"]?.intValue ?? 0),
                                bands: bands, raw: scd))
        }

        // Flags (#199): staff-side, declared after the scores (as in Go).
        for (i, fd) in (raw["flags"]?.array ?? []).enumerated() {
            let fp = Self.idx("flags", i)
            guard fd.isObject else { throw Self.fail(fp, "schema", "must be an object") }
            let fid = try declare(Self.join(fp, "id"), fd["id"])
            guard fd["when"] != nil else { throw Self.fail(Self.join(fp, "when"), "schema", "required") }
            flags.append(Flag(id: fid, text: fd["text"]?.string, raw: fd["when"], when: nil))
        }

        // Expressions: scores first, then rules in document order (as in Go).
        for i in scores.indices {
            let path = Self.join(Self.idx("scores", i), "expr")
            var c = Ctx(rule: false, limit: openEnd, score: i, deps: noDeps)
            let (e, t) = try expr(path, scores[i].raw["expr"], 1, &c)
            guard t.kind == .number else { throw Self.fail(path, "type", "a score must be a number") }
            scores[i].expr = e
            scores[i].maxDep = c.deps
        }
        for si in sections.indices {
            let sp = Self.idx("sections", si)
            for (j, pgi) in sections[si].pages.enumerated() {
                let pp = Self.idx(Self.join(sp, "pages"), j)
                if let w = pages[pgi].raw["enableWhen"] {
                    pages[pgi].enableWhen = try rule(Self.join(pp, "enableWhen"), w, (pages[pgi].index, -1))
                }
                for (k, ii) in pages[pgi].items.enumerated() {
                    if let w = items[ii].raw["enableWhen"] {
                        items[ii].enableWhen = try rule(Self.join(Self.idx(Self.join(pp, "items"), k), "enableWhen"), w, (pages[pgi].index, k))
                    }
                }
            }
            let last = pages[sections[si].pages.last!].index
            for j in sections[si].next.indices {
                let bp = Self.idx(Self.join(sp, "next"), j)
                let b = sections[si].next[j]
                if b.hasWhen { sections[si].next[j].when = try rule(Self.join(bp, "when"), b.rawWhen, (last, Int.max)) }
                if b.section == Self.end { continue }
                guard let target = sectionIndex[b.section] else {
                    throw Self.fail(Self.join(bp, "section"), "reference", "no section \"\(b.section)\"")
                }
                if target <= si { throw Self.fail(Self.join(bp, "section"), "order", "next may only go forward") }
            }
        }
        // Flags use the whole submitted form: any item or score.
        for i in flags.indices {
            flags[i].when = try rule(Self.join(Self.idx("flags", i), "when"), flags[i].raw, openEnd)
        }
    }

    // MARK: expressions

    enum Kind: String { case number, string, boolean, date, set }
    struct TypeInfo { var kind: Kind; var lit = false; var choice: Int? = nil } // choice: item index

    struct Ctx { let rule: Bool; let limit: (Int, Int); let score: Int?; var deps: (Int, Int) }

    private func rule(_ path: String, _ raw: FormJSON?, _ limit: (Int, Int)) throws -> FormExpr {
        var c = Ctx(rule: true, limit: limit, score: nil, deps: noDeps)
        let (e, t) = try expr(path, raw, 1, &c)
        guard t.kind == .boolean else { throw Self.fail(path, "type", "a rule must be true or false") }
        return e
    }

    private func itemRef(_ path: String, _ v: FormJSON?, _ c: inout Ctx) throws -> Int {
        guard let id = v?.string else { throw Self.fail(path, "schema", "must be an item id") }
        guard let ii = itemIndex[id] else { throw Self.fail(path, "reference", "no item \"\(id)\"") }
        let it = items[ii]
        if it.type == "display" { throw Self.fail(path, "type", "item \"\(id)\" is a display item and has no answer") }
        if !less(it.position, c.limit) { throw Self.fail(path, "order", "item \"\(id)\" isn't earlier than this rule") }
        c.deps = maxPos(c.deps, it.position)
        return ii
    }

    private func answerType(_ ii: Int) -> TypeInfo {
        let it = items[ii]
        switch it.type {
        case "choice": return it.multiple ? TypeInfo(kind: .set, choice: ii) : TypeInfo(kind: .string, choice: ii)
        case "boolean": return TypeInfo(kind: .boolean)
        case "integer", "decimal": return TypeInfo(kind: .number)
        case "date": return TypeInfo(kind: .date)
        default: return TypeInfo(kind: .string)
        }
    }

    private func hasOption(_ ii: Int, _ v: String?) -> Bool { items[ii].options.contains { $0.value == v } }

    private static let arith: Set<String> = ["sum", "add", "sub", "mul", "div", "round", "count"]

    private func expr(_ path: String, _ rawExpr: FormJSON?, _ depth: Int, _ c: inout Ctx) throws -> (FormExpr, TypeInfo) {
        if depth > FormLimits.maxDepth { throw Self.fail(path, "depth", "expression is more than \(FormLimits.maxDepth) deep") }
        nodes += 1
        if nodes > FormLimits.maxNodes { throw Self.fail(path, "nodes", "more than \(FormLimits.maxNodes) expression nodes") }
        guard let pairs = rawExpr?.objectPairs else { throw Self.fail(path, "schema", "an expression must be an object") }
        guard pairs.count == 1 else { throw Self.fail(path, "operator", "an expression must have exactly one operator") }
        let (op, arg) = pairs[0]
        let ap = Self.join(path, op)

        func args(_ min: Int, _ max: Int) throws -> ([FormExpr], [TypeInfo]) {
            guard let a = arg.array else { throw Self.fail(ap, "schema", "must be an array of expressions") }
            if a.count < min || (max >= 0 && a.count > max) {
                throw Self.fail(ap, "operator", "\(op) takes \(min == max ? "" : "at least ")\(min) operands")
            }
            var es: [FormExpr] = [], ts: [TypeInfo] = []
            for (i, x) in a.enumerated() {
                let (e, t) = try expr(Self.idx(ap, i), x, depth + 1, &c)
                es.append(e)
                ts.append(t)
            }
            return (es, ts)
        }
        func need(_ i: Int, _ t: TypeInfo, _ k: Kind) throws {
            if t.kind != k { throw Self.fail(Self.idx(ap, i), "type", "\(op) can't take a \(t.kind.rawValue) here") }
        }
        if c.rule && Self.arith.contains(op) { throw Self.fail(ap, "operator", "arithmetic (\(op)) is only allowed in scores") }

        switch op {
        case "answer":
            let ii = try itemRef(ap, arg, &c)
            return (.answer(items[ii].id), answerType(ii))
        case "scoreOf":
            let ii = try itemRef(ap, arg, &c)
            let it = items[ii]
            if it.type != "choice" { throw Self.fail(ap, "type", "item \"\(it.id)\" isn't a choice item") }
            if it.options.contains(where: { $0.score == nil }) { throw Self.fail(ap, "type", "item \"\(it.id)\" has options without a score") }
            return (.scoreOf(it.id), TypeInfo(kind: .number))
        case "score":
            guard let sid = arg.string else { throw Self.fail(ap, "schema", "must be a score id") }
            guard let si = scoreIndex[sid] else { throw Self.fail(ap, "reference", "no score \"\(sid)\"") }
            if let own = c.score {
                if si >= own { throw Self.fail(ap, "order", "score \"\(sid)\" isn't defined before this one") }
            } else if !less(scores[si].maxDep, c.limit) {
                throw Self.fail(ap, "order", "score \"\(sid)\" depends on items that aren't earlier than this rule")
            }
            c.deps = maxPos(c.deps, scores[si].maxDep)
            return (.score(sid), TypeInfo(kind: .number))
        case "num":
            guard case let .number(t) = arg else { throw Self.fail(ap, "schema", "must be a number") }
            guard let r = FormRational.parse(t) else {
                throw Self.fail(ap, "range", "number must have at most 12 integer and 6 fractional digits, and no exponent")
            }
            return (.num(r), TypeInfo(kind: .number, lit: true))
        case "str":
            guard let s = arg.string else { throw Self.fail(ap, "schema", "must be a string") }
            return (.str(s), TypeInfo(kind: .string, lit: true))
        case "bool":
            guard let b = arg.bool else { throw Self.fail(ap, "schema", "must be true or false") }
            return (.bool(b), TypeInfo(kind: .boolean, lit: true))
        case "eq", "ne", "lt", "le", "gt", "ge":
            var (es, ts) = try args(2, 2)
            try comparable(ap, es, &ts, ordered: op != "eq" && op != "ne")
            return (.compare(op, es[0], es[1]), TypeInfo(kind: .boolean))
        case "in":
            let (es, ts) = try args(2, -1)
            let subj = ts[0]
            let want: Kind
            switch subj.kind {
            case .string, .set: want = .string
            case .number: want = .number
            default: throw Self.fail(Self.idx(ap, 0), "type", "in can't test a \(subj.kind.rawValue)")
            }
            for i in 1..<ts.count {
                if !ts[i].lit || ts[i].kind != want {
                    throw Self.fail(Self.idx(ap, i), "type", "in takes \(want.rawValue) literals after its first operand")
                }
                if let ch = subj.choice, case let .str(s) = es[i], !hasOption(ch, s) {
                    throw Self.fail(Self.idx(ap, i), "reference", "item \"\(items[ch].id)\" has no option \"\(s)\"")
                }
            }
            return (.inList(es[0], Array(es.dropFirst())), TypeInfo(kind: .boolean))
        case "answered":
            let ii = try itemRef(ap, arg, &c)
            return (.answered(items[ii].id), TypeInfo(kind: .boolean))
        case "and", "or":
            let (es, ts) = try args(1, -1)
            for (j, t) in ts.enumerated() { try need(j, t, .boolean) }
            return (op == "and" ? .and(es) : .or(es), TypeInfo(kind: .boolean))
        case "not":
            let (e, t) = try expr(ap, arg, depth + 1, &c)
            if t.kind != .boolean { throw Self.fail(ap, "type", "not can't take a \(t.kind.rawValue)") }
            return (.not(e), TypeInfo(kind: .boolean))
        case "sum", "add", "sub", "mul", "div":
            let (es, ts) = op == "sum" ? try args(1, -1) : try args(2, 2)
            for (j, t) in ts.enumerated() { try need(j, t, .number) }
            return (.arith(op, es), TypeInfo(kind: .number))
        case "round":
            let (es, ts) = try args(2, 2)
            try need(0, ts[0], .number)
            guard case let .num(pl) = es[1], pl.isInteger, pl.num.signum >= 0, pl.num <= FormBigInt(6) else {
                throw Self.fail(Self.idx(ap, 1), "range", "round places must be a num literal from 0 to 6")
            }
            return (.round(es[0], Int(pl.num.description)!), TypeInfo(kind: .number))
        case "count":
            guard let a = arg.array, !a.isEmpty else { throw Self.fail(ap, "schema", "must be a non-empty array of item ids") }
            var refs: [String] = []
            for (j, v) in a.enumerated() { refs.append(items[try itemRef(Self.idx(ap, j), v, &c)].id) }
            return (.count(refs), TypeInfo(kind: .number))
        default:
            throw Self.fail(path, "operator", "unknown operator \"\(op)\"")
        }
    }

    private func comparable(_ ap: String, _ es: [FormExpr], _ ts: inout [TypeInfo], ordered: Bool) throws {
        for i in 0..<2 where ts[i].kind == .set {
            throw Self.fail(Self.idx(ap, i), "type", "compare a multiple-choice answer with in")
        }
        for i in 0..<2 {
            let other = ts[1 - i]
            if ts[i].lit, ts[i].kind == .string, other.kind == .date {
                guard case let .str(s) = es[i], FormDates.valid(s) else {
                    throw Self.fail(Self.idx(ap, i), "type", "\"\(es[i].literalString ?? "")\" isn't a date (YYYY-MM-DD)")
                }
                ts[i] = TypeInfo(kind: .date, lit: true)
            }
        }
        if ts[0].kind != ts[1].kind {
            throw Self.fail(Self.idx(ap, 1), "type", "can't compare a \(ts[0].kind.rawValue) with a \(ts[1].kind.rawValue)")
        }
        if ordered && ts[0].kind != .number && ts[0].kind != .date {
            throw Self.fail(ap, "type", "only numbers and dates can be ordered")
        }
        for i in 0..<2 {
            if let ch = ts[1 - i].choice, ts[i].lit, case let .str(s) = es[i], !hasOption(ch, s) {
                throw Self.fail(Self.idx(ap, i), "reference", "item \"\(items[ch].id)\" has no option \"\(s)\"")
            }
        }
    }
}

private let noDeps = (-1, -1)
private let openEnd = (Int.max, Int.max)
private func less(_ a: (Int, Int), _ b: (Int, Int)) -> Bool { a.0 < b.0 || (a.0 == b.0 && a.1 < b.1) }
private func maxPos(_ a: (Int, Int), _ b: (Int, Int)) -> (Int, Int) { less(a, b) ? b : a }

/// A compiled rule or score expression.
indirect enum FormExpr: Sendable {
    case answer(String)
    case scoreOf(String)
    case score(String)
    case num(FormRational)
    case str(String)
    case bool(Bool)
    case compare(String, FormExpr, FormExpr)
    case inList(FormExpr, [FormExpr])
    case answered(String)
    case and([FormExpr])
    case or([FormExpr])
    case not(FormExpr)
    case arith(String, [FormExpr])
    case round(FormExpr, Int)
    case count([String])

    var literalString: String? { if case let .str(s) = self { return s }; return nil }
}

enum FormDates {
    /// A real calendar date written YYYY-MM-DD.
    static func valid(_ s: String) -> Bool {
        let u = Array(s.utf8)
        guard u.count == 10, u[4] == UInt8(ascii: "-"), u[7] == UInt8(ascii: "-"),
              [0, 1, 2, 3, 5, 6, 8, 9].allSatisfy({ (0x30...0x39).contains(u[$0]) }),
              let y = Int(s.prefix(4)), let m = Int(s.dropFirst(5).prefix(2)), let d = Int(s.suffix(2))
        else { return false }
        guard y >= 1, (1...12).contains(m), d >= 1 else { return false }
        var days = [31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][m - 1]
        if m == 2, y % 4 == 0, y % 100 != 0 || y % 400 == 0 { days = 29 }
        return d <= days
    }

    /// Today's local date as YYYY-MM-DD.
    static func today(_ now: Date = Date()) -> String {
        let c = Calendar(identifier: .gregorian).dateComponents([.year, .month, .day], from: now)
        return String(format: "%04d-%02d-%02d", c.year ?? 1, c.month ?? 1, c.day ?? 1)
    }
}

// MARK: - evaluation

/// The outcome of evaluating a form against answers.
struct FormResult: Sendable {
    struct ScoreOut: Equatable, Sendable {
        /// Canonical decimal text, or nil when the score can't be computed.
        let value: String?
        let band: String?
        /// Why value is nil: missing, divideByZero, belowMinAnswered or overflow.
        let reason: String?
    }
    var visible: [String: Bool] = [:]
    var next: [String: String] = [:]
    var path: [String] = []
    var scores: [String: ScoreOut] = [:]
    /// The canonical answers to store: well-formed answers to visible items.
    var answers: [String: FormJSON] = [:]
    /// Raised staff flags, in definition order (#199). The app never shows
    /// them; they're here so the evaluator matches the shared vectors.
    var flags: [String] = []
    /// An "overflow" error when a score exceeded the format's bounds.
    var error: FormError?
}

struct FormAnswerError: Equatable, Sendable {
    let item: String
    let code: String
    let message: String
}

/// The outcome of checking answers before submit (mirrors forms.CheckAnswersOn).
struct FormChecked: Sendable {
    let answers: [String: FormJSON]
    let errors: [FormAnswerError]
    let result: FormResult
}

extension FormDefinition {
    /// Walks the form once: visibility, the section path, next sections and
    /// scores (see internal/forms' package doc for the rules).
    func evaluate(_ answers: [String: FormJSON]) -> FormResult {
        var e = FormEval(f: self)
        var res = FormResult()
        var target = sections[0].id
        for s in sections {
            let on = s.id == target
            res.visible[s.id] = on
            for pgi in s.pages {
                let pg = pages[pgi]
                let pv = on && e.test(pg.enableWhen)
                res.visible[pg.id] = pv
                for ii in pg.items {
                    let it = items[ii]
                    let iv = pv && e.test(it.enableWhen)
                    res.visible[it.id] = iv
                    guard iv, let raw = answers[it.id] else { continue }
                    let av = FormEval.answerValue(it, raw)
                    if let v = av.value {
                        e.eff[it.id] = v
                        res.answers[it.id] = raw
                    }
                }
            }
            let next = e.next(s)
            res.next[s.id] = next
            if on {
                res.path.append(s.id)
                target = next
            }
        }
        for sc in scores {
            let s = e.score(sc)
            res.scores[sc.id] = FormResult.ScoreOut(value: s.value, band: s.band, reason: s.reason)
        }
        res.flags = flags.filter { e.test($0.when) }.map(\.id)
        res.error = e.error
        return res
    }

    /// Checks answers as the Back Office will on submit: wrong types, values
    /// out of range, unknown options, text too long and required items missing
    /// on visible pages. Answers to hidden items are dropped.
    func checkAnswers(_ answers: [String: FormJSON], today: String = FormDates.today()) -> FormChecked {
        var errs: [FormAnswerError] = []
        var bad = Set<String>()
        var valid: [String: FormJSON] = [:]
        for id in answers.keys.sorted() {
            let raw = answers[id]!
            guard let it = item(id) else {
                errs.append(FormAnswerError(item: id, code: "unknown_item", message: "no such item"))
                bad.insert(id)
                continue
            }
            let av = FormEval.answerValue(it, raw)
            if let code = av.code {
                errs.append(FormAnswerError(item: id, code: code, message: FormEval.problems[code] ?? code))
                bad.insert(id)
                continue
            }
            guard let v = av.value else { continue }
            if let (code, msg) = FormEval.rangeProblem(it, v, today) {
                errs.append(FormAnswerError(item: id, code: code, message: msg))
                bad.insert(id)
                continue
            }
            valid[id] = raw
        }
        let res = evaluate(valid)
        errs = errs.filter { item($0.item) == nil || res.visible[$0.item] == true }
        for it in items where it.required && res.visible[it.id] == true && !bad.contains(it.id) && res.answers[it.id] == nil {
            errs.append(FormAnswerError(item: it.id, code: "required", message: "an answer is required"))
        }
        errs = errs.enumerated().sorted { x, y in
            let a = item(x.element.item), b = item(y.element.item)
            if let a, let b { return a.seq != b.seq ? a.seq < b.seq : x.offset < y.offset }
            if (a != nil) != (b != nil) { return a != nil }
            return x.element.item != y.element.item ? x.element.item < y.element.item : x.offset < y.offset
        }.map(\.element)
        if res.error != nil {
            for sc in scores where res.scores[sc.id]?.reason == "overflow" {
                errs.append(FormAnswerError(item: sc.id, code: "overflow", message: "the score can't be computed exactly within bounds"))
            }
        }
        return FormChecked(answers: res.answers, errors: errs, result: res)
    }
}

/// A runtime value. `.null` carries why (missing, divideByZero, ...).
enum FormValue: Sendable {
    case null(String)
    case num(FormRational)
    case str(String)
    case bool(Bool)
    case set([String])

    var isNull: Bool { if case .null = self { return true }; return false }
    var isTrue: Bool { if case .bool(true) = self { return true }; return false }
}

private struct FormEval {
    let f: FormDefinition
    var eff: [String: FormValue] = [:]
    var scores: [String: (value: String?, exact: FormRational?, band: String?, reason: String?)] = [:]
    var error: FormError?

    init(f: FormDefinition) { self.f = f }

    static let problems = [
        "not_answerable": "a display item takes no answer",
        "unknown_option": "not one of the item's options",
        "duplicate_option": "an option is chosen twice",
        "wrong_type": "wrong type",
    ]

    /// (value, code): value when well formed; code for a problem; neither
    /// when unanswered (null, an empty choice list or empty text).
    static func answerValue(_ it: FormDefinition.Item, _ raw: FormJSON) -> (value: FormValue?, code: String?) {
        if case .null = raw { return (nil, nil) }
        func has(_ v: String) -> Bool { it.options.contains { $0.value == v } }
        switch it.type {
        case "display":
            return (nil, "not_answerable")
        case "choice":
            if !it.multiple {
                guard let s = raw.string else { return (nil, "wrong_type") }
                guard has(s) else { return (nil, "unknown_option") }
                return (.str(s), nil)
            }
            guard let a = raw.array else { return (nil, "wrong_type") }
            if a.isEmpty { return (nil, nil) }
            var seen = Set<String>(), out: [String] = []
            for v in a {
                guard let s = v.string else { return (nil, "wrong_type") }
                guard has(s) else { return (nil, "unknown_option") }
                guard seen.insert(s).inserted else { return (nil, "duplicate_option") }
                out.append(s)
            }
            return (.set(out), nil)
        case "boolean":
            guard let b = raw.bool else { return (nil, "wrong_type") }
            return (.bool(b), nil)
        case "integer", "decimal":
            guard case let .number(t) = raw else { return (nil, "wrong_type") }
            if it.type == "integer" && !FormRational.isIntegerLiteral(t) { return (nil, "wrong_type") }
            guard let r = FormRational.parse(t) else { return (nil, "wrong_type") }
            return (.num(r), nil)
        case "date":
            guard let s = raw.string, FormDates.valid(s) else { return (nil, "wrong_type") }
            return (.str(s), nil)
        case "text":
            guard let s = raw.string else { return (nil, "wrong_type") }
            if s.isEmpty { return (nil, nil) }
            return (.str(s), nil)
        default:
            return (nil, "wrong_type")
        }
    }

    static func rangeProblem(_ it: FormDefinition.Item, _ v: FormValue, _ today: String) -> (String, String)? {
        switch (it.type, v) {
        case ("integer", .num(let r)), ("decimal", .num(let r)):
            if let mn = it.min, r.compare(mn) < 0 { return ("out_of_range", "below the minimum \(mn.text)") }
            if let mx = it.max, r.compare(mx) > 0 { return ("out_of_range", "above the maximum \(mx.text)") }
        case ("date", .str(let s)):
            let mn = it.minDate == "today" ? today : it.minDate
            let mx = it.maxDate == "today" ? today : it.maxDate
            if !mn.isEmpty && s < mn { return ("out_of_range", "before \(mn)") }
            if !mx.isEmpty && s > mx { return ("out_of_range", "after \(mx)") }
        case ("text", .str(let s)):
            if s.unicodeScalars.count > it.maxLength { return ("too_long", "longer than \(it.maxLength) characters") }
        default: break
        }
        return nil
    }

    mutating func bound(_ r: FormRational) -> FormValue {
        guard r.inBounds else {
            if error == nil { error = FormError(path: "", kind: "overflow", message: "a score result exceeds 128-bit numerator/denominator bounds") }
            return .null("overflow")
        }
        return .num(r)
    }

    mutating func test(_ x: FormExpr?) -> Bool {
        guard let x else { return true }
        return node(x, nil).isTrue
    }

    mutating func next(_ s: FormDefinition.Section) -> String {
        for b in s.next where b.when == nil || test(b.when) { return b.section }
        return s.index + 1 < f.sections.count ? f.sections[s.index + 1].id : FormDefinition.end
    }

    mutating func score(_ sc: FormDefinition.Score) -> (value: String?, exact: FormRational?, band: String?, reason: String?) {
        if let s = scores[sc.id] { return s }
        let v = node(sc.expr, sc.missing)
        var out: (value: String?, exact: FormRational?, band: String?, reason: String?) = (nil, nil, nil, nil)
        switch v {
        case let .null(reason): out.reason = reason
        case let .num(r):
            out.exact = r
            out.value = r.text
            for b in sc.bands where r.compare(b.min) >= 0 { out.band = b.id }
        default: break
        }
        scores[sc.id] = out
        return out
    }

    private func optionScore(_ it: FormDefinition.Item, _ v: String) -> FormRational {
        it.options.first { $0.value == v }?.score ?? .zero
    }

    private static func compare(_ a: FormValue, _ b: FormValue) -> Int {
        switch (a, b) {
        case let (.num(x), .num(y)): return x.compare(y)
        case let (.str(x), .str(y)): return x < y ? -1 : (x == y ? 0 : 1)
        case let (.bool(x), .bool(y)): return x == y ? 0 : 1
        default: return 1
        }
    }

    mutating func node(_ x: FormExpr, _ policy: FormDefinition.Missing?) -> FormValue {
        switch x {
        case let .answer(id):
            return eff[id] ?? .null("missing")
        case let .scoreOf(id):
            guard let v = eff[id], let it = f.item(id) else { return .null("missing") }
            switch v {
            case let .str(s): return .num(optionScore(it, s))
            case let .set(list):
                var acc = FormValue.num(.zero)
                for s in list {
                    guard case let .num(a) = acc else { break }
                    acc = bound(a + optionScore(it, s))
                    if acc.isNull { return acc }
                }
                return acc
            default: return .null("missing")
            }
        case let .score(id):
            let s = score(f.scores[f.scoreIndex[id]!])
            if let e = s.exact { return .num(e) }
            return .null(s.reason ?? "missing")
        case let .num(r): return .num(r)
        case let .str(s): return .str(s)
        case let .bool(b): return .bool(b)
        case let .compare(op, ae, be):
            let a = node(ae, policy), b = node(be, policy)
            if a.isNull || b.isNull { return .bool(false) }
            let c = Self.compare(a, b)
            switch op {
            case "eq": return .bool(c == 0)
            case "ne": return .bool(c != 0)
            case "lt": return .bool(c < 0)
            case "le": return .bool(c <= 0)
            case "gt": return .bool(c > 0)
            default: return .bool(c >= 0)
            }
        case let .inList(se, lits):
            let subj = node(se, policy)
            for lit in lits {
                switch (subj, lit) {
                case let (.num(a), .num(b)) where a.compare(b) == 0: return .bool(true)
                case let (.set(list), .str(s)) where list.contains(s): return .bool(true)
                case let (.str(a), .str(b)) where a == b: return .bool(true)
                default: continue
                }
            }
            return .bool(false)
        case let .answered(id):
            return .bool(eff[id] != nil)
        case let .and(es):
            for e in es where !node(e, policy).isTrue { return .bool(false) }
            return .bool(true)
        case let .or(es):
            for e in es where node(e, policy).isTrue { return .bool(true) }
            return .bool(false)
        case let .not(e):
            return .bool(!node(e, policy).isTrue)
        case let .count(ids):
            return .num(FormRational(ids.filter { eff[$0] != nil }.count))
        case let .arith("sum", es):
            return sum(es, policy)
        case let .arith(op, es):
            let a = node(es[0], policy), b = node(es[1], policy)
            guard case let .num(x) = a else { return a }
            guard case let .num(y) = b else { return b }
            switch op {
            case "add": return bound(x + y)
            case "sub": return bound(x - y)
            case "mul": return bound(x * y)
            default:
                if y.num.isZero { return .null("divideByZero") }
                return bound(x / y)
            }
        case let .round(e, places):
            let a = node(e, policy)
            guard case let .num(x) = a else { return a }
            return bound(x.roundedHalfEven(places))
        }
    }

    mutating func sum(_ es: [FormExpr], _ policy: FormDefinition.Missing?) -> FormValue {
        let vals = es.map { node($0, policy) }
        let p = policy ?? FormDefinition.Missing(policy: "null", minAnswered: 0, places: 0)
        var acc = FormRational.zero
        var answered = 0
        for v in vals {
            switch v {
            case let .null(reason):
                if p.policy == "null" || reason == "overflow" { return v }
                continue
            case let .num(r):
                answered += 1
                let b = bound(acc + r)
                guard case let .num(n) = b else { return b }
                acc = n
            default:
                continue
            }
        }
        if answered < p.minAnswered { return .null("belowMinAnswered") }
        if p.policy != "prorate" { return .num(acc) }
        if answered == 0 { return .null("missing") }
        if answered < vals.count {
            let scaled = bound(acc * FormRational(vals.count))
            guard case let .num(s) = scaled else { return scaled }
            let prorated = bound(s / FormRational(answered))
            guard case let .num(pr) = prorated else { return prorated }
            acc = pr
        }
        return bound(acc.roundedHalfEven(p.places))
    }
}
