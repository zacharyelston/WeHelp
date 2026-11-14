package officelist

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func TestSignOpen(t *testing.T) {
	priv := newKey(t)
	pub := priv.Public().(ed25519.PublicKey)
	in := List{Version: 3, IssuedAt: time.Unix(1_800_000_000, 0).UTC(),
		Offices: []Office{{OfficeID: "o1", SSHPublicKey: "ssh-ed25519 AAAA", RelayID: "r1"}}}
	payload, sig, err := Sign(priv, in)
	if err != nil {
		t.Fatal(err)
	}
	env := Seal(payload, sig)

	got, err := Open(pub, env, 2)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got.Version != 3 || len(got.Offices) != 1 || got.Offices[0].OfficeID != "o1" {
		t.Fatalf("got %+v", got)
	}

	if _, err := Open(pub, env, 3); !errors.Is(err, ErrRollback) {
		t.Errorf("same version: err = %v, want ErrRollback", err)
	}
	if _, err := Open(pub, env, 9); !errors.Is(err, ErrRollback) {
		t.Errorf("older version: err = %v, want ErrRollback", err)
	}
	other := newKey(t).Public().(ed25519.PublicKey)
	if _, err := Open(other, env, 0); !errors.Is(err, ErrBadSignature) {
		t.Errorf("wrong key: err = %v, want ErrBadSignature", err)
	}

	tampered := append([]byte{}, payload...)
	tampered[len(tampered)-3] ^= 1
	if _, err := Open(pub, Envelope{Payload: base64.StdEncoding.EncodeToString(tampered), Signature: env.Signature}, 0); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tampered payload: err = %v, want ErrBadSignature", err)
	}
}

func TestSignRejectsZeroVersion(t *testing.T) {
	if _, _, err := Sign(newKey(t), List{}); err == nil {
		t.Fatal("version 0 signed")
	}
}

func TestEmptyListEncodesArray(t *testing.T) {
	priv := newKey(t)
	payload, _, err := Sign(priv, List{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if want := `"offices":[]`; !strings.Contains(string(payload), want) {
		t.Fatalf("payload %s lacks %s", payload, want)
	}
}

func TestPublicKeyString(t *testing.T) {
	pub := newKey(t).Public().(ed25519.PublicKey)
	got, err := ParsePublicKey(PublicKeyString(pub))
	if err != nil || !got.Equal(pub) {
		t.Fatalf("round trip: %v", err)
	}
	for _, bad := range []string{"", "AAAA", "ed25519:", "ed25519:AAAA", "rsa:" + base64.StdEncoding.EncodeToString(pub)} {
		if _, err := ParsePublicKey(bad); err == nil {
			t.Errorf("ParsePublicKey(%q) accepted", bad)
		}
	}
}
