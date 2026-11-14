package relay

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeAPNs records requests and answers with a fixed status and reason.
type fakeAPNs struct {
	mu     sync.Mutex
	status int
	reason string
	got    []*http.Request
	bodies []string
}

func (f *fakeAPNs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.got = append(f.got, r)
	f.bodies = append(f.bodies, string(b))
	status, reason := f.status, f.reason
	f.mu.Unlock()
	w.WriteHeader(status)
	if reason != "" {
		_ = json.NewEncoder(w).Encode(map[string]string{"reason": reason})
	}
}

// newFakeAPNs starts an HTTP/2 TLS server and an APNs sender pointed at it.
func newFakeAPNs(t *testing.T) (*fakeAPNs, *APNs, *ecdsa.PrivateKey) {
	t.Helper()
	f := &fakeAPNs{status: http.StatusOK}
	srv := httptest.NewUnstartedServer(f)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	a := NewAPNs(key, "KEY123", "TEAM456", "com.zacelston.wehelp")
	a.Client = srv.Client()
	a.Hosts = map[string]string{"sandbox": srv.URL, "production": srv.URL}
	return f, a, key
}

const testToken = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

func TestAPNsSend(t *testing.T) {
	f, a, key := newFakeAPNs(t)
	res, err := a.Send(context.Background(), PushRequest{Token: strings.ToUpper(testToken), Environment: "sandbox", Category: "new_message"})
	if err != nil || res != PushSent {
		t.Fatalf("send: %s %v", res, err)
	}
	r := f.got[0]
	if r.ProtoMajor != 2 {
		t.Errorf("APNs needs HTTP/2, got %s", r.Proto)
	}
	if r.URL.Path != "/3/device/"+testToken {
		t.Errorf("path %s", r.URL.Path)
	}
	if r.Header.Get("apns-topic") != "com.zacelston.wehelp" || r.Header.Get("apns-push-type") != "alert" {
		t.Errorf("headers %v", r.Header)
	}
	// The provider token is ES256, signed by the key, with kid and iss.
	tok, err := jwt.Parse(strings.TrimPrefix(r.Header.Get("authorization"), "bearer "), func(tk *jwt.Token) (any, error) {
		return &key.PublicKey, nil
	}, jwt.WithValidMethods([]string{"ES256"}))
	if err != nil || tok.Header["kid"] != "KEY123" || tok.Claims.(jwt.MapClaims)["iss"] != "TEAM456" {
		t.Fatalf("jwt: %v %v", err, tok)
	}
	// Fixed, generic text only.
	var body struct {
		APS struct {
			Alert    struct{ Title, Body string } `json:"alert"`
			Category string                       `json:"category"`
		} `json:"aps"`
	}
	if err := json.Unmarshal([]byte(f.bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body.APS.Alert.Body != Categories["new_message"].Body || body.APS.Category != "new_message" {
		t.Errorf("payload %s", f.bodies[0])
	}
	// The JWT is reused, not re-signed per push.
	a.Send(context.Background(), PushRequest{Token: testToken, Environment: "sandbox", Category: "appointment_reminder"})
	if f.got[1].Header.Get("authorization") != r.Header.Get("authorization") {
		t.Error("provider token re-signed for every push")
	}
}

func TestAPNsRefusalsAndErrors(t *testing.T) {
	f, a, _ := newFakeAPNs(t)
	for _, bad := range []PushRequest{
		{Token: testToken, Environment: "sandbox", Category: "lab_results_ready"},
		{Token: testToken, Environment: "sandbox", Category: "Your test results: positive"},
		{Token: "not-hex", Environment: "sandbox", Category: "new_message"},
		{Token: testToken, Environment: "staging", Category: "new_message"},
	} {
		if res, _ := a.Send(context.Background(), bad); res != PushRejected {
			t.Errorf("%+v: %s, want rejected", bad, res)
		}
	}
	if len(f.got) != 0 {
		t.Fatalf("refused pushes reached APNs: %d", len(f.got))
	}
	f.status, f.reason = http.StatusGone, "Unregistered"
	if res, _ := a.Send(context.Background(), PushRequest{Token: testToken, Environment: "sandbox", Category: "new_message"}); res != PushInvalidToken {
		t.Errorf("410: %s", res)
	}
	f.status, f.reason = http.StatusBadRequest, "BadDeviceToken"
	if res, _ := a.Send(context.Background(), PushRequest{Token: testToken, Environment: "sandbox", Category: "new_message"}); res != PushInvalidToken {
		t.Errorf("BadDeviceToken: %s", res)
	}
	f.status, f.reason = http.StatusServiceUnavailable, "ServiceUnavailable"
	if res, _ := a.Send(context.Background(), PushRequest{Token: testToken, Environment: "sandbox", Category: "new_message"}); res != PushFailed {
		t.Errorf("503: %s", res)
	}
}

func TestPushRateLimit(t *testing.T) {
	r := &Relay{cfg: Config{PushPerMin: 3}, pushes: map[string][]time.Time{}}
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !r.allowPush("o1", now) {
			t.Fatalf("push %d refused", i)
		}
	}
	if r.allowPush("o1", now) {
		t.Error("4th push in a minute allowed")
	}
	if !r.allowPush("o2", now) {
		t.Error("another office limited")
	}
	if !r.allowPush("o1", now.Add(61*time.Second)) {
		t.Error("limit didn't reset after a minute")
	}
}
