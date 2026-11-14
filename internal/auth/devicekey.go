package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"strconv"
	"time"
)

// Device-key proof of possession.
//
// A device (the patient's phone) holds a P-256 key in its Secure Enclave and
// registers the public key at enrollment. Refresh tokens issued after that are
// bound to the device: presenting one also requires a signature, made with the
// device key, over DeviceProofMessage. Refresh tokens are single-use, so a
// captured signature cannot be replayed once the token rotates.

// DeviceProof purposes. They are part of the signed message so a signature
// made for one purpose cannot be reused for another.
const (
	PurposeEnroll  = "enroll"
	PurposeRefresh = "refresh"
)

// MaxProofSkew bounds how far a proof's timestamp may differ from server time.
const MaxProofSkew = 5 * time.Minute

var (
	ErrBadDeviceKey   = errors.New("device public key must be a P-256 SubjectPublicKeyInfo")
	ErrBadProof       = errors.New("device signature does not verify")
	ErrProofTimestamp = errors.New("device proof timestamp out of range")
)

// DeviceProofMessage is the exact byte string a device signs.
func DeviceProofMessage(purpose, refreshToken string, unixSeconds int64) []byte {
	return []byte("wehelp-device-v1\n" + purpose + "\n" + refreshToken + "\n" +
		strconv.FormatInt(unixSeconds, 10))
}

// ParseDevicePublicKey accepts a DER SubjectPublicKeyInfo (what CryptoKit's
// P256 publicKey.derRepresentation produces) and requires curve P-256.
func ParseDevicePublicKey(der []byte) (*ecdsa.PublicKey, error) {
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, ErrBadDeviceKey
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, ErrBadDeviceKey
	}
	return ec, nil
}

// VerifyDeviceProof checks an ASN.1 DER ECDSA-SHA256 signature (CryptoKit's
// signature.derRepresentation) over DeviceProofMessage.
func VerifyDeviceProof(pubDER []byte, purpose, refreshToken string, unixSeconds int64, sigDER []byte, now time.Time) error {
	pub, err := ParseDevicePublicKey(pubDER)
	if err != nil {
		return err
	}
	ts := time.Unix(unixSeconds, 0)
	if ts.Before(now.Add(-MaxProofSkew)) || ts.After(now.Add(MaxProofSkew)) {
		return ErrProofTimestamp
	}
	digest := sha256.Sum256(DeviceProofMessage(purpose, refreshToken, unixSeconds))
	if !ecdsa.VerifyASN1(pub, digest[:], sigDER) {
		return ErrBadProof
	}
	return nil
}
