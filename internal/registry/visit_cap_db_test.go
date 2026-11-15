package registry

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"testing"

	"github.com/zacharyelston/wehelp/internal/officelist"
)

// TestVisitCapInList is #175's Registry half: each office's live video visit
// cap is in the signed list, follows the practice's active providers (one
// more, at least 4), takes support's override, and a change that moves the
// cap publishes a new list while one that doesn't, doesn't.
func TestVisitCapInList(t *testing.T) {
	ctx := context.Background()
	pool := testDB(t)
	key := newSigner(t)
	st := New(pool, key)
	const actor = "cli:test"
	if _, err := st.AddRelay(ctx, actor, Relay{Name: "r", SSHAddress: "h:1", ConnectAddress: "h:2", SSHHostKey: sshKeyLine(t), TLSPin: testPin}); err != nil {
		t.Fatal(err)
	}
	p, err := st.AddPractice(ctx, actor, "Capped", "r")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := st.IssueInstallToken(ctx, actor, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Install(ctx, tok, sshKeyLine(t)); err != nil {
		t.Fatal(err)
	}
	listCap := func() (version int64, cap int) {
		t.Helper()
		env, err := st.LatestList(ctx)
		if err != nil {
			t.Fatal(err)
		}
		l, err := officelist.Verify(key.Public().(ed25519.PublicKey), env)
		if err != nil || len(l.Offices) != 1 {
			t.Fatalf("list: %v %+v", err, l)
		}
		return l.Version, l.Offices[0].MaxVisits
	}
	v0, c := listCap()
	if c != 4 {
		t.Fatalf("no providers: cap %d, want the minimum 4", c)
	}

	// Providers 1-3 keep the minimum: no new list. The 4th makes it 5.
	var ids []Provider
	for i := 1; i <= 4; i++ {
		pr, err := st.AddProvider(ctx, actor, Provider{PracticeID: p.ID, Name: fmt.Sprint("Dr ", i),
			Email: fmt.Sprintf("dr%d@example.test", i), Role: "provider"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, pr)
	}
	if _, err := st.AddProvider(ctx, actor, Provider{PracticeID: p.ID, Name: "Admin", Email: "admin@example.test", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	v1, c := listCap()
	if c != 5 || v1 != v0+1 {
		t.Errorf("4 providers and an admin: cap %d (want 5), versions %d→%d (want one new list)", c, v0, v1)
	}

	// Removing one brings it back to 4; turning one into an admin, to 4 still.
	if err := st.RemoveProvider(ctx, actor, ids[3].ID); err != nil {
		t.Fatal(err)
	}
	if _, c := listCap(); c != 4 {
		t.Errorf("after removal: cap %d, want 4", c)
	}

	// Support's override wins; 0 goes back to automatic.
	n := 12
	if err := st.UpdatePractice(ctx, actor, p.ID, PracticeUpdate{TURNMaxVisits: &n}); err != nil {
		t.Fatal(err)
	}
	if _, c := listCap(); c != 12 {
		t.Errorf("override: cap %d, want 12", c)
	}
	d, err := st.GetPractice(ctx, p.ID)
	if err != nil || d.TURNMaxVisits == nil || *d.TURNMaxVisits != 12 || d.MaxVisits != 12 {
		t.Errorf("practice detail: %+v %v", d, err)
	}
	zero := 0
	if err := st.UpdatePractice(ctx, actor, p.ID, PracticeUpdate{TURNMaxVisits: &zero}); err != nil {
		t.Fatal(err)
	}
	if _, c := listCap(); c != 4 {
		t.Errorf("back to automatic: cap %d, want 4", c)
	}
	bad := 500
	if err := st.UpdatePractice(ctx, actor, p.ID, PracticeUpdate{TURNMaxVisits: &bad}); err == nil {
		t.Error("cap of 500 accepted")
	}
	var audited int
	if err := pool.QueryRow(ctx, `select count(*) from audit_events where action = 'practice.update'
		and detail ? 'turn_max_visits'`).Scan(&audited); err != nil || audited != 2 {
		t.Errorf("override changes audited: %d %v, want 2", audited, err)
	}
	if broken, _, _ := st.VerifyAudit(ctx); broken != 0 {
		t.Errorf("audit broken at %d", broken)
	}
}
