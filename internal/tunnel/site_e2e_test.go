package tunnel_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zacharyelston/wehelp/internal/site"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

// TestPublishSiteThroughRelay is #97 end to end: the office renders its site,
// streams it over a "wehelp-site" channel on its SSH tunnel, and the Relay
// serves it under that office's ID, and only while the office is listed.
func TestPublishSiteThroughRelay(t *testing.T) {
	h := newHarnessWith(t, nil)
	id := h.newOffice(t)
	officeID := id.Registration.OfficeID
	h.reg.publish(t, 2, listed(id, h.relayID))
	eventually(t, "list v2", func() bool { return h.rl.Version() == 2 })

	pub := &tunnel.Pusher{}
	if _, err := pub.PublishSite(context.Background(), nil); !errors.Is(err, tunnel.ErrNotConnected) {
		t.Errorf("publish before connecting: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = tunnel.Run(ctx, id, http.NewServeMux(), tunnel.Options{MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, Keepalive: time.Second, Pusher: pub})
	}()
	eventually(t, "office connected", func() bool { return h.rl.Connected(officeID) })

	bundle, n, err := site.Bundle(site.DemoContent("Demo Clinic"), site.DemoProducts(),
		[]site.File{{Kind: "library", Title: "Guide", Filename: "guide.pdf", Data: []byte("%PDF-1.4")}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var res tunnel.SiteResult
	eventually(t, "site published", func() bool {
		res, err = pub.PublishSite(context.Background(), bundle)
		return err == nil
	})
	if !res.OK || res.Files != n || res.Path != "/s/"+officeID+"/" || res.URL != "https://relay.test:8444/s/"+officeID+"/" {
		t.Fatalf("publish result %+v", res)
	}

	web := httptest.NewServer(h.rl.SiteHandler())
	t.Cleanup(web.Close)
	get := func(path string) (int, string) {
		resp, err := http.Get(web.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get(res.Path + "store.html"); code != 200 || !strings.Contains(body, "Morning Greens Blend") {
		t.Fatalf("store page: %d", code)
	}
	if code, body := get(res.Path + "files/guide.pdf"); code != 200 || body != "%PDF-1.4" {
		t.Errorf("download: %d %q", code, body)
	}

	// A publish straight after another is throttled; the live site stays.
	if _, err := pub.PublishSite(context.Background(), bundle); err == nil || !strings.Contains(err.Error(), "too often") {
		t.Errorf("second publish within the gap: %v", err)
	}
	if code, _ := get(res.Path); code != 200 {
		t.Errorf("site after a refused publish: %d", code)
	}

	// Suspending the practice takes the site down with the next list.
	h.reg.publish(t, 3)
	eventually(t, "list v3", func() bool { return h.rl.Version() == 3 })
	if code, _ := get(res.Path); code != http.StatusNotFound {
		t.Errorf("suspended office's site: %d", code)
	}
}
