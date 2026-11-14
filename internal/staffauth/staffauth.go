// Package staffauth holds the pieces of staff sign-in shared by the Back
// Office staff UI (#38) and the Registry console (#61): TOTP codes that work
// once, random session tokens stored only as hashes, and QR images for
// enrolling an authenticator. It holds no data and imports no data package.
package staffauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"rsc.io/qr"
)

// TOTPPeriod is the code step in seconds (RFC 6238 default).
const TOTPPeriod = 30

// CheckTOTP accepts a 6-digit code for the current step or one step either
// side, but never a step at or before lastStep, so each code works once. It
// returns the step to store as the new lastStep.
func CheckTOTP(secret, code string, lastStep *int64, now time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0, false
	}
	step := now.Unix() / TOTPPeriod
	for _, d := range []int64{-1, 0, 1} {
		st := step + d
		if lastStep != nil && st <= *lastStep {
			continue
		}
		want, err := totp.GenerateCodeCustom(secret, time.Unix(st*TOTPPeriod, 0), totp.ValidateOpts{
			Period: TOTPPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return st, true
		}
	}
	return 0, false
}

// NewTOTPKey makes a new authenticator secret for issuer and account.
func NewTOTPKey(issuer, account string) (*otp.Key, error) {
	return totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: account,
		Period: TOTPPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
}

// RandomToken returns n random bytes, base64url-encoded.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken is what gets stored for a session or other bearer token.
func HashToken(t string) []byte {
	sum := sha256.Sum256([]byte(t))
	return sum[:]
}

// QRPNG renders text as a QR code PNG.
func QRPNG(text string) ([]byte, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, err
	}
	c.Scale = 8
	return c.PNG(), nil
}
