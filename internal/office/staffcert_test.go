package office

import (
	"crypto/x509"
	"testing"
)

func TestStaffCert(t *testing.T) {
	dir := t.TempDir()
	id, _, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	c1, err := id.StaffCert([]string{"Barnacle.local", "192.168.1.55", ""})
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(c1.Certificate[0])
	if err := leaf.CheckSignatureFrom(id.CACert); err != nil {
		t.Fatalf("not issued by the local CA: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(id.CACert)
	for _, host := range []string{"barnacle.local", "192.168.1.55", "localhost", "127.0.0.1"} {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: pool}); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
	if len(c1.Certificate) != 2 {
		t.Errorf("chain has %d certificates, want leaf + CA", len(c1.Certificate))
	}

	// Same names: the same certificate is reused.
	c2, _ := id.StaffCert([]string{"barnacle.local", "192.168.1.55"})
	if string(c2.Certificate[0]) != string(c1.Certificate[0]) {
		t.Error("certificate reissued for the same names")
	}
	// New name: reissued with the same key.
	c3, _ := id.StaffCert([]string{"barnacle.local", "192.168.1.55", "office.example"})
	if string(c3.Certificate[0]) == string(c1.Certificate[0]) {
		t.Error("certificate not reissued for a new name")
	}
	l3, _ := x509.ParseCertificate(c3.Certificate[0])
	if err := l3.VerifyHostname("office.example"); err != nil {
		t.Error(err)
	}
	// The staff files don't upset reloading the identity.
	if _, created, err := Ensure(dir); err != nil || len(created) != 0 {
		t.Fatalf("reload: created %v, err %v", created, err)
	}
}
