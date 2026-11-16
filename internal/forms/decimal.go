package forms

import (
	"math/big"
	"regexp"
	"strings"
)

// Numbers in a definition or an answer are finite base-10 values with at
// most 12 integer digits and 6 fractional digits, and no exponent.
var numPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,11})(\.[0-9]{1,6})?$`)

// intPattern is an integer answer or bound: no fractional part at all.
var intPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,11})$`)

// maxBits bounds every arithmetic result: numerator and denominator of the
// normalised rational must each fit in 128 bits, or scoring fails with an
// overflow error.
const maxBits = 128

// outputPlaces is the precision of a score's reported value. Arithmetic is
// exact; only the reported string is rounded (half-even) to this many places.
const outputPlaces = 6

func parseDecimal(s string) (*big.Rat, bool) {
	if !numPattern.MatchString(s) {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	return r, ok
}

func inBounds(r *big.Rat) bool {
	return r.Num().BitLen() <= maxBits && r.Denom().BitLen() <= maxBits
}

var (
	bigOne = big.NewInt(1)
	bigTwo = big.NewInt(2)
	bigTen = big.NewInt(10)
)

// roundHalfEven rounds x to places fractional digits, ties to even.
func roundHalfEven(x *big.Rat, places int) *big.Rat {
	scale := new(big.Int).Exp(bigTen, big.NewInt(int64(places)), nil)
	s := new(big.Rat).Mul(x, new(big.Rat).SetInt(scale))
	q, m := new(big.Int), new(big.Int)
	q.DivMod(s.Num(), s.Denom(), m) // Euclidean: m >= 0, so q = floor(s)
	c := new(big.Int).Lsh(m, 1).Cmp(s.Denom())
	if c > 0 || (c == 0 && new(big.Int).Rem(q, bigTwo).Sign() != 0) {
		q.Add(q, bigOne)
	}
	return new(big.Rat).SetFrac(q, scale)
}

// formatDecimal renders x as a canonical decimal string: rounded half-even
// to six places, no exponent, no trailing zeros, "0" rather than "-0".
func formatDecimal(x *big.Rat) string {
	s := roundHalfEven(x, outputPlaces).FloatString(outputPlaces)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	if s == "-0" {
		s = "0"
	}
	return s
}
