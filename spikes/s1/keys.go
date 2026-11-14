package main

// Spike S1 (#32): key and certificate generation. Throwaway code.

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
)

// genKeys writes, into dir:
//
//	relay_host / relay_host.pub   ed25519 SSH host key for the relay
//	office_ssh / office_ssh.pub   ed25519 SSH key the office authenticates with
//	relay_tls.crt / relay_tls.key self-signed P-256 cert for the CONNECT listener
//	office_tls.crt / office_tls.key self-signed P-256 cert the phone pins
//
// and prints the pins the phone needs.
func genKeys(dir, relayIP, officeHost string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"relay_host", "office_ssh"} {
		if err := writeSSHKey(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	relayPin, err := writeCert(filepath.Join(dir, "relay_tls"), "wehelp-relay", nil, []net.IP{net.ParseIP(relayIP)})
	if err != nil {
		return err
	}
	officePin, err := writeCert(filepath.Join(dir, "office_tls"), officeHost, []string{officeHost}, nil)
	if err != nil {
		return err
	}
	fmt.Printf("relay TLS pin  (sha256 of X9.63 public key): %s\n", relayPin)
	fmt.Printf("office TLS pin (sha256 of X9.63 public key): %s\n", officePin)
	return nil
}

func writeSSHKey(path string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return err
	}
	return os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(sshPub), 0o644)
}

// writeCert creates a self-signed P-256 certificate and returns its pin: the
// base64 SHA-256 of the uncompressed (X9.63) public key, which is what iOS
// SecKeyCopyExternalRepresentation returns for an EC key.
func writeCert(path, cn string, dns []string, ips []net.IP) (string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dns,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path+".crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(path+".key", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return "", err
	}
	raw := elliptic.Marshal(elliptic.P256(), key.PublicKey.X, key.PublicKey.Y) //nolint:staticcheck // X9.63 is what iOS exports
	sum := sha256.Sum256(raw)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}
