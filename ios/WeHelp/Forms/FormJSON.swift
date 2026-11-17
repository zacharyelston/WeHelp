import Foundation

/// JSON as wehelp.form.v1 needs it: numbers keep their literal text (like
/// Go's json.Number), so decimals are never rounded through a Double, and
/// objects keep their keys in order. Used for form definitions and answers.
indirect enum FormJSON: Equatable, Sendable {
    case null
    case bool(Bool)
    /// The number exactly as written, e.g. "38.5" or "-0.25".
    case number(String)
    case string(String)
    case array([FormJSON])
    case object([(String, FormJSON)])

    static func == (a: FormJSON, b: FormJSON) -> Bool {
        switch (a, b) {
        case (.null, .null): true
        case let (.bool(x), .bool(y)): x == y
        case let (.number(x), .number(y)): x == y
        case let (.string(x), .string(y)): x == y
        case let (.array(x), .array(y)): x == y
        case let (.object(x), .object(y)):
            x.count == y.count && zip(x, y).allSatisfy { $0.0 == $1.0 && $0.1 == $1.1 }
        default: false
        }
    }

    subscript(key: String) -> FormJSON? {
        if case let .object(pairs) = self { return pairs.first { $0.0 == key }?.1 }
        return nil
    }

    var string: String? { if case let .string(s) = self { return s }; return nil }
    var bool: Bool? { if case let .bool(b) = self { return b }; return nil }
    var array: [FormJSON]? { if case let .array(a) = self { return a }; return nil }
    var objectPairs: [(String, FormJSON)]? { if case let .object(o) = self { return o }; return nil }
    var isObject: Bool { if case .object = self { return true }; return false }
    var isNumber: Bool { if case .number = self { return true }; return false }
    var intValue: Int? { if case let .number(n) = self { return Int(n) }; return nil }

    struct ParseError: Error, Equatable { let offset: Int; let message: String }

    /// Parses UTF-8 JSON text (RFC 8259). Duplicate object keys are refused.
    static func parse(_ data: Data) throws -> FormJSON {
        var p = Parser(bytes: Array(data))
        p.skipSpace()
        let v = try p.value(depth: 0)
        p.skipSpace()
        guard p.i == p.bytes.count else { throw ParseError(offset: p.i, message: "trailing data") }
        return v
    }

    static func parse(_ text: String) throws -> FormJSON { try parse(Data(text.utf8)) }

    /// Serializes back to JSON (compact, keys in their stored order).
    var text: String {
        switch self {
        case .null: return "null"
        case let .bool(b): return b ? "true" : "false"
        case let .number(n): return n
        case let .string(s): return Self.quote(s)
        case let .array(a): return "[" + a.map(\.text).joined(separator: ",") + "]"
        case let .object(o): return "{" + o.map { Self.quote($0.0) + ":" + $0.1.text }.joined(separator: ",") + "}"
        }
    }

    static func quote(_ s: String) -> String {
        var out = "\""
        for u in s.unicodeScalars {
            switch u {
            case "\"": out += "\\\""
            case "\\": out += "\\\\"
            case "\n": out += "\\n"
            case "\r": out += "\\r"
            case "\t": out += "\\t"
            default:
                if u.value < 0x20 { out += String(format: "\\u%04x", u.value) } else { out.unicodeScalars.append(u) }
            }
        }
        return out + "\""
    }

    private struct Parser {
        let bytes: [UInt8]
        var i = 0
        static let maxDepth = 256

        mutating func skipSpace() {
            while i < bytes.count, [0x20, 0x09, 0x0A, 0x0D].contains(bytes[i]) { i += 1 }
        }

        func fail(_ m: String) -> ParseError { ParseError(offset: i, message: m) }

        mutating func value(depth: Int) throws -> FormJSON {
            guard depth < Self.maxDepth else { throw fail("nested too deeply") }
            guard i < bytes.count else { throw fail("unexpected end") }
            switch bytes[i] {
            case UInt8(ascii: "{"): return try object(depth: depth)
            case UInt8(ascii: "["): return try array(depth: depth)
            case UInt8(ascii: "\""): return .string(try string())
            case UInt8(ascii: "t"): try literal("true"); return .bool(true)
            case UInt8(ascii: "f"): try literal("false"); return .bool(false)
            case UInt8(ascii: "n"): try literal("null"); return .null
            default: return .number(try number())
            }
        }

        mutating func literal(_ word: String) throws {
            let w = Array(word.utf8)
            guard i + w.count <= bytes.count, Array(bytes[i..<i + w.count]) == w else { throw fail("bad literal") }
            i += w.count
        }

        mutating func object(depth: Int) throws -> FormJSON {
            i += 1
            var pairs: [(String, FormJSON)] = []
            var seen = Set<String>()
            skipSpace()
            if i < bytes.count, bytes[i] == UInt8(ascii: "}") { i += 1; return .object(pairs) }
            while true {
                skipSpace()
                guard i < bytes.count, bytes[i] == UInt8(ascii: "\"") else { throw fail("expected a key") }
                let k = try string()
                guard seen.insert(k).inserted else { throw fail("duplicate key \"\(k)\"") }
                skipSpace()
                guard i < bytes.count, bytes[i] == UInt8(ascii: ":") else { throw fail("expected :") }
                i += 1
                skipSpace()
                pairs.append((k, try value(depth: depth + 1)))
                skipSpace()
                guard i < bytes.count else { throw fail("unexpected end") }
                if bytes[i] == UInt8(ascii: ",") { i += 1; continue }
                if bytes[i] == UInt8(ascii: "}") { i += 1; return .object(pairs) }
                throw fail("expected , or }")
            }
        }

        mutating func array(depth: Int) throws -> FormJSON {
            i += 1
            var items: [FormJSON] = []
            skipSpace()
            if i < bytes.count, bytes[i] == UInt8(ascii: "]") { i += 1; return .array(items) }
            while true {
                skipSpace()
                items.append(try value(depth: depth + 1))
                skipSpace()
                guard i < bytes.count else { throw fail("unexpected end") }
                if bytes[i] == UInt8(ascii: ",") { i += 1; continue }
                if bytes[i] == UInt8(ascii: "]") { i += 1; return .array(items) }
                throw fail("expected , or ]")
            }
        }

        mutating func number() throws -> String {
            let start = i
            func digits() -> Int { let s = i; while i < bytes.count, (0x30...0x39).contains(bytes[i]) { i += 1 }; return i - s }
            if i < bytes.count, bytes[i] == UInt8(ascii: "-") { i += 1 }
            guard i < bytes.count, (0x30...0x39).contains(bytes[i]) else { throw fail("bad number") }
            if bytes[i] == UInt8(ascii: "0") { i += 1 } else { _ = digits() }
            if i < bytes.count, bytes[i] == UInt8(ascii: ".") {
                i += 1
                guard digits() > 0 else { throw fail("bad number") }
            }
            if i < bytes.count, bytes[i] == UInt8(ascii: "e") || bytes[i] == UInt8(ascii: "E") {
                i += 1
                if i < bytes.count, bytes[i] == UInt8(ascii: "+") || bytes[i] == UInt8(ascii: "-") { i += 1 }
                guard digits() > 0 else { throw fail("bad number") }
            }
            return String(decoding: bytes[start..<i], as: UTF8.self)
        }

        mutating func hex4() throws -> UInt32 {
            guard i + 4 <= bytes.count, let v = UInt32(String(decoding: bytes[i..<i + 4], as: UTF8.self), radix: 16) else {
                throw fail("bad \\u escape")
            }
            i += 4
            return v
        }

        mutating func string() throws -> String {
            i += 1
            var out = [UInt8]()
            while true {
                guard i < bytes.count else { throw fail("unterminated string") }
                let c = bytes[i]
                if c == UInt8(ascii: "\"") { i += 1; break }
                if c < 0x20 { throw fail("control character in string") }
                if c != UInt8(ascii: "\\") { out.append(c); i += 1; continue }
                i += 1
                guard i < bytes.count else { throw fail("bad escape") }
                let e = bytes[i]
                i += 1
                switch e {
                case UInt8(ascii: "\""): out.append(0x22)
                case UInt8(ascii: "\\"): out.append(0x5C)
                case UInt8(ascii: "/"): out.append(0x2F)
                case UInt8(ascii: "b"): out.append(0x08)
                case UInt8(ascii: "f"): out.append(0x0C)
                case UInt8(ascii: "n"): out.append(0x0A)
                case UInt8(ascii: "r"): out.append(0x0D)
                case UInt8(ascii: "t"): out.append(0x09)
                case UInt8(ascii: "u"):
                    var cp = try hex4()
                    if (0xD800...0xDBFF).contains(cp) {
                        guard i + 1 < bytes.count, bytes[i] == UInt8(ascii: "\\"), bytes[i + 1] == UInt8(ascii: "u") else {
                            throw fail("lone surrogate")
                        }
                        i += 2
                        let lo = try hex4()
                        guard (0xDC00...0xDFFF).contains(lo) else { throw fail("lone surrogate") }
                        cp = 0x10000 + ((cp - 0xD800) << 10) + (lo - 0xDC00)
                    } else if (0xDC00...0xDFFF).contains(cp) {
                        throw fail("lone surrogate")
                    }
                    guard let s = Unicode.Scalar(cp) else { throw fail("bad code point") }
                    out.append(contentsOf: Array(String(Character(s)).utf8))
                default: throw fail("bad escape")
                }
            }
            guard let s = String(bytes: out, encoding: .utf8) else { throw fail("invalid UTF-8") }
            return s
        }
    }
}
