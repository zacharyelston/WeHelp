package relay

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zacharyelston/wehelp/internal/keyfile"
	"golang.org/x/crypto/ssh"
)

// Files in the Relay directory (config relay_dir).
const (
	HostKeyFile = "ssh_host_ed25519"     // OpenSSH or PKCS#8 PEM, mode 600
	HostPubFile = "ssh_host_ed25519.pub" // registered with the Registry
	TLSKeyFile  = "tls.key"              // P-256, pinned by phones
	TLSCertFile = "tls.crt"
	CacheFile   = "office-list.json" // last good signed list
)

// Keys is the Relay's identity.
type Keys struct {
	HostKey ssh.Signer
	TLSCert tls.Certificate
	TLSPin  string // keyfile.P256Pin of the TLS key
}

// EnsureKeys loads the Relay keys from dir, creating any that are missing.
// It never replaces a key. It returns the files it created.
func EnsureKeys(dir string) (*Keys, []string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	var created []string
	hostPath := filepath.Join(dir, HostKeyFile)
	if _, err := os.Stat(hostPath); errors.Is(err, fs.ErrNotExist) {
		if _, err := keyfile.NewEd25519(hostPath); err != nil {
			return nil, nil, err
		}
		created = append(created, HostKeyFile)
	}
	if err := keyfile.CheckPrivate(hostPath); err != nil {
		return nil, nil, err
	}
	raw, err := os.ReadFile(hostPath)
	if err != nil {
		return nil, nil, err
	}
	signer, err := ssh.ParsePrivateKey(raw) // OpenSSH (ssh-keygen) or PKCS#8
	if err != nil {
		return nil, nil, fmt.Errorf("relay: host key: %w", err)
	}
	pubPath := filepath.Join(dir, HostPubFile)
	if _, err := os.Stat(pubPath); errors.Is(err, fs.ErrNotExist) {
		line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " wehelp-relay\n"
		if err := os.WriteFile(pubPath, []byte(line), 0o644); err != nil {
			return nil, nil, err
		}
		created = append(created, HostPubFile)
	}

	keyPath, certPath := filepath.Join(dir, TLSKeyFile), filepath.Join(dir, TLSCertFile)
	var tlsKey *ecdsa.PrivateKey
	if _, err := os.Stat(keyPath); errors.Is(err, fs.ErrNotExist) {
		if tlsKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			return nil, nil, err
		}
		if err := keyfile.Write(keyPath, tlsKey); err != nil {
			return nil, nil, err
		}
		created = append(created, TLSKeyFile)
	} else {
		k, err := keyfile.Read(keyPath)
		if err != nil {
			return nil, nil, err
		}
		var ok bool
		if tlsKey, ok = k.(*ecdsa.PrivateKey); !ok || tlsKey.Curve != elliptic.P256() {
			return nil, nil, fmt.Errorf("relay: %s is not a P-256 key", keyPath)
		}
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil || cert.Leaf == nil || time.Until(cert.Leaf.NotAfter) < 30*24*time.Hour {
		// Phones pin the key, not the certificate, so it can be reissued.
		if err := writeRelayCert(certPath, tlsKey); err != nil {
			return nil, nil, err
		}
		created = append(created, TLSCertFile)
		if cert, err = tls.LoadX509KeyPair(certPath, keyPath); err != nil {
			return nil, nil, err
		}
	}
	return &Keys{HostKey: signer, TLSCert: cert, TLSPin: keyfile.P256Pin(&tlsKey.PublicKey)}, created, nil
}

func writeRelayCert(path string, key *ecdsa.PrivateKey) error {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "relay" + hostSuffix, Organization: []string{"WeHelp Relay"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(2, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"relay" + hostSuffix},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}
