package relay

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"golang.org/x/crypto/ssh"
)

// PushRequestType is the SSH global request an office sends to ask for a
// push (#7). The payload is a JSON PushRequest; the reply payload is one of
// the Push* results.
const PushRequestType = "wehelp-push"

// officeRequests answers an office's global requests: pushes, and replies
// to keepalives. Everything else is refused.
func (r *Relay) officeRequests(officeID string, reqs <-chan *ssh.Request) {
	for req := range reqs {
		if req.Type != PushRequestType {
			if req.WantReply {
				_ = req.Reply(req.Type == "keepalive@openssh.com", nil)
			}
			continue
		}
		result := r.handlePush(officeID, req.Payload)
		if req.WantReply {
			_ = req.Reply(result == PushSent, []byte(result))
		}
	}
}

func (r *Relay) handlePush(officeID string, payload []byte) string {
	var p PushRequest
	if len(payload) > 4<<10 || json.Unmarshal(payload, &p) != nil {
		return PushRejected
	}
	if err := p.Validate(); err != nil {
		slog.Warn("relay: push refused", "office", officeID, "reason", err.Error())
		return PushRejected
	}
	if r.cfg.APNs == nil {
		return PushFailed
	}
	if !r.allowPush(officeID, time.Now()) {
		slog.Warn("relay: push rate limited", "office", officeID)
		return PushRateLimited
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := r.cfg.APNs.Send(ctx, p)
	// Office ID, category and result only: never the device token.
	if err != nil {
		slog.Warn("relay: push", "office", officeID, "category", p.Category, "result", result, "err", err.Error())
	} else {
		slog.Info("relay: push", "office", officeID, "category", p.Category, "result", result)
	}
	return result
}

// allowPush applies a per-office sliding one-minute limit.
func (r *Relay) allowPush(officeID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := now.Add(-time.Minute)
	recent := r.pushes[officeID][:0]
	for _, t := range r.pushes[officeID] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= r.cfg.PushPerMin {
		r.pushes[officeID] = recent
		return false
	}
	r.pushes[officeID] = append(recent, now)
	return true
}
