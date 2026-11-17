import Foundation

/// A small signed big integer, enough for wehelp.form.v1's exact arithmetic:
/// scores keep numerator and denominator within 128 bits, so intermediates
/// stay within a few hundred bits. Magnitude is little-endian 32-bit limbs.
/// Not a general-purpose library: no performance tricks, no bit operations
/// beyond what the evaluator needs.
struct FormBigInt: Equatable, Comparable, Sendable, CustomStringConvertible {
    private(set) var negative: Bool
    private(set) var limbs: [UInt32] // no trailing zero limbs; zero is []

    static let zero = FormBigInt(negative: false, limbs: [])
    static let one = FormBigInt(1)

    private init(negative: Bool, limbs: [UInt32]) {
        var l = limbs
        while let last = l.last, last == 0 { l.removeLast() }
        self.limbs = l
        self.negative = l.isEmpty ? false : negative
    }

    init(_ v: Int) {
        var m = v.magnitude
        var l: [UInt32] = []
        while m > 0 { l.append(UInt32(truncatingIfNeeded: m)); m >>= 32 }
        self.init(negative: v < 0, limbs: l)
    }

    /// Decimal digits with an optional leading "-".
    init?(decimal s: Substring) {
        var neg = false
        var digits = s
        if digits.first == "-" { neg = true; digits = digits.dropFirst() }
        guard !digits.isEmpty, digits.allSatisfy({ $0.isASCII && $0.isNumber }) else { return nil }
        var acc = FormBigInt.zero
        let ten9 = FormBigInt(1_000_000_000)
        var rest = digits[...]
        // Leading chunk so the remaining length is a multiple of 9.
        let first = rest.count % 9 == 0 ? 9 : rest.count % 9
        var chunk = rest.prefix(first)
        rest = rest.dropFirst(first)
        while true {
            acc = acc * ten9.pow10Chunk(chunk.count) + FormBigInt(Int(chunk)!)
            if rest.isEmpty { break }
            chunk = rest.prefix(9)
            rest = rest.dropFirst(9)
        }
        self = neg ? -acc : acc
    }

    /// 10^n for a chunk of n ≤ 9 digits (helper for parsing).
    private func pow10Chunk(_ n: Int) -> FormBigInt {
        var p = 1
        for _ in 0..<n { p *= 10 }
        return FormBigInt(p)
    }

    static func pow10(_ n: Int) -> FormBigInt {
        var r = FormBigInt.one
        let ten = FormBigInt(10)
        for _ in 0..<n { r = r * ten }
        return r
    }

    var isZero: Bool { limbs.isEmpty }
    var signum: Int { isZero ? 0 : (negative ? -1 : 1) }
    var magnitude: FormBigInt { FormBigInt(negative: false, limbs: limbs) }

    var bitLength: Int {
        guard let top = limbs.last else { return 0 }
        return (limbs.count - 1) * 32 + (32 - top.leadingZeroBitCount)
    }

    var isEven: Bool { (limbs.first ?? 0) & 1 == 0 }

    // MARK: magnitude helpers

    private static func cmpMag(_ a: [UInt32], _ b: [UInt32]) -> Int {
        if a.count != b.count { return a.count < b.count ? -1 : 1 }
        for i in stride(from: a.count - 1, through: 0, by: -1) where a[i] != b[i] {
            return a[i] < b[i] ? -1 : 1
        }
        return 0
    }

    private static func addMag(_ a: [UInt32], _ b: [UInt32]) -> [UInt32] {
        var out = [UInt32](repeating: 0, count: max(a.count, b.count) + 1)
        var carry: UInt64 = 0
        for i in 0..<out.count - 1 {
            let s = UInt64(i < a.count ? a[i] : 0) + UInt64(i < b.count ? b[i] : 0) + carry
            out[i] = UInt32(truncatingIfNeeded: s)
            carry = s >> 32
        }
        out[out.count - 1] = UInt32(carry)
        return out
    }

    /// a - b with |a| ≥ |b|.
    private static func subMag(_ a: [UInt32], _ b: [UInt32]) -> [UInt32] {
        var out = a
        var borrow: Int64 = 0
        for i in 0..<a.count {
            var d = Int64(a[i]) - Int64(i < b.count ? b[i] : 0) - borrow
            if d < 0 { d += 1 << 32; borrow = 1 } else { borrow = 0 }
            out[i] = UInt32(d)
        }
        return out
    }

