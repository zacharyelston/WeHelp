package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/zacharyelston/wehelp/internal/turncred"
	"golang.org/x/crypto/ssh"
)

// Push (#7): the Back Office asks its Relay to notify a device. It sends
// only the device token, the app's APNs environment and a fixed category;
// the Relay supplies the generic text. These match internal/relay.
const pushRequestType = "wehelp-push"

// PushRequest is one push. Category is one of relay.Categories: new_message,
// appointment_reminder, visit_starting, appointment_update,
// appointment_request or order_update.
type PushRequest struct {
	Token       string `json:"token"`
	Environment string `json:"environment"`
	Category    string `json:"category"`
}

// Push results from the Relay.
const (
	PushSent         = "sent"
	PushInvalidToken = "invalid_token" // drop this token
)

// ErrNotConnected means the tunnel to the Relay is down right now.
var ErrNotConnected = errors.New("tunnel: not connected to the relay")

// Pusher sends push requests over whichever SSH connection is live.
type Pusher struct {
	mu      sync.Mutex
	client  *ssh.Client
	turnKey *turncred.Key // from the Relay when the tunnel connects (#6); memory only
}

// setTURNKey stores the TURN key the Relay sent. A malformed one is ignored.
func (p *Pusher) setTURNKey(payload []byte) {
	var k turncred.Key
	if err := json.Unmarshal(payload, &k); err != nil || k.KeyID == "" || k.Secret == "" || len(k.URLs) == 0 {
		slog.Warn("tunnel: ignoring a malformed TURN key from the relay")
		return
	}
	p.mu.Lock()
	p.turnKey = &k
	p.mu.Unlock()
	slog.Info("tunnel: TURN key received", "urls", len(k.URLs))
}

// TURNKey is the office's TURN key, if the Relay has sent one. It stays
// valid across reconnects until the Relay's master secret changes, and the
// Relay sends the current one on every connect.
func (p *Pusher) TURNKey() (turncred.Key, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.turnKey == nil {
		return turncred.Key{}, false
	}
	return *p.turnKey, true
}

// Connected reports whether the tunnel to the Relay is up right now.
func (p *Pusher) Connected() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.client != nil
}

func (p *Pusher) set(c *ssh.Client) {
	p.mu.Lock()
	p.client = c
	p.mu.Unlock()
}

// Push sends one request and returns the Relay's result.
func (p *Pusher) Push(ctx context.Context, r PushRequest) (string, error) {
	p.mu.Lock()
	c := p.client
	p.mu.Unlock()
	if c == nil {
		return "", ErrNotConnected
	}
	body, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	type reply struct {
		payload []byte
		err     error
	}
	done := make(chan reply, 1)
	go func() {
		_, payload, err := c.SendRequest(pushRequestType, true, body)
		done <- reply{payload, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case rep := <-done:
		if rep.err != nil {
			return "", rep.err
		}
		return string(rep.payload), nil
	}
}
