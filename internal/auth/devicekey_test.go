package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"testing"
	"time"
)

func newDevice(t *testing.T, curve elliptic.Curve) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key, der
}

func sign(t *testing.T, key *ecdsa.PrivateKey, purpose, token string, ts int64) []byte {
	t.Helper()
	d := sha256.Sum256(DeviceProofMessage(purpose, token, ts))
	sig, err := ecdsa.SignASN1(rand.Reader, key, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func TestDeviceProof(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key, pub := newDevice(t, elliptic.P256())
	_, otherPub := newDevice(t, elliptic.P256())
	ts := now.Unix()
	sig := sign(t, key, PurposeRefresh, "tok", ts)

	cases := []struct {
		name    string
		pub     []byte
		purpose string
		token   string
		ts      int64
		sig     []byte
		want    error
	}{
		{"valid", pub, PurposeRefresh, "tok", ts, sig, nil},
		{"wrong key", otherPub, PurposeRefresh, "tok", ts, sig, ErrBadProof},
		{"wrong purpose", pub, PurposeEnroll, "tok", ts, sig, ErrBadProof},
		{"wrong token", pub, PurposeRefresh, "other", ts, sig, ErrBadProof},
		{"tampered ts", pub, PurposeRefresh, "tok", ts + 1, sig, ErrBadProof},
		{"stale", pub, PurposeRefresh, "tok", ts - 600, sign(t, key, PurposeRefresh, "tok", ts-600), ErrProofTimestamp},
		{"future", pub, PurposeRefresh, "tok", ts + 600, sign(t, key, PurposeRefresh, "tok", ts+600), ErrProofTimestamp},
		{"garbage key", []byte("nope"), PurposeRefresh, "tok", ts, sig, ErrBadDeviceKey},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := VerifyDeviceProof(c.pub, c.purpose, c.token, c.ts, c.sig, now)
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestDeviceKeyRejectsOtherCurves(t *testing.T) {
	_, pub := newDevice(t, elliptic.P384())
	if _, err := ParseDevicePublicKey(pub); !errors.Is(err, ErrBadDeviceKey) {
		t.Fatalf("P-384 accepted: %v", err)
	}
}
