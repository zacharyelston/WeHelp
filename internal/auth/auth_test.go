package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil || !ok {
		t.Fatalf("expected match, ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword("wrong", hash)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected mismatch")
	}
}

func TestAccessTokenRoundTrip(t *testing.T) {
	secret := []byte("test-secret-that-is-long-enough")
	uid, tid := uuid.New(), uuid.New()

	tok, err := IssueAccessToken(uid, tid, "provider", secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ParseAccessToken(tok, secret)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != uid.String() || claims.TenantID != tid || claims.Role != "provider" {
		t.Fatalf("claims mismatch: %+v", claims)
	}

	if _, err := ParseAccessToken(tok, []byte("other-secret")); err == nil {
		t.Fatal("expected signature failure with wrong secret")
	}

	expired, err := IssueAccessToken(uid, tid, "provider", secret, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAccessToken(expired, secret); err == nil {
		t.Fatal("expected expiry failure")
	}
}

func TestRefreshTokenRoundTrip(t *testing.T) {
	tok, hash, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	got, err := HashRefreshToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(hash) {
		t.Fatal("hash mismatch")
	}
	if _, err := HashRefreshToken("not-base64!!!"); err == nil {
		t.Fatal("expected malformed-token error")
	}
}
