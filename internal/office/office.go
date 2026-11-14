// Package office is the Back Office's own identity (#35): the office SSH key
// it authenticates to the Relay with, the office TLS key phones pin, the local
// CA for the staff UI, and the registration it got from the Registry.
//
// All of it lives in one directory outside the repo (config office_dir). Keys
// are mode 600 and are never overwritten: rotation is a documented manual
// step (docs/OFFICE_KEYS.md).
package office

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
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

// File names inside the office directory.
const (
	SSHKeyFile       = "ssh_ed25519.key"
	SSHPubFile       = "ssh_ed25519.pub"
	TLSKeyFile       = "tls.key"
	TLSCertFile      = "tls.crt"
	CAKeyFile        = "ca.key"
	CACertFile       = "ca.crt"
	RegistrationFile = "registration.json"
)

// HostSuffix is the reserved name space phones use for offices. The app's
// scoped ATS exception covers exactly this domain (spike S1).
const HostSuffix = ".wehelp.invalid"

// Identity is the loaded office identity.
type Identity struct {
	Dir          string
	SSHKey       ed25519.PrivateKey
	TLSKey       *ecdsa.PrivateKey
	CAKey        *ecdsa.PrivateKey
	CACert       *x509.Certificate
	Registration *Registration // nil until installed
}

// Registration is what the Registry returned at install.
type Registration struct {
	OfficeID           string    `json:"office_id"`
	RegistryURL        string    `json:"registry_url"`
	RegistrySigningKey string    `json:"registry_signing_key"`
	Relay              Relay     `json:"relay"`
	InstalledAt        time.Time `json:"installed_at"`
}

// Relay is the office's assigned Relay.
type Relay struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	SSHAddress     string `json:"ssh_address"`
	ConnectAddress string `json:"connect_address"`
	SSHHostKey     string `json:"ssh_host_key"`
}

// Ensure loads the office identity from dir, generating any key that does
// not exist yet. Existing keys are never replaced. It returns the names of
// the files it created.
func Ensure(dir string) (*Identity, []string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, nil, err
	}
	id := &Identity{Dir: dir}
	var created []string
	note := func(names ...string) { created = append(created, names...) }

	// Office SSH key.
	sshPath := filepath.Join(dir, SSHKeyFile)
	if exists(sshPath) {
		k, err := keyfile.ReadEd25519(sshPath)
		if err != nil {
			return nil, nil, err
		}
		id.SSHKey = k
	} else {
		k, err := keyfile.NewEd25519(sshPath)
		if err != nil {
			return nil, nil, err
		}
		id.SSHKey = k
		note(SSHKeyFile)
	}
	if !exists(filepath.Join(dir, SSHPubFile)) {
		if err := writeFile(filepath.Join(dir, SSHPubFile), []byte(id.SSHAuthorizedKey()+"\n"), 0o644); err != nil {
			return nil, nil, err
		}
		note(SSHPubFile)
	}

	// Office TLS key (P-256, pinned by phones) and its certificate.
	k, made, err := ensureECDSA(filepath.Join(dir, TLSKeyFile))
	if err != nil {
		return nil, nil, err
	}
	id.TLSKey = k
	if made {
		note(TLSKeyFile)
	}

	// Local CA for the staff UI on the LAN (#38 issues its server certificate).
	if id.CAKey, made, err = ensureECDSA(filepath.Join(dir, CAKeyFile)); err != nil {
		return nil, nil, err
	}
	if made {
		note(CAKeyFile)
	}
	caPath := filepath.Join(dir, CACertFile)
	if !exists(caPath) || made {
		if made && exists(caPath) {
			return nil, nil, fmt.Errorf("office: %s exists without its key; move it aside", caPath)
		}
		if err := writeCACert(caPath, id.CAKey); err != nil {
			return nil, nil, err
		}
		note(CACertFile)
	}
	if id.CACert, err = readCert(caPath); err != nil {
		return nil, nil, err
	}
	if !publicKeysEqual(id.CACert.PublicKey, &id.CAKey.PublicKey) {
		return nil, nil, fmt.Errorf("office: %s does not match %s", CACertFile, CAKeyFile)
	}

	if id.Registration, err = readRegistration(filepath.Join(dir, RegistrationFile)); err != nil {
		return nil, nil, err
	}

	// The TLS certificate can always be reissued: phones pin the key, not the
	// certificate. Reissue it when missing or when its names are stale.
	certPath := filepath.Join(dir, TLSCertFile)
	if fresh, err := id.tlsCertCurrent(certPath); err != nil || !fresh {
		if err := id.WriteTLSCert(); err != nil {
			return nil, nil, err
		}
		note(TLSCertFile)
	}
	return id, created, nil
}

