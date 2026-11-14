package server

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/auth"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
)

// TestRefreshRetryAndCodes is #136: a refresh whose reply was lost can be
// retried with the old token while the new one is unused, and refresh
// errors carry a code so the app signs out only when the session is gone.
func TestRefreshRetryAndCodes(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	staff := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	phoneSide := &client{t: t, h: h, relay: true}
	jordan := patientID(t, pool, seed.Patient1Email)
	p, sess := pairPhone(t, staff, phoneSide, jordan)

	refresh := func(token string, at time.Time) (int, map[string]any) {
		t.Helper()
		ts := at.Unix()
		digest := sha256.Sum256(auth.DeviceProofMessage(auth.PurposeRefresh, token, ts))
		sig, _ := ecdsa.SignASN1(rand.Reader, p.key, digest[:])
		return phoneSide.do("POST", "/auth/refresh", map[string]any{
			"refresh_token": token, "timestamp": ts, "signature": base64.StdEncoding.EncodeToString(sig)})
	}
	ok := func(token string) string {
		t.Helper()
		code, out := refresh(token, time.Now())
		if code != http.StatusOK {
			t.Fatalf("refresh: %d %v", code, out)
		}
		return out["refresh_token"].(string)
	}
	wantCode := func(what, token string, at time.Time, code string) {
		t.Helper()
		status, out := refresh(token, at)
		if status != http.StatusUnauthorized || out["code"] != code {
			t.Errorf("%s: %d %v, want 401 %s", what, status, out, code)
		}
	}

	r0 := sess["refresh_token"].(string)
	r1 := ok(r0)
	// The reply carrying r1 was lost: the phone retries with r0, which
	// retires the unused r1 and issues r1b.
	r1b := ok(r0)
	wantCode("lost token after the retry", r1, time.Now(), "invalid_token")
	// Once the phone uses its new token, the old one is finished.
	r2 := ok(r1b)
	wantCode("old token after its successor was used", r0, time.Now(), "invalid_token")
	var retries int
	_ = pool.QueryRow(t.Context(), `select count(*) from audit_events where action = 'auth.refresh_retry'`).Scan(&retries)
	if retries != 1 {
		t.Errorf("%d auth.refresh_retry events, want 1", retries)
	}

	// A skewed clock is not a signed-out session.
	wantCode("skewed clock", r2, time.Now().Add(-time.Hour), "proof_timestamp")
	r3 := ok(r2) // the skewed attempt didn't use up the token

	// A signature from another key fails with proof_failed.
	other := newPhone(t)
	p.key, other.key = other.key, p.key
	wantCode("wrong key", r3, time.Now(), "proof_failed")
	p.key = other.key

	// Revoking the device ends the session and the retry: r3 was rotated
	// to an unused r4, but the practice revoked the phone.
	ok(r3)
	if code, _ := staff.do("DELETE", "/patients/"+jordan+"/devices/"+sess["device_id"].(string), nil); code != http.StatusNoContent {
		t.Fatalf("revoke: %d", code)
	}
	wantCode("retry after revoke", r3, time.Now(), "invalid_token")
}
