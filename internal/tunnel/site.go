package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"golang.org/x/crypto/ssh"
)

// Website publishing (#97). The Back Office opens a "wehelp-site" channel on
// its SSH connection, writes the site as one tar, closes its side and reads
// one JSON reply. The Relay files the site under the office ID this SSH
// connection authenticated as, so no other office can write it. Must match
// internal/relay.
const siteChannelType = "wehelp-site"

// SiteResult is the Relay's reply to a publish.
type SiteResult struct {
	OK    bool   `json:"ok"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
	Path  string `json:"path"`  // e.g. /s/<office-id>/
	URL   string `json:"url"`   // full public URL, when the Relay knows its public address
	Error string `json:"error"` // why the Relay refused it
}

// PublishSite sends one site bundle over the live connection.
func (p *Pusher) PublishSite(ctx context.Context, bundle []byte) (SiteResult, error) {
	p.mu.Lock()
	c := p.client
	p.mu.Unlock()
	if c == nil {
		return SiteResult{}, ErrNotConnected
	}
	type outcome struct {
		res SiteResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		var o outcome
		defer func() { done <- o }()
		ch, reqs, err := c.OpenChannel(siteChannelType, nil)
		if err != nil {
			o.err = err
			return
		}
		go ssh.DiscardRequests(reqs)
		defer ch.Close()
		if _, o.err = ch.Write(bundle); o.err != nil {
			return
		}
		if o.err = ch.CloseWrite(); o.err != nil {
			return
		}
		raw, err := io.ReadAll(io.LimitReader(ch, 4<<10))
		if err != nil {
			o.err = err
			return
		}
		if o.err = json.Unmarshal(raw, &o.res); o.err != nil {
			o.err = errors.New("tunnel: unreadable reply from the relay")
		}
	}()
	select {
	case <-ctx.Done():
		return SiteResult{}, ctx.Err()
	case o := <-done:
		if o.err == nil && !o.res.OK {
			o.err = errors.New("relay refused the site: " + o.res.Error)
		}
		return o.res, o.err
	}
}