// SSHAuthorizedKey is the office SSH public key in authorized_keys form.
func (id *Identity) SSHAuthorizedKey() string {
	pk, err := ssh.NewPublicKey(id.SSHKey.Public())
	if err != nil {
		panic(err) // ed25519 keys always convert
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
}

// KeyPin is what phones pin: base64 SHA-256 of the office TLS public key in
// uncompressed X9.63 form (0x04 || X || Y). That is the subjectPublicKey of
// the certificate's SPKI, and what iOS SecKeyCopyExternalRepresentation
// returns, so the app can check it without parsing ASN.1 (spike S1).
func (id *Identity) KeyPin() string { return KeyPin(&id.TLSKey.PublicKey) }

// KeyPin computes the pin for any P-256 public key.
func KeyPin(pub *ecdsa.PublicKey) string { return keyfile.P256Pin(pub) }

// Host is the name phones use for this office: "<office-id>.wehelp.invalid".
// Empty before install.
func (id *Identity) Host() string {
	if id.Registration == nil {
		return ""
	}
	return id.Registration.OfficeID + HostSuffix
}

// Pairing is the office part of the QR pairing payload (#37). The invite
// code is added per patient by the pairing flow.
type Pairing struct {
	RelayAddress string `json:"relay"`   // host:port of the Relay's CONNECT listener
	OfficeID     string `json:"office"`  // also the inner TLS host: <office>.wehelp.invalid
	KeyPin       string `json:"key_pin"` // see KeyPin
}

// Pairing returns the office's pairing identity, or an error before install.
func (id *Identity) Pairing() (Pairing, error) {
	if id.Registration == nil {
		return Pairing{}, errors.New("office: not installed with the Registry yet (wehelpd init --install-token)")
	}
	return Pairing{RelayAddress: id.Registration.Relay.ConnectAddress,
		OfficeID: id.Registration.OfficeID, KeyPin: id.KeyPin()}, nil
}

func (id *Identity) tlsDNSNames() []string {
	names := []string{"office" + HostSuffix}
	if h := id.Host(); h != "" {
		names = append(names, h)
	}
	return names
}

func (id *Identity) tlsCertCurrent(path string) (bool, error) {
	if !exists(path) {
		return false, nil
	}
	c, err := readCert(path)
	if err != nil {
		return false, err
	}
	if !publicKeysEqual(c.PublicKey, &id.TLSKey.PublicKey) || time.Until(c.NotAfter) < 30*24*time.Hour {
		return false, nil
	}
	want := id.tlsDNSNames()
	if len(c.DNSNames) != len(want) {
		return false, nil
	}
	for i := range want {
		if c.DNSNames[i] != want[i] {
			return false, nil
		}
	}
	return true, nil
}

// WriteTLSCert (re)issues the self-signed office TLS certificate for the
// current names. The key, and so the pin, does not change.
func (id *Identity) WriteTLSCert() error {
	names := id.tlsDNSNames()
	tmpl := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: names[len(names)-1], Organization: []string{"WeHelp office"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(2, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     names,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &id.TLSKey.PublicKey, id.TLSKey)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(id.Dir, TLSCertFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

func writeCACert(path string, key *ecdsa.PrivateKey) error {
	tmpl := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: "WeHelp office local CA", Organization: []string{"WeHelp office"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	return writeFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

func ensureECDSA(path string) (*ecdsa.PrivateKey, bool, error) {
	if exists(path) {
		k, err := keyfile.Read(path)
		if err != nil {
			return nil, false, err
		}
		ek, ok := k.(*ecdsa.PrivateKey)
		if !ok || ek.Curve != elliptic.P256() {
			return nil, false, fmt.Errorf("office: %s is not a P-256 key", path)
		}
		return ek, false, nil
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, false, err
	}
	if err := keyfile.Write(path, k); err != nil {
		return nil, false, err
	}
	return k, true, nil
}

// SaveRegistration stores the Registry's answer (mode 600) and reissues the
// TLS certificate for the office's host name.
func (id *Identity) SaveRegistration(r Registration) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(id.Dir, RegistrationFile), append(b, '\n'), 0o600); err != nil {
		return err
	}
	id.Registration = &r
	return id.WriteTLSCert()
}

func readRegistration(path string) (*Registration, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Registration
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("office: %s: %w", path, err)
	}
	return &r, nil
}

func readCert(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("office: %s is not a PEM certificate", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

func publicKeysEqual(a any, b *ecdsa.PublicKey) bool {
	ak, ok := a.(*ecdsa.PublicKey)
	return ok && ak.Equal(b)
}

func randomSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		panic(err)
	}
	return n
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writeFile replaces path atomically with the given mode.
func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
