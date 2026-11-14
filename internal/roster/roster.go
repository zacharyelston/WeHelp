// Package roster is the practice's provider roster (#62): the Registry keeps
// who works at each practice, and the practice's Back Office pulls it to
// invite their staff accounts.
//
// The Back Office signs each request with its office SSH key (the key the
// Registry pinned at install). The Registry answers with the roster signed by
// its ed25519 signing key (the key the Back Office pinned at install), for
// that office only. Names, emails, credentials and licence numbers: business
// data, never patient data, and never staff passwords or accounts.
//
// It has no database or Back Office dependencies, so both binaries import it.
package roster

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zacharyelston/wehelp/internal/officelist"
	"golang.org/x/crypto/ssh"
)

// SignatureHeader carries the office's SSH signature over the request body.
const SignatureHeader = "X-WeHelp-Office-Signature"

// MaxSkew bounds how far a request or roster time may be from now.
const MaxSkew = 2 * time.Minute

// Request is what the Back Office signs.
type Request struct {
	OfficeID string `json:"office_id"`
	At       int64  `json:"at"` // Unix seconds
}

// Provider is one person on the roster. Removed ones stay on it, with
// RemovedAt set, so the Back Office can flag their account.
type Provider struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Email         string     `json:"email"`
	Role          string     `json:"role"` // provider or admin
	Credentials   string     `json:"credentials"`
	LicenceNumber string     `json:"licence_number"`
	RemovedAt     *time.Time `json:"removed_at,omitempty"`
}

// Roster is the signed payload.
type Roster struct {
	OfficeID  string     `json:"office_id"`
	IssuedAt  time.Time  `json:"issued_at"`
	Providers []Provider `json:"providers"`
}

var (
	ErrBadSignature = errors.New("roster: signature does not verify")
	ErrWrongOffice  = errors.New("roster: issued for another office")
	ErrStale        = errors.New("roster: issued too far from now")
)

// Sign marshals r and signs it with the Registry key.
func Sign(priv ed25519.PrivateKey, r Roster) (officelist.Envelope, error) {
	if r.Providers == nil {
		r.Providers = []Provider{}
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return officelist.Envelope{}, err
	}
	return officelist.Seal(payload, ed25519.Sign(priv, payload)), nil
}

// Open verifies the envelope with the pinned Registry key and checks that
// the roster is for this office and fresh.
func Open(pub ed25519.PublicKey, e officelist.Envelope, officeID string, now time.Time) (Roster, error) {
	payload, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return Roster{}, fmt.Errorf("roster: payload: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil {
		return Roster{}, fmt.Errorf("roster: signature: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, payload, sig) {
		return Roster{}, ErrBadSignature
	}
	var r Roster
	if err := json.Unmarshal(payload, &r); err != nil {
		return Roster{}, fmt.Errorf("roster: parse: %w", err)
	}
	if r.OfficeID != officeID {
		return Roster{}, ErrWrongOffice
	}
	if d := now.Sub(r.IssuedAt); d > MaxSkew || d < -MaxSkew {
		return Roster{}, ErrStale
	}
	return r, nil
}

// SignRequest returns the request body and its signature header value.
func SignRequest(officeKey ed25519.PrivateKey, officeID string, now time.Time) ([]byte, string, error) {
	body, err := json.Marshal(Request{OfficeID: officeID, At: now.Unix()})
	if err != nil {
		return nil, "", err
	}
	signer, err := ssh.NewSignerFromKey(officeKey)
	if err != nil {
		return nil, "", err
	}
	sig, err := signer.Sign(rand.Reader, body)
	if err != nil {
		return nil, "", err
	}
	return body, base64.StdEncoding.EncodeToString(ssh.Marshal(sig)), nil
}

// Client fetches the roster for one installed office.
type Client struct {
	HTTP        *http.Client
	RegistryURL string
	OfficeID    string
	OfficeKey   ed25519.PrivateKey
	RegistryKey ed25519.PublicKey
}

// Fetch asks the Registry for the roster and verifies the answer.
func (c *Client) Fetch(ctx context.Context) (Roster, error) {
	body, sig, err := SignRequest(c.OfficeKey, c.OfficeID, time.Now())
	if err != nil {
		return Roster{}, err
	}
	url := strings.TrimRight(c.RegistryURL, "/") + "/v1/roster"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Roster{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SignatureHeader, sig)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Roster{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Roster{}, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return Roster{}, fmt.Errorf("roster: Registry answered HTTP %d %s", resp.StatusCode, e.Error)
	}
	var env officelist.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Roster{}, fmt.Errorf("roster: response: %w", err)
	}
	return Open(c.RegistryKey, env, c.OfficeID, time.Now())
}
