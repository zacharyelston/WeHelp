package tunnel_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestRelayLogsNoDeviceAddress is #42 for the Relay: phones talking to an
// office, and to an office that doesn't exist, leave log lines with office
// IDs and byte counts, never the phones' addresses.
func TestRelayLogsNoDeviceAddress(t *testing.T) {
	logs := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := newHarness(t)
	id := h.newOffice(t)
	h.reg.publish(t, 2, listed(id, h.relayID))
	eventually(t, "list v2", func() bool { return h.rl.Version() == 2 })
	runOffice(t, id)
	eventually(t, "office connected", func() bool { return h.rl.Connected(id.Registration.OfficeID) })

	var mu sync.Mutex
	var phoneAddrs []string
	c := h.phone(id.KeyPin())
	tr := c.Transport.(*http.Transport)
	d := &net.Dialer{}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := d.DialContext(ctx, network, addr)
		if err == nil {
			mu.Lock()
			phoneAddrs = append(phoneAddrs, conn.LocalAddr().String())
			mu.Unlock()
		}
		return conn, err
	}
	for i := 0; i < 3; i++ {
		if body, err := get(c, id.Registration.OfficeID); err != nil || !strings.Contains(body, "pong") {
			t.Fatalf("request %d: %q %v", i, body, err)
		}
		tr.CloseIdleConnections() // a new connection, so a "closed" line each time
	}
	_, _ = get(c, "00000000-0000-0000-0000-000000000000") // no such office
	tr.CloseIdleConnections()
	eventually(t, "connection logged", func() bool { return strings.Contains(logs.String(), "phone connection closed") })

	out := logs.String()
	if !strings.Contains(out, "bytes_up=") || !strings.Contains(out, id.Registration.OfficeID) {
		t.Errorf("expected office ID and byte counts in the log:\n%s", out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(phoneAddrs) == 0 {
		t.Fatal("no phone connections recorded; the check would pass vacuously")
	}
	for _, a := range phoneAddrs {
		_, port, _ := net.SplitHostPort(a)
		if strings.Contains(out, a) || strings.Contains(out, ":"+port+" ") || strings.Contains(out, ":"+port+"\n") {
			t.Errorf("a phone's address %s appears in the Relay log", a)
		}
	}
}
