package turncred

import (
	"testing"
	"time"
)

func TestMintAndCheck(t *testing.T) {
	master := []byte("0123456789abcdef0123456789abcdef")
	id := KeyID(master, "5fcd416a-fec0-48ba-a4f2-9b6c7ad43a59")
	if id == KeyID(master, "another-office") || id == KeyID([]byte("other master"), "5fcd416a-fec0-48ba-a4f2-9b6c7ad43a59") {
		t.Fatal("key IDs collide")
	}
	if len(id) != 16 {
		t.Errorf("key id %q", id)
	}
	k := Key{KeyID: id, Secret: Secret(master, id)}
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	user, pass := Mint(k, "tok123", exp)
	u, err := Parse(user)
	if err != nil || u.KeyID != id || u.Token != "tok123" || !u.Expires.Equal(exp) {
		t.Fatalf("parse %q = %+v, %v", user, u, err)
	}
	// The Relay re-derives the secret from the key ID alone.
	if Password(Secret(master, u.KeyID), user) != pass {
		t.Error("relay-side password differs")
	}
	if Password(Secret(master, KeyID(master, "another-office")), user) == pass {
		t.Error("another office's secret matches")
	}
	for _, bad := range []string{"", "1:2", "x:key:tok", "0:key:tok", "1700000000::tok", "1700000000:key:"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("parsed %q", bad)
		}
	}
}
