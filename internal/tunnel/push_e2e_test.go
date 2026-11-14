package tunnel_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zacharyelston/wehelp/internal/relay"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

type apnsRecorder struct {
	mu     sync.Mutex
	paths  []string
	bodies []string
}

func (a *apnsRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	a.paths = append(a.paths, r.URL.Path)
	a.bodies = append(a.bodies, string(b))
	a.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// TestPushThroughRelay is #7 end to end: the office asks over its SSH
// tunnel, the Relay checks the category and sends the fixed text to APNs.
func TestPushThroughRelay(t *testing.T) {
	rec := &apnsRecorder{}
	srv := httptest.NewUnstartedServer(rec)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	apns := relay.NewAPNs(key, "KID", "TEAM", "com.zacelston.wehelp")
	apns.Client = srv.Client()
	apns.Hosts = map[string]string{"sandbox": srv.URL, "production": srv.URL}

	h := newHarnessWith(t, apns)
	id := h.newOffice(t)
	h.reg.publish(t, 2, listed(id, h.relayID))
	eventually(t, "list v2", func() bool { return h.rl.Version() == 2 })

	pusher := &tunnel.Pusher{}
	if _, err := pusher.Push(context.Background(), tunnel.PushRequest{}); !errors.Is(err, tunnel.ErrNotConnected) {
		t.Errorf("push before connecting: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = tunnel.Run(ctx, id, http.NewServeMux(), tunnel.Options{MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, Keepalive: time.Second, Pusher: pusher})
	}()
	eventually(t, "office connected", func() bool { return h.rl.Connected(id.Registration.OfficeID) })

	const token = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	var res string
	eventually(t, "push sent", func() bool {
		res, _ = pusher.Push(context.Background(), tunnel.PushRequest{Token: token, Environment: "sandbox", Category: "new_message"})
		return res == tunnel.PushSent
	})
	if len(rec.paths) != 1 || rec.paths[0] != "/3/device/"+token {
		t.Fatalf("APNs saw %v", rec.paths)
	}
	if !strings.Contains(rec.bodies[0], relay.Categories["new_message"].Body) {
		t.Errorf("payload %s", rec.bodies[0])
	}
	// Anything outside the fixed categories never reaches Apple.
	res, err := pusher.Push(context.Background(), tunnel.PushRequest{Token: token, Environment: "sandbox", Category: "Your results are ready"})
	if err != nil || res != relay.PushRejected {
		t.Errorf("free-text category: %s %v", res, err)
	}
	if len(rec.paths) != 1 {
		t.Errorf("rejected push reached APNs")
	}
}
