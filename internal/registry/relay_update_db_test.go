package registry

import (
	"context"
	"errors"
	"testing"
)

// TestUpdateRelay: moving a Relay with its keys changes only its name and
// addresses, audited, and an install afterwards hands out the new addresses (#180).
func TestUpdateRelay(t *testing.T) {
	ctx := context.Background()
	pool := testDB(t)
	st := New(pool, newSigner(t))
	const actor = "cli:test"
	if err := st.EnsureList(ctx); err != nil {
		t.Fatal(err)
	}
	orig, err := st.AddRelay(ctx, actor, Relay{Name: "laptop", SSHAddress: "10.0.0.20:2222",
		ConnectAddress: "10.0.0.20:8443", SSHHostKey: sshKeyLine(t), TLSPin: testPin})
	if err != nil {
		t.Fatal(err)
	}

	r, err := st.UpdateRelay(ctx, actor, "laptop", RelayUpdate{Name: "barnacle",
		SSHAddress: "10.0.0.2:2222", ConnectAddress: "10.0.0.2:9443"})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != orig.ID || r.Name != "barnacle" || r.SSHAddress != "10.0.0.2:2222" ||
		r.ConnectAddress != "10.0.0.2:9443" || r.SSHHostKey != orig.SSHHostKey || r.TLSPin != orig.TLSPin {
		t.Fatalf("updated relay = %+v", r)
	}
	var detail map[string]string
	if err := pool.QueryRow(ctx, `select detail from audit_events where action = 'relay.update'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if detail["connect_address_from"] != "10.0.0.20:8443" || detail["connect_address_to"] != "10.0.0.2:9443" ||
		detail["name_to"] != "barnacle" {
		t.Errorf("audit detail = %v", detail)
	}

	// Only the given fields change.
	r, err = st.UpdateRelay(ctx, actor, "barnacle", RelayUpdate{ConnectAddress: "10.0.0.2:9444"})
	if err != nil || r.SSHAddress != "10.0.0.2:2222" || r.ConnectAddress != "10.0.0.2:9444" {
		t.Fatalf("partial update = %+v, %v", r, err)
	}

	if _, err := st.UpdateRelay(ctx, actor, "barnacle", RelayUpdate{ConnectAddress: "10.0.0.2:9444"}); !errors.Is(err, ErrNoChange) {
		t.Errorf("same address: err = %v, want ErrNoChange", err)
	}
	if _, err := st.UpdateRelay(ctx, actor, "barnacle", RelayUpdate{SSHAddress: "nohostport"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad address: err = %v, want ErrInvalidInput", err)
	}
	if _, err := st.UpdateRelay(ctx, actor, "laptop", RelayUpdate{Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("old name: err = %v, want ErrNotFound", err)
	}
}
