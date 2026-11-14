package office

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// The staff web UI's HTTPS certificate (#38), issued by the office's local
// CA. Staff machines trust ca.crt once; the leaf can be reissued at any time.
const (
	StaffKeyFile  = "staff.key"
	StaffCertFile = "staff.crt"
	staffValidity = 397 * 24 * time.Hour // browsers cap server certificates at 398 days
)

// StaffCert returns the staff UI certificate for these host names and IP
// addresses (plus localhost), issuing a new one from the local CA when it is
// missing, about to expire, for other names, or from another CA.
func (id *Identity) StaffCert(hosts []string) (tls.Certificate, error) {
	dns, ips := splitHosts(hosts)
	keyPath, certPath := filepath.Join(id.Dir, StaffKeyFile), filepath.Join(id.Dir, StaffCertFile)
	key, _, err := ensureECDSA(keyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	if c, err := readCert(certPath); err == nil && id.staffCertCurrent(c, key, dns, ips) {
		return tls.LoadX509KeyPair(certPath, keyPath)
	}
	tmpl := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: dns[0], Organization: []string{"WeHelp office"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(staffValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dns,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, id.CACert, &key.PublicKey, id.CAKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	// The chain (leaf + CA) so browsers that trust the CA can verify it.
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: id.CACert.Raw})...)
	if err := writeFile(certPath, chain, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

func (id *Identity) staffCertCurrent(c *x509.Certificate, key *ecdsa.PrivateKey, dns []string, ips []net.IP) bool {
	if !publicKeysEqual(c.PublicKey, &key.PublicKey) || c.CheckSignatureFrom(id.CACert) != nil ||
		time.Until(c.NotAfter) < 30*24*time.Hour {
		return false
	}
	have := slices.Clone(c.DNSNames)
	sort.Strings(have)
	want := slices.Clone(dns)
	sort.Strings(want)
	if !slices.Equal(have, want) || len(c.IPAddresses) != len(ips) {
		return false
	}
	for i := range ips {
		if !c.IPAddresses[i].Equal(ips[i]) {
			return false
		}
	}
	return true
}

// splitHosts separates names from IPs, lowercases names, drops duplicates,
// and always includes localhost.
func splitHosts(hosts []string) ([]string, []net.IP) {
	seen := map[string]bool{}
	var dns []string
	var ips []net.IP
	for _, h := range append(slices.Clone(hosts), "localhost", "127.0.0.1") {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		if ip := net.ParseIP(h); ip != nil {
			ips = append(ips, ip)
		} else {
			dns = append(dns, h)
		}
	}
	return dns, ips
}
