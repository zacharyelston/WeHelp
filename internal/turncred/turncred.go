// Package turncred is how video-visit TURN credentials are made and checked
// (#6). The Relay holds one master secret and never sends it anywhere. Each
// office gets its own key, derived from the master secret, as an update when
// its tunnel connects; the office then mints per-visit credentials itself
// and never asks the Relay for anything.
//
// The key ID is opaque, so no office ID travels in a TURN username. The
// credentials are standard TURN REST ones (draft-uberti-behave-turn-rest):
// username "expiry:keyID:token", password base64(HMAC-SHA1(secret,
// username)), so libwebrtc needs nothing special.
//
// No database or Back Office dependencies: both binaries import it.
package turncred

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // TURN REST passwords are HMAC-SHA1 by definition
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Realm is the TURN realm the Relay serves.
const Realm = "wehelp"

// RequestType is the SSH request the Relay sends an office, carrying its Key
// as JSON, when the office's tunnel connects.
const RequestType = "wehelp-turn-key@wehelp"

// Key is what the Relay sends an office: its key ID, its secret, and the
// TURN URLs phones should use.
type Key struct {
	KeyID  string   `json:"key_id"`
	Secret string   `json:"secret"`
	URLs   []string `json:"urls"`
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// KeyID is the office's opaque key ID under master.
func KeyID(master []byte, officeID string) string {
	m := hmac.New(sha256.New, master)
	m.Write([]byte("wehelp-turn-key-id\x00" + officeID))
	return strings.ToLower(b32.EncodeToString(m.Sum(nil)[:10]))
}

// Secret is the per-office secret for keyID under master.
func Secret(master []byte, keyID string) string {
	m := hmac.New(sha256.New, master)
	m.Write([]byte("wehelp-turn-secret\x00" + keyID))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// Mint returns credentials for one visit token, valid until expires.
func Mint(k Key, token string, expires time.Time) (username, password string) {
	username = strconv.FormatInt(expires.Unix(), 10) + ":" + k.KeyID + ":" + token
	return username, Password(k.Secret, username)
}

// Password is the TURN REST password for username under secret.
func Password(secret, username string) string {
	m := hmac.New(sha1.New, []byte(secret))
	m.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// Username is a parsed credential username.
type Username struct {
	Expires time.Time
	KeyID   string
	Token   string
}

var ErrBadUsername = errors.New("turncred: malformed username")

// Parse splits "expiry:keyID:token".
func Parse(username string) (Username, error) {
	parts := strings.Split(username, ":")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" || len(parts[2]) > 64 {
		return Username{}, ErrBadUsername
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || exp <= 0 {
		return Username{}, ErrBadUsername
	}
	return Username{Expires: time.Unix(exp, 0), KeyID: parts[1], Token: parts[2]}, nil
}
