package relay

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/zacharyelston/wehelp/internal/keyfile"
)

// Push (#7). The Relay sends Apple push notifications for offices, because
// the APNs key belongs to the app publisher and can't be given to every
// office. Offices send only (device token, category); the Relay supplies the
// fixed, generic text, so a push can never carry patient information. The
// app fetches the details through its pinned connection.

// Categories are the only pushes there are, with their fixed text.
var Categories = map[string]struct{ Title, Body string }{
	"new_message":          {"WeHelp", "You have a new message from your practice."},
	"appointment_reminder": {"WeHelp", "You have an upcoming appointment."},
	"visit_starting":       {"WeHelp", "Your video visit is starting."},
	"appointment_update":   {"WeHelp", "Your appointment request has an update."}, // #68, to the patient
	"appointment_request":  {"WeHelp", "A patient has requested an appointment."}, // #68, to the provider
}

// APNs hosts.
const (
	APNsSandbox    = "https://api.sandbox.push.apple.com"
	APNsProduction = "https://api.push.apple.com"
)

// PushRequest is what an office sends over its SSH connection.
type PushRequest struct {
	Token       string `json:"token"`       // hex APNs device token
	Environment string `json:"environment"` // sandbox or production (the app's build)
	Category    string `json:"category"`
}

// Push results returned to the office.
const (
	PushSent         = "sent"
	PushInvalidToken = "invalid_token" // the office should drop this token
	PushRejected     = "rejected"      // bad request: unknown category, malformed token
	PushFailed       = "failed"        // try later
	PushRateLimited  = "rate_limited"
)

// APNs sends pushes with token-based (.p8) authentication.
type APNs struct {
	KeyID, TeamID, Topic string
	key                  *ecdsa.PrivateKey
	Client               *http.Client
	// Hosts by environment; tests point these at a local server.
	Hosts map[string]string

	mu       sync.Mutex
	jwt      string
	jwtIssue time.Time
}

// LoadAPNs reads a .p8 auth key. The key file must not be readable by
// group or others.
func LoadAPNs(keyPath, keyID, teamID, topic string) (*APNs, error) {
	if err := keyfile.CheckPrivate(keyPath); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("apns: key is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apns: key: %w", err)
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apns: key is not an EC key")
	}
	return NewAPNs(ek, keyID, teamID, topic), nil
}

// NewAPNs builds a sender from a parsed key.
func NewAPNs(key *ecdsa.PrivateKey, keyID, teamID, topic string) *APNs {
	return &APNs{KeyID: keyID, TeamID: teamID, Topic: topic, key: key,
		Client: &http.Client{Timeout: 15 * time.Second},
		Hosts:  map[string]string{"sandbox": APNsSandbox, "production": APNsProduction}}
}

// token returns a provider JWT, renewed every 50 minutes (Apple accepts
// 20–60 minutes).
func (a *APNs) token(now time.Time) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.jwt != "" && now.Sub(a.jwtIssue) < 50*time.Minute {
		return a.jwt, nil
	}
	t := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": a.TeamID, "iat": now.Unix()})
	t.Header["kid"] = a.KeyID
	s, err := t.SignedString(a.key)
	if err != nil {
		return "", err
	}
	a.jwt, a.jwtIssue = s, now
	return s, nil
}

// Validate checks a request before anything is sent to Apple.
func (r PushRequest) Validate() error {
	if _, ok := Categories[r.Category]; !ok {
		return fmt.Errorf("unknown category %q", r.Category)
	}
	if r.Environment != "sandbox" && r.Environment != "production" {
		return fmt.Errorf("environment must be sandbox or production")
	}
	if b, err := hex.DecodeString(r.Token); err != nil || len(b) < 32 || len(b) > 100 {
		return errors.New("token must be a hex APNs device token")
	}
	return nil
}

// Send delivers one push and maps Apple's answer to a result for the office.
func (a *APNs) Send(ctx context.Context, r PushRequest) (string, error) {
	if err := r.Validate(); err != nil {
		return PushRejected, err
	}
	c := Categories[r.Category]
	payload, _ := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert":    map[string]string{"title": c.Title, "body": c.Body},
			"sound":    "default",
			"category": r.Category,
		},
	})
	jwtStr, err := a.token(time.Now())
	if err != nil {
		return PushFailed, err
	}
	url := a.Hosts[r.Environment] + "/3/device/" + strings.ToLower(r.Token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return PushFailed, err
	}
	req.Header.Set("authorization", "bearer "+jwtStr)
	req.Header.Set("apns-topic", a.Topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("apns-expiration", fmt.Sprint(time.Now().Add(24*time.Hour).Unix()))
	resp, err := a.Client.Do(req)
	if err != nil {
		return PushFailed, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode == http.StatusOK {
		return PushSent, nil
	}
	var e struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(body, &e)
	switch {
	case resp.StatusCode == http.StatusGone, e.Reason == "BadDeviceToken", e.Reason == "Unregistered", e.Reason == "DeviceTokenNotForTopic":
		return PushInvalidToken, fmt.Errorf("apns %d %s", resp.StatusCode, e.Reason)
	case resp.StatusCode == http.StatusTooManyRequests:
		return PushRateLimited, fmt.Errorf("apns %d %s", resp.StatusCode, e.Reason)
	case resp.StatusCode >= 500:
		return PushFailed, fmt.Errorf("apns %d %s", resp.StatusCode, e.Reason)
	}
	return PushRejected, fmt.Errorf("apns %d %s", resp.StatusCode, e.Reason)
}
