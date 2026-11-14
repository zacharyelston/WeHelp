package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Push (#7): the Back Office asks its Relay to notify a device. It sends
// only the device token, the app's APNs environment and a fixed category;
// the Relay supplies the generic text. These match internal/relay.
const pushRequestType = "wehelp-push"

// PushRequest is one push. Category is one of relay.Categories: new_message,
// appointment_reminder, visit_starting, appointment_update or
// appointment_request.
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
	mu     sync.Mutex
	client *ssh.Client
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