    private static func mulMag(_ a: [UInt32], _ b: [UInt32]) -> [UInt32] {
        if a.isEmpty || b.isEmpty { return [] }
        var out = [UInt32](repeating: 0, count: a.count + b.count)
        for i in 0..<a.count {
            var carry: UInt64 = 0
            let ai = UInt64(a[i])
            for j in 0..<b.count {
                let t = ai * UInt64(b[j]) + UInt64(out[i + j]) + carry
                out[i + j] = UInt32(truncatingIfNeeded: t)
                carry = t >> 32
            }
            out[i + b.count] = UInt32(carry)
        }
        return out
    }

    /// Truncating division of magnitudes (shift-and-subtract; inputs are small).
    private static func divModMag(_ a: [UInt32], _ b: [UInt32]) -> ([UInt32], [UInt32]) {
        precondition(!b.isEmpty, "division by zero")
        if cmpMag(a, b) < 0 { return ([], a) }
        let bits = FormBigInt(negative: false, limbs: a).bitLength
        var q = [UInt32](repeating: 0, count: a.count)
        var r: [UInt32] = []
        for bit in stride(from: bits - 1, through: 0, by: -1) {
            // r = r << 1 | bit
            var carry: UInt32 = (a[bit / 32] >> UInt32(bit % 32)) & 1
            for k in 0..<r.count {
                let next = r[k] >> 31
                r[k] = (r[k] << 1) | carry
                carry = next
            }
            if carry != 0 { r.append(carry) }
            if cmpMag(r, b) >= 0 {
                r = FormBigInt(negative: false, limbs: subMag(r, b)).limbs
                q[bit / 32] |= 1 << UInt32(bit % 32)
            }
        }
        return (q, r)
    }

    // MARK: arithmetic

    static prefix func - (a: FormBigInt) -> FormBigInt { FormBigInt(negative: !a.negative, limbs: a.limbs) }

    static func + (a: FormBigInt, b: FormBigInt) -> FormBigInt {
        if a.negative == b.negative { return FormBigInt(negative: a.negative, limbs: addMag(a.limbs, b.limbs)) }
        let c = cmpMag(a.limbs, b.limbs)
        if c == 0 { return .zero }
        return c > 0 ? FormBigInt(negative: a.negative, limbs: subMag(a.limbs, b.limbs))
                     : FormBigInt(negative: b.negative, limbs: subMag(b.limbs, a.limbs))
    }

    static func - (a: FormBigInt, b: FormBigInt) -> FormBigInt { a + (-b) }

    static func * (a: FormBigInt, b: FormBigInt) -> FormBigInt {
        FormBigInt(negative: a.negative != b.negative, limbs: mulMag(a.limbs, b.limbs))
    }

    /// Truncated quotient and remainder (remainder has the dividend's sign).
    func quotientAndRemainder(dividingBy b: FormBigInt) -> (FormBigInt, FormBigInt) {
        let (q, r) = Self.divModMag(limbs, b.limbs)
        return (FormBigInt(negative: negative != b.negative, limbs: q), FormBigInt(negative: negative, limbs: r))
    }

    /// Floor division for a positive divisor: q = ⌊a/b⌋, 0 ≤ m < b.
    func floorDivMod(_ b: FormBigInt) -> (FormBigInt, FormBigInt) {
        var (q, r) = quotientAndRemainder(dividingBy: b)
        if r.negative { q = q - .one; r = r + b }
        return (q, r)
    }

    static func gcd(_ a: FormBigInt, _ b: FormBigInt) -> FormBigInt {
        var x = a.magnitude, y = b.magnitude
        while !y.isZero { (x, y) = (y, x.quotientAndRemainder(dividingBy: y).1) }
        return x
    }

    static func < (a: FormBigInt, b: FormBigInt) -> Bool {
        if a.negative != b.negative { return a.negative }
        let c = cmpMag(a.limbs, b.limbs)
        return a.negative ? c > 0 : c < 0
    }

    var description: String {
        if isZero { return "0" }
        var parts: [String] = []
        var m = magnitude
        let ten9 = FormBigInt(1_000_000_000)
        while !m.isZero {
            let (q, r) = m.quotientAndRemainder(dividingBy: ten9)
            parts.append(String(r.limbs.first ?? 0))
            m = q
        }
        var s = parts.last!
        for p in parts.dropLast().reversed() { s += String(repeating: "0", count: 9 - p.count) + p }
        return (negative ? "-" : "") + s
    }
}

