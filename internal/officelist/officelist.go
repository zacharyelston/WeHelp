// Package officelist is the signed list of offices a Relay may accept (#60).
// The Registry signs it; Relays verify it (#4). It has no database or
// Back Office dependencies, so both binaries can import it.
//
// Rules borrowed from TUF: an ed25519 signature over the exact payload bytes,
// a version that only goes up, and callers keep the last good list.
package officelist

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Office is one entry: an office allowed to connect to its assigned Relay.
// Suspended practices are left out of the list entirely.
type Office struct {
	OfficeID     string `json:"office_id"`
	SSHPublicKey string `json:"ssh_public_key"` // authorized_keys line
	RelayID      string `json:"relay_id"`
}

// List is the signed payload.
type List struct {
	Version  int64     `json:"version"`
	IssuedAt time.Time `json:"issued_at"`
	Offices  []Office  `json:"offices"`
}

// Envelope is what the Registry serves: the payload bytes and their
// signature, both base64 (standard encoding).
type Envelope struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

var (
	ErrBadSignature = errors.New("officelist: signature does not verify")
	ErrRollback     = errors.New("officelist: version is not newer than the last good list")
)

// Sign marshals l and signs the resulting bytes.
func Sign(priv ed25519.PrivateKey, l List) (payload, sig []byte, err error) {
	if l.Version <= 0 {
		return nil, nil, fmt.Errorf("officelist: version must be positive")
	}
	if l.Offices == nil {
		l.Offices = []Office{}
	}
	payload, err = json.Marshal(l)
	if err != nil {
		return nil, nil, err
	}
	return payload, ed25519.Sign(priv, payload), nil
}

// Seal wraps signed bytes into an Envelope.
func Seal(payload, sig []byte) Envelope {
	return Envelope{
		Payload:   base64.StdEncoding.EncodeToString(payload),
		Signature: base64.StdEncoding.EncodeToString(sig),
	}
}

// Verify checks the envelope's signature with pub and returns the list.
// The signature is checked before the payload is parsed. It does not apply
// the rollback rule; see Open.
func Verify(pub ed25519.PublicKey, e Envelope) (List, error) {
	payload, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return List{}, fmt.Errorf("officelist: payload: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil {
		return List{}, fmt.Errorf("officelist: signature: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, payload, sig) {
		return List{}, ErrBadSignature
	}
	var l List
	if err := json.Unmarshal(payload, &l); err != nil {
		return List{}, fmt.Errorf("officelist: parse: %w", err)
	}
	if l.Version <= 0 {
		return List{}, fmt.Errorf("officelist: version must be positive")
	}
	return l, nil
}

// Open verifies the envelope like Verify and also rejects any version not
// greater than lastGood (pass 0 when there is no previous list).
func Open(pub ed25519.PublicKey, e Envelope, lastGood int64) (List, error) {
	l, err := Verify(pub, e)
	if err != nil {
		return List{}, err
	}
	if l.Version <= lastGood {
		return List{}, ErrRollback
	}
	return l, nil
}

// PublicKeyString encodes a Registry signing key for distribution and
// pinning: "ed25519:" followed by the base64 raw key.
func PublicKeyString(pub ed25519.PublicKey) string {
	return "ed25519:" + base64.StdEncoding.EncodeToString(pub)
}

// ParsePublicKey reverses PublicKeyString.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b64, ok := strings.CutPrefix(s, "ed25519:")
	if !ok {
		return nil, errors.New("officelist: key must start with ed25519:")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("officelist: malformed ed25519 key")
	}
	return ed25519.PublicKey(raw), nil
}
