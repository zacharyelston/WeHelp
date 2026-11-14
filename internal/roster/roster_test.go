package roster

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"
)

func TestOpen(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	env, err := Sign(priv, Roster{OfficeID: "office-1", IssuedAt: now,
		Providers: []Provider{{ID: "p1", Name: "Dr. Ada", Email: "ada@demo.test", Role: "provider"}}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(pub, env, "office-1", now)
	if err != nil || len(r.Providers) != 1 || r.Providers[0].Name != "Dr. Ada" {
		t.Fatalf("open = %+v, %v", r, err)
	}
	if _, err := Open(otherPub, env, "office-1", now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("wrong key: %v", err)
	}
	if _, err := Open(pub, env, "office-2", now); !errors.Is(err, ErrWrongOffice) {
		t.Errorf("another office's roster: %v", err)
	}
	if _, err := Open(pub, env, "office-1", now.Add(10*time.Minute)); !errors.Is(err, ErrStale) {
		t.Errorf("replayed old roster: %v", err)
	}
	tampered := env
	tampered.Payload = env.Payload[:len(env.Payload)-4] + "AAAA"
	if _, err := Open(pub, tampered, "office-1", now); err == nil {
		t.Error("tampered payload accepted")
	}
}