/// An exact rational, always normalised (denominator > 0, gcd 1).
struct FormRational: Equatable, Sendable {
    let num: FormBigInt
    let den: FormBigInt

    init(_ n: FormBigInt, _ d: FormBigInt = .one) {
        var n = n, d = d
        if d.signum < 0 { n = -n; d = -d }
        let g = FormBigInt.gcd(n, d)
        if g > .one {
            n = n.quotientAndRemainder(dividingBy: g).0
            d = d.quotientAndRemainder(dividingBy: g).0
        }
        num = n
        den = d
    }

    init(_ i: Int) { self.init(FormBigInt(i)) }

    static let zero = FormRational(0)

    var isInteger: Bool { den == .one }

    /// Numerator and denominator each fit in 128 bits (the format's bound).
    var inBounds: Bool { num.bitLength <= 128 && den.bitLength <= 128 }

    static func + (a: FormRational, b: FormRational) -> FormRational { FormRational(a.num * b.den + b.num * a.den, a.den * b.den) }
    static func - (a: FormRational, b: FormRational) -> FormRational { FormRational(a.num * b.den - b.num * a.den, a.den * b.den) }
    static func * (a: FormRational, b: FormRational) -> FormRational { FormRational(a.num * b.num, a.den * b.den) }
    /// b must not be zero.
    static func / (a: FormRational, b: FormRational) -> FormRational { FormRational(a.num * b.den, a.den * b.num) }

    func compare(_ b: FormRational) -> Int {
        let l = num * b.den, r = b.num * den
        return l < r ? -1 : (l == r ? 0 : 1)
    }

    /// A definition or answer number: at most 12 integer and 6 fractional
    /// digits, no exponent (the Go numPattern).
    static func parse(_ text: String) -> FormRational? {
        guard isDecimalLiteral(text) else { return nil }
        var t = Substring(text)
        var neg = false
        if t.first == "-" { neg = true; t = t.dropFirst() }
        let parts = t.split(separator: ".", omittingEmptySubsequences: false)
        let frac = parts.count > 1 ? parts[1] : ""
        guard let n = FormBigInt(decimal: Substring(String(parts[0]) + String(frac))) else { return nil }
        return FormRational(neg ? -n : n, FormBigInt.pow10(frac.count))
    }

    static func isDecimalLiteral(_ s: String) -> Bool { matches(s, allowFraction: true) }
    static func isIntegerLiteral(_ s: String) -> Bool { matches(s, allowFraction: false) }

    /// ^-?(0|[1-9][0-9]{0,11})(\.[0-9]{1,6})?$ without a regex.
    private static func matches(_ s: String, allowFraction: Bool) -> Bool {
        var b = Array(s.utf8)[...]
        if b.first == UInt8(ascii: "-") { b = b.dropFirst() }
        let digit: (UInt8) -> Bool = { (0x30...0x39).contains($0) }
        let intPart = b.prefix { digit($0) }
        guard (1...12).contains(intPart.count) else { return false }
        if intPart.count > 1, intPart.first == UInt8(ascii: "0") { return false }
        b = b.dropFirst(intPart.count)
        if b.isEmpty { return true }
        guard allowFraction, b.first == UInt8(ascii: ".") else { return false }
        b = b.dropFirst()
        return (1...6).contains(b.count) && b.allSatisfy(digit)
    }

    /// Rounds to `places` fractional digits, ties to even.
    func roundedHalfEven(_ places: Int) -> FormRational {
        let scale = FormBigInt.pow10(places)
        let sn = num * scale
        var (q, m) = sn.floorDivMod(den)
        let twice = m + m
        if twice > den || (twice == den && !q.isEven) { q = q + .one }
        return FormRational(q, scale)
    }

    /// Canonical text: rounded half-even to six places, no exponent, no
    /// trailing zeros, "0" rather than "-0".
    var text: String {
        let r = roundedHalfEven(6)
        let scale = FormBigInt.pow10(6)
        var scaled = r.num * scale.quotientAndRemainder(dividingBy: r.den).0
        let neg = scaled.signum < 0
        if neg { scaled = -scaled }
        var s = scaled.description
        if s.count < 7 { s = String(repeating: "0", count: 7 - s.count) + s }
        let intPart = s.dropLast(6)
        var frac = s.suffix(6)
        while frac.last == "0" { frac = frac.dropLast() }
        let out = frac.isEmpty ? String(intPart) : String(intPart) + "." + String(frac)
        if out == "0" { return "0" }
        return neg ? "-" + out : out
    }
}
